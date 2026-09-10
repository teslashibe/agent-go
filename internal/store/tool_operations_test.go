package store

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

func TestToolProgressSurvivesRestartWithoutCrossSourceDisclosure(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "progress.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	source := Source{Name: "test", Sender: "sender", ChatGUID: "chat", ChatID: 1}
	if err := s.Initialize(ctx, source, 0, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO jobs(id,source,guid,prompt,created_at,state) VALUES(1,?,'fixture','test',0,'running')`, source.key()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ClaimToolOperation(ctx, source, 1, "create", `{}`); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordToolProgress(ctx, source, 1, "create", 0, "durable invitation"); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteToolOperation(ctx, source, 1, "create", "completed"); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteTurn(ctx, source, 1, "", []string{"invitation reply"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i := 0; i < 2; i++ {
		progress, err := s.ToolProgress(ctx, source, 1)
		if err != nil || len(progress) != 1 || progress[0] != "durable invitation" {
			t.Fatal(progress, err)
		}
	}
	other := source
	other.ChatID++
	if progress, err := s.ToolProgress(ctx, other, 1); err != nil || len(progress) != 0 {
		t.Fatal("cross-source progress", progress, err)
	}
	if progress, err := s.ToolProgress(ctx, source, 2); err != nil || len(progress) != 0 {
		t.Fatal("cross-job progress", progress, err)
	}
	var pending int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM replies WHERE job_id=1 AND state='pending'`).Scan(&pending); err != nil || pending != 1 {
		t.Fatal("read changed reply state", pending, err)
	}
}

func TestToolOperationClaims(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	source := Source{Name: "test", Sender: "sender", ChatGUID: "chat", ChatID: 1}
	if err := s.Initialize(ctx, source, 0, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO jobs(id,source,guid,prompt,created_at,state) VALUES(1,?,'real-test-guid','test',0,'running')`, source.key()); err != nil {
		t.Fatal(err)
	}
	var claims atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			claimed, _, err := s.ClaimToolOperation(ctx, source, 1, "item-1", `{"text":"chips"}`)
			if claimed {
				claims.Add(1)
			}
			if err != nil && !errors.Is(err, ErrUncertain) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if claims.Load() != 1 {
		t.Fatalf("claims=%d", claims.Load())
	}
	other := source
	other.ChatID = 2
	if claimed, _, err := s.ClaimToolOperation(ctx, other, 1, "other", `{}`); claimed || err == nil {
		t.Fatal("cross-source claim allowed")
	}
	if err := s.CompleteToolOperation(ctx, source, 1, "item-1", `{"added":true}`); err != nil {
		t.Fatal(err)
	}
	claimed, result, err := s.ClaimToolOperation(ctx, source, 1, "item-1", `{"text":"chips"}`)
	if err != nil || claimed || result != `{"added":true}` {
		t.Fatalf("cached result: %t %s %v", claimed, result, err)
	}
	if claimed, _, err := s.ClaimToolOperation(ctx, source, 1, "item-1", `{"text":"walnuts"}`); claimed || err == nil {
		t.Fatal("changed arguments allowed")
	}
	if claimed, _, err := s.ClaimToolOperation(ctx, source, 1, "item-2", `{"text":"walnuts"}`); !claimed || err != nil {
		t.Fatal("second operation not allowed", err)
	}
	if claimed, _, err := s.ClaimToolOperation(ctx, source, 1, "new-id-after-unresolved-write", `{}`); claimed || !errors.Is(err, ErrUncertain) {
		t.Fatal("unresolved write allowed a fresh operation ID", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var state string
	if err := s.db.QueryRow(`SELECT state FROM tool_operations WHERE operation_id='item-2'`).Scan(&state); err != nil || state != "unknown" {
		t.Fatalf("restart state=%s err=%v", state, err)
	}
	if claimed, _, err := s.ClaimToolOperation(ctx, source, 1, "item-2", `{"text":"walnuts"}`); claimed || err == nil {
		t.Fatal("interrupted operation replayed")
	}
	if err := s.db.QueryRow(`SELECT state FROM tool_operations WHERE operation_id='item-1'`).Scan(&state); err != nil || state != "completed" {
		t.Fatalf("completed operation lost: %s %v", state, err)
	}
}

func TestReactionClaimRespectsEarlyAcknowledgement(t *testing.T) {
	for _, tc := range []struct {
		state, reaction string
		allowed         bool
	}{
		{"dispatching", "like", false}, {"submitted", "like", false}, {"unknown", "like", false},
		{"submitted", "none", true}, {"dispatching", "", true},
	} {
		t.Run(tc.state+"/"+tc.reaction, func(t *testing.T) {
			s, err := Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			ctx := context.Background()
			src := Source{Name: "fixture", Sender: "owner", ChatGUID: "chat", ChatID: 1}
			if err = s.Initialize(ctx, src, 0, ""); err != nil {
				t.Fatal(err)
			}
			if _, err = s.db.Exec(`INSERT INTO jobs(id,source,guid,prompt,created_at,state,ack_state,ack_reaction) VALUES(1,?,'fixture','fixture',0,'running',?,?)`, src.key(), tc.state, tc.reaction); err != nil {
				t.Fatal(err)
			}
			claimed, _, err := s.ClaimToolOperation(ctx, src, 1, "fresh-id", `{"Name":"react","Args":{"reaction":"love"}}`)
			if claimed != tc.allowed || (err == nil) != tc.allowed {
				t.Fatal(claimed, err)
			}
			var state, reaction string
			if err = s.db.QueryRow(`SELECT ack_state,ack_reaction FROM jobs WHERE id=1`).Scan(&state, &reaction); err != nil || state != tc.state || reaction != tc.reaction {
				t.Fatal("acknowledgement evidence changed", err)
			}
		})
	}
}

func TestAcknowledgementOutcomePreservesLegacyEvidence(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	src := Source{Name: "fixture", Sender: "owner", ChatGUID: "chat", ChatID: 1}
	if err = s.Initialize(ctx, src, 0, ""); err != nil {
		t.Fatal(err)
	}
	for i, outcome := range []string{"accepted", "skipped", "unknown"} {
		id := int64(i + 1)
		if _, err = s.db.Exec(`INSERT INTO jobs(id,source,guid,prompt,created_at,state,ack_state) VALUES(?,?,?,'fixture',0,'running','dispatching')`, id, src.key(), outcome); err != nil {
			t.Fatal(err)
		}
		if err = s.FinishAcknowledgement(ctx, src, id, outcome); err != nil {
			t.Fatal(err)
		}
		got, err := s.AcknowledgementOutcome(ctx, src, id)
		if err != nil || got != outcome {
			t.Fatal(got, err)
		}
	}
	if _, err = s.db.Exec(`INSERT INTO jobs(id,source,guid,prompt,created_at,state,ack_state,ack_reaction) VALUES(9,?,'old','original',0,'completed','submitted','like')`, src.key()); err != nil {
		t.Fatal(err)
	}
	if err = s.migrateAcknowledgements(); err != nil {
		t.Fatal(err)
	}
	got, err := s.AcknowledgementOutcome(ctx, src, 9)
	if err != nil || got != "unrecorded" {
		t.Fatal("legacy row falsely upgraded", got, err)
	}
	var state, prompt string
	if err = s.db.QueryRow(`SELECT ack_state,prompt FROM jobs WHERE id=9`).Scan(&state, &prompt); err != nil || state != "submitted" || prompt != "original" {
		t.Fatal("legacy evidence changed", err)
	}
}

func TestAcknowledgementOutcomesSurviveRestart(t *testing.T) {
	for _, tc := range []struct {
		name, state, outcome, wantState, wantOutcome string
	}{
		{"accepted", "submitted", "accepted", "submitted", "accepted"},
		{"skipped", "submitted", "skipped", "submitted", "skipped"},
		{"unknown", "unknown", "unknown", "unknown", "unknown"},
		{"legacy", "submitted", "", "submitted", "unrecorded"},
		{"interrupted", "dispatching", "", "unknown", "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "state.db")
			s, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { s.Close() })
			src := Source{Name: "fixture", Sender: "owner", ChatGUID: "chat", ChatID: 1}
			if err = s.Initialize(ctx, src, 0, ""); err != nil {
				t.Fatal(err)
			}
			if _, err = s.db.Exec(`INSERT INTO jobs(id,source,guid,prompt,created_at,state,ack_state,ack_reaction,ack_outcome) VALUES(1,?,'original-guid','original prompt',0,'running',?,'love',?)`, src.key(), tc.state, tc.outcome); err != nil {
				t.Fatal(err)
			}
			if err = s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = Open(path)
			if err != nil {
				t.Fatal(err)
			}
			got, err := s.AcknowledgementOutcome(ctx, src, 1)
			if err != nil || got != tc.wantOutcome {
				t.Fatalf("outcome=%q want=%q err=%v", got, tc.wantOutcome, err)
			}
			var state, reaction, guid, prompt, jobState string
			if err = s.db.QueryRow(`SELECT ack_state,ack_reaction,guid,prompt,state FROM jobs WHERE id=1`).Scan(&state, &reaction, &guid, &prompt, &jobState); err != nil {
				t.Fatal(err)
			}
			if state != tc.wantState || reaction != "love" || guid != "original-guid" || prompt != "original prompt" || jobState != "unknown" {
				t.Fatalf("restart lost acknowledgement evidence or running-job protection: %q %q %q %q %q", state, reaction, guid, prompt, jobState)
			}
			if claimed, _, err := s.ClaimToolOperation(ctx, src, 1, "after-restart", `{"Name":"react","Args":{"reaction":"love"}}`); claimed || err == nil {
				t.Fatal("restart allowed a reaction on the interrupted job")
			}
		})
	}
}
