package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestSourceAllowedIdentities(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	source := Source{Name: "upstream", AllowedSenders: []string{"second", "first"}, Group: true, ChatGUID: "any;+;group", ChatID: 1}
	if err := s.CheckHistory(ctx, source, []Anchor{{ID: 10, GUID: "baseline"}}); err != nil {
		t.Fatal(err)
	}
	reordered := source
	reordered.AllowedSenders = []string{"first", "second", "first"}
	if cursor, err := s.Cursor(ctx, reordered); err != nil || cursor != 10 {
		t.Fatalf("equivalent sender set: %d, %v", cursor, err)
	}
	if source.AllowedSenders[0] != "second" {
		t.Fatal("key changed caller slice")
	}
	for _, mutate := range []func(*Source){
		func(s *Source) { s.AllowedSenders = []string{"first"} },
		func(s *Source) { s.AllowedSenders = []string{"first", "third"} },
		func(s *Source) { s.AllowedSenders = []string{"first", "second", "third"} },
		func(s *Source) { s.Group = false },
		func(s *Source) { s.ChatGUID = "any;+;other" },
		func(s *Source) { s.ChatID = 2 },
	} {
		changed := source
		mutate(&changed)
		if _, err := s.Cursor(ctx, changed); !errors.Is(err, ErrUninitialized) {
			t.Fatalf("reused mismatched identity: %+v, %v", changed, err)
		}
		if err := s.CheckHistory(ctx, changed, []Anchor{{ID: 20, GUID: "new-baseline"}}); err != nil {
			t.Fatal(err)
		}
		if cursor, err := s.Cursor(ctx, changed); err != nil || cursor != 20 {
			t.Fatalf("fresh baseline: %d, %v", cursor, err)
		}
		job, reply, err := s.ClaimNext(ctx, changed, time.Now())
		if err != nil || job != nil || reply != nil {
			t.Fatalf("baseline queued work: %+v, %+v, %v", job, reply, err)
		}
	}
	legacy := Source{Name: "upstream", Sender: "first", ChatGUID: "iMessage;-;first", ChatID: 2}
	if err := s.Initialize(ctx, legacy, 5, ""); err != nil {
		t.Fatal(err)
	}
	modern := legacy
	modern.Sender = ""
	modern.AllowedSenders = []string{"first"}
	if cursor, err := s.Cursor(ctx, modern); err != nil || cursor != 5 {
		t.Fatalf("legacy compatibility: %d, %v", cursor, err)
	}
	if legacy.key() != `{"Name":"upstream","Sender":"first","ChatGUID":"iMessage;-;first","ChatID":2}` {
		t.Fatalf("legacy key changed: %s", legacy.key())
	}
}

func TestStatusJSONFieldNames(t *testing.T) {
	for _, status := range []Status{{}, {UnknownTurns: 2, UnresolvedReplies: 3}} {
		data, err := json.Marshal(status)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(data, &fields); err != nil {
			t.Fatal(err)
		}
		for name, want := range map[string]int{"UnknownTurns": status.UnknownTurns, "UnresolvedReplies": status.UnresolvedReplies} {
			var got int
			if err := json.Unmarshal(fields[name], &got); err != nil || got != want {
				t.Fatalf("field %s: got %d, want %d, error %v; JSON: %s", name, got, want, err, data)
			}
		}
		for _, oldName := range []string{"Unknown", "PendingReplies"} {
			if _, ok := fields[oldName]; ok {
				t.Fatalf("obsolete field %s in JSON: %s", oldName, data)
			}
		}
	}
}

func TestDiscardRepliesAndResumePreservesQueueAndReplacesSession(t *testing.T) {
	for _, session := range []string{"", "replacement-session"} {
		t.Run(fmt.Sprintf("session=%q", session), func(t *testing.T) {
			ctx := context.Background()
			s, err := Open(filepath.Join(t.TempDir(), "state.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			source := Source{Name: "upstream", Sender: "sender", ChatGUID: "chat", ChatID: 1}
			if err := s.Initialize(ctx, source, 0, "gen"); err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			for id, prompt := range []string{"completed", "uncertain", "queued"} {
				if _, err := s.Accept(ctx, source, Event{ID: int64(id + 1), GUID: prompt, Text: prompt, CreatedAt: now}, "turn"); err != nil {
					t.Fatal(err)
				}
			}
			// Persist a recovery snapshot with both uncertain and unsent replies.
			if _, err := s.db.Exec(`UPDATE jobs SET state='completed' WHERE guid='completed';
UPDATE jobs SET state='unknown' WHERE guid='uncertain';
UPDATE sources SET session='previous-session',paused=1;
INSERT INTO replies(job_id,ordinal,text,state)
 SELECT id,0,'already sent','submitted' FROM jobs WHERE guid='completed';
INSERT INTO replies(job_id,ordinal,text,state)
 SELECT id,1,'delivery uncertain','unknown' FROM jobs WHERE guid='completed';
INSERT INTO replies(job_id,ordinal,text,state)
 SELECT id,2,'not sent','pending' FROM jobs WHERE guid='completed';`); err != nil {
				t.Fatal(err)
			}
			before, err := s.Status(ctx, source)
			if err != nil || before.UnknownTurns != 1 || before.UnresolvedReplies != 2 || before.Queued != 1 || !before.Paused {
				t.Fatalf("before discard: %+v, %v", before, err)
			}
			if err := s.DiscardRepliesAndResume(ctx, source, session); err != nil {
				t.Fatal(err)
			}
			status, err := s.Status(ctx, source)
			if err != nil || status != (Status{Cursor: 3, SessionID: session, Queued: 1}) {
				t.Fatalf("after discard: %+v, %v", status, err)
			}
			var state, reason string
			if err := s.db.QueryRow(`SELECT state,error FROM jobs WHERE guid='uncertain'`).Scan(&state, &reason); err != nil || state != "failed" || reason != "manually abandoned" {
				t.Fatalf("uncertain turn: state=%q reason=%q error=%v", state, reason, err)
			}
			var count int
			if err := s.db.QueryRow(`SELECT COUNT(*) FROM replies`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("remaining replies: %d, %v", count, err)
			}
			if err := s.db.QueryRow(`SELECT state FROM replies`).Scan(&state); err != nil || state != "submitted" {
				t.Fatalf("submitted reply not retained: %q, %v", state, err)
			}
			job, reply, err := s.ClaimNext(ctx, source, now)
			if err != nil || reply != nil || job == nil || job.Prompt != "queued" || job.SessionID != session {
				t.Fatalf("resumed queue: job=%+v reply=%+v error=%v", job, reply, err)
			}
			if err := s.CompleteTurn(ctx, source, job.ID, session, nil); err != nil {
				t.Fatal(err)
			}
			job, reply, err = s.ClaimNext(ctx, source, now)
			if err != nil || job != nil || reply != nil {
				t.Fatalf("abandoned work retried: job=%+v reply=%+v error=%v", job, reply, err)
			}
		})
	}
}

func TestHistoryAnchorValidation(t *testing.T) {
	for _, test := range []struct {
		name    string
		history []Anchor
		wantErr bool
	}{
		{"equal row wrong GUID", []Anchor{{10, "replacement"}}, true},
		{"higher row wrong GUID", []Anchor{{11, "new"}, {10, "replacement"}}, true},
		{"equal matching", []Anchor{{10, "initial"}}, false},
		{"higher matching", []Anchor{{11, "new"}, {10, "initial"}}, false},
		{"anchor outside window", []Anchor{{11, "new"}}, true},
		{"empty history", nil, true},
		{"lower highwater", []Anchor{{9, "old"}}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "state.sqlite")
			s, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			source := Source{Name: "upstream", Sender: "sender", ChatGUID: "chat", ChatID: 1}
			if err := s.CheckHistory(ctx, source, []Anchor{{10, "initial"}, {9, "old"}}); err != nil {
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
			err = s.CheckHistory(ctx, source, test.history)
			if test.wantErr && !errors.Is(err, ErrUncertain) || !test.wantErr && err != nil {
				t.Fatalf("CheckHistory: %v", err)
			}
			status, err := s.Status(ctx, source)
			if err != nil || status.Cursor != 10 || status.Paused != test.wantErr {
				t.Fatalf("status: %+v, %v", status, err)
			}
			var generation string
			if err := s.db.QueryRow(`SELECT generation FROM sources`).Scan(&generation); err != nil || generation != "" {
				t.Fatalf("invented generation: %q, %v", generation, err)
			}
			if test.wantErr {
				if _, _, err := s.ClaimNext(ctx, source, time.Now()); !errors.Is(err, ErrUncertain) {
					t.Fatalf("worker not paused: %v", err)
				}
				if err := s.DiscardRepliesAndResume(ctx, source, ""); !errors.Is(err, ErrUncertain) {
					t.Fatalf("resumed invalid source: %v", err)
				}
			}
		})
	}
}

func TestHistoryPrefersCursorInboxAnchor(t *testing.T) {
	for _, matching := range []bool{true, false} {
		t.Run(fmt.Sprint(matching), func(t *testing.T) {
			ctx := context.Background()
			s, err := Open(filepath.Join(t.TempDir(), "state.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			source := Source{Name: "upstream", Sender: "sender", ChatGUID: "chat", ChatID: 1}
			if err := s.CheckHistory(ctx, source, []Anchor{{10, "initial"}}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Accept(ctx, source, Event{ID: 20, GUID: "cursor"}, "status"); err != nil {
				t.Fatal(err)
			}
			history := []Anchor{{21, "latest"}, {20, "cursor"}}
			if !matching {
				// A matching initial anchor must not override a wrong cursor GUID.
				history = []Anchor{{21, "latest"}, {20, "replacement"}, {10, "initial"}}
			}
			err = s.CheckHistory(ctx, source, history)
			if matching && err != nil || !matching && !errors.Is(err, ErrUncertain) {
				t.Fatalf("CheckHistory: %v", err)
			}
		})
	}
}

func TestLegacyHistoryAnchorMigration(t *testing.T) {
	for _, withInbox := range []bool{false, true} {
		t.Run(fmt.Sprint(withInbox), func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "state.sqlite")
			s, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			source := Source{Name: "upstream", Sender: "sender", ChatGUID: "chat", ChatID: 1}
			if err := s.Initialize(ctx, source, 10, "upstream"); err != nil {
				t.Fatal(err)
			}
			if withInbox {
				if _, err := s.Accept(ctx, source, Event{ID: 20, GUID: "cursor"}, "status"); err != nil {
					t.Fatal(err)
				}
			}
			// Simulate the published schema, which has no initial anchor table.
			if _, err := s.db.Exec(`DROP TABLE source_anchors`); err != nil {
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
			err = s.CheckHistory(ctx, source, []Anchor{{20, "cursor"}, {10, "initial"}})
			if withInbox && err != nil || !withInbox && !errors.Is(err, ErrUncertain) {
				t.Fatalf("CheckHistory: %v", err)
			}
			var count int
			if err := s.db.QueryRow(`SELECT COUNT(*) FROM source_anchors`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("silently baselined legacy state: %d, %v", count, err)
			}
		})
	}
}

func TestCursorInitializationAndSourceIsolation(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	source := Source{Name: "upstream", Sender: "sender", ChatGUID: "chat", ChatID: 1}
	if _, err = s.Cursor(ctx, source); !errors.Is(err, ErrUninitialized) {
		t.Fatal(err)
	}
	if err = s.Initialize(ctx, source, 42, "gen"); err != nil {
		t.Fatal(err)
	}
	if err = s.Initialize(ctx, source, 900, "different"); err != nil {
		t.Fatal(err)
	}
	cursor, err := s.Cursor(ctx, source)
	if err != nil || cursor != 42 {
		t.Fatalf("%d %v", cursor, err)
	}
	other := source
	other.Sender = "another"
	if _, err = s.Cursor(ctx, other); !errors.Is(err, ErrUninitialized) {
		t.Fatal(err)
	}
}

func TestSourceRegressionAndGenerationFailClosed(t *testing.T) {
	for _, test := range []struct {
		name, generation string
		highWater        int64
	}{
		{"regression", "gen", 9},
		{"generation", "new-gen", 20},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			s, err := Open(filepath.Join(t.TempDir(), "state.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			source := Source{Name: "upstream", Sender: "sender", ChatGUID: "chat", ChatID: 1}
			if err = s.Initialize(ctx, source, 10, "gen"); err != nil {
				t.Fatal(err)
			}
			if err = s.CheckSource(ctx, source, "gen", 10); err != nil {
				t.Fatal(err)
			}
			if err = s.CheckSource(ctx, source, test.generation, test.highWater); !errors.Is(err, ErrUncertain) {
				t.Fatal(err)
			}
			if _, _, err = s.ClaimNext(ctx, source, time.Now()); !errors.Is(err, ErrUncertain) {
				t.Fatal(err)
			}
			if err = s.DiscardRepliesAndResume(ctx, source, ""); !errors.Is(err, ErrUncertain) {
				t.Fatalf("source change was resumed without a baseline: %v", err)
			}
			if err = s.ResetSource(ctx, source, "", test.highWater, test.generation); err != nil {
				t.Fatal(err)
			}
			if err = s.CheckSource(ctx, source, test.generation, test.highWater); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestUnknownReplayPausesWithoutMovingCursor(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	source := Source{Name: "upstream", Sender: "sender", ChatGUID: "chat", ChatID: 1}
	if err = s.Initialize(ctx, source, 10, ""); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Accept(ctx, source, Event{ID: 9, GUID: "unseen-old", CreatedAt: time.Now()}, "turn"); !errors.Is(err, ErrUncertain) {
		t.Fatal(err)
	}
	status, err := s.Status(ctx, source)
	if err != nil || status.Cursor != 10 || !status.Paused || status.Queued != 0 {
		t.Fatalf("%+v %v", status, err)
	}
}

func TestQueuedTurnExpiresBeforeExecution(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	source := Source{Name: "upstream", Sender: "sender", ChatGUID: "chat", ChatID: 1}
	if err = s.Initialize(ctx, source, 0, ""); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if _, err = s.Accept(ctx, source, Event{ID: 1, GUID: "old", Text: "old", CreatedAt: now.Add(-16 * time.Minute)}, "turn"); err != nil {
		t.Fatal(err)
	}
	job, reply, err := s.ClaimNext(ctx, source, now)
	if err != nil || job != nil || reply != nil {
		t.Fatalf("%+v %+v %v", job, reply, err)
	}
	var state string
	if err = s.db.QueryRow(`SELECT state FROM jobs`).Scan(&state); err != nil || state != "failed" {
		t.Fatalf("%s %v", state, err)
	}
}

func TestCanceledIntakeDoesNotAdvanceCursor(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	source := Source{Name: "upstream", Sender: "sender", ChatGUID: "chat", ChatID: 1}
	if err = s.Initialize(ctx, source, 0, ""); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = s.Accept(canceled, source, Event{ID: 1, GUID: "event"}, "turn"); err == nil {
		t.Fatal("expected cancellation")
	}
	status, err := s.Status(ctx, source)
	if err != nil || status.Cursor != 0 || status.Queued != 0 {
		t.Fatalf("%+v %v", status, err)
	}
}

func TestSubmittedProgressRepliesPrecedeFinalChunks(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	source := Source{Name: "upstream", Sender: "sender", ChatGUID: "chat", ChatID: 1}
	if err = s.Initialize(ctx, source, 0, ""); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Accept(ctx, source, Event{ID: 1, GUID: "job", Text: "work", Sender: "sender", CreatedAt: time.Now()}, "turn"); err != nil {
		t.Fatal(err)
	}
	job, _, err := s.ClaimNext(ctx, source, time.Now())
	if err != nil || job == nil {
		t.Fatalf("%+v %v", job, err)
	}
	if err = s.RecordSubmittedReply(ctx, source, job.ID, "Filed the issue"); err != nil {
		t.Fatal(err)
	}
	if err = s.CompleteTurn(ctx, source, job.ID, "session", []string{"PR is up."}); err != nil {
		t.Fatal(err)
	}
	rows, err := s.db.Query(`SELECT ordinal,text,state FROM replies WHERE job_id=? ORDER BY ordinal`, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var ordinal int
		var text, state string
		if err := rows.Scan(&ordinal, &text, &state); err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("%d:%s:%s", ordinal, state, text))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "0:submitted:Filed the issue" || got[1] != "1:pending:PR is up." {
		t.Fatalf("replies=%q", got)
	}
}
