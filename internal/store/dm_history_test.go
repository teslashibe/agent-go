package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func privateHistoryStore(t *testing.T) (*Store, Source) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	source := Source{Name: "messages", Sender: "alex", ChatID: 1, ChatGUID: "dm-alex"}
	if err := s.CheckHistory(context.Background(), source, []Anchor{{ID: 10, GUID: "archive-10"}}); err != nil {
		t.Fatal(err)
	}
	return s, source
}

func TestBootstrapDMHistoryPreservesSessionAndQueuedWork(t *testing.T) {
	ctx := context.Background()
	s, source := privateHistoryStore(t)
	if _, err := s.db.Exec(`UPDATE sources SET session='existing-session' WHERE source=?`, source.key()); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	receipt, err := s.Accept(ctx, source, Event{ID: 11, GUID: "live-11", Text: "live question", Sender: source.Sender, CreatedAt: now}, "turn")
	if err != nil || receipt.Disposition != "turn" {
		t.Fatalf("enqueue: %+v %v", receipt, err)
	}
	history := []HistoryMessage{archiveMessage(source, 10, source.Sender, "private archive", false)}
	if n, err := s.BootstrapDMHistory(ctx, source, history); err != nil || n != 1 {
		t.Fatalf("bootstrap: %d %v", n, err)
	}
	status, err := s.Status(ctx, source)
	if err != nil || status.SessionID != "existing-session" || status.Cursor != 11 || status.Queued != 1 {
		t.Fatalf("status: %+v %v", status, err)
	}
	// A reconnect must not archive new live events or reset the continuing session.
	history = append(history, archiveMessage(source, 11, source.Sender, "live question", false))
	if n, err := s.BootstrapDMHistory(ctx, source, history); err != nil || n != 0 {
		t.Fatalf("reconnect: %d %v", n, err)
	}
	var archiveCount, jobCount int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM history_messages WHERE source=?`, source.key()).Scan(&archiveCount); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE source=?`, source.key()).Scan(&jobCount); err != nil {
		t.Fatal(err)
	}
	if archiveCount != 1 || jobCount != 1 {
		t.Fatalf("archive=%d jobs=%d", archiveCount, jobCount)
	}
}

func TestBootstrapDMHistoryPendingArchiveDelivery(t *testing.T) {
	for _, outcome := range []string{"success", "completion-rollback", "unknown"} {
		t.Run(outcome, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "state.sqlite")
			s, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { s.Close() })
			source := Source{Name: "messages", Sender: "alex", ChatID: 1, ChatGUID: "dm-alex"}
			if err := s.CheckHistory(ctx, source, []Anchor{{ID: 10, GUID: "archive-10"}}); err != nil {
				t.Fatal(err)
			}
			if complete, err := s.DMHistoryBootstrapped(ctx, source); err != nil || complete {
				t.Fatalf("before bootstrap: %v %v", complete, err)
			}
			if _, err := s.db.Exec(`UPDATE sources SET session='existing-session' WHERE source=?`, source.key()); err != nil {
				t.Fatal(err)
			}
			history := []HistoryMessage{archiveMessage(source, 10, source.Sender, "pending archive fact", false)}
			if _, err := s.BootstrapDMHistory(ctx, source, history); err != nil {
				t.Fatal(err)
			}
			// Neither the bootstrap completion nor the pending delivery is transient.
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = Open(path)
			if err != nil {
				t.Fatal(err)
			}
			if complete, err := s.DMHistoryBootstrapped(ctx, source); err != nil || !complete {
				t.Fatalf("reopened bootstrap: %v %v", complete, err)
			}
			now := time.Now()
			claim := func(id int64) *Job {
				t.Helper()
				if _, err := s.Accept(ctx, source, Event{ID: id, GUID: fmt.Sprintf("live-%d", id), Text: "next question", Sender: source.Sender, CreatedAt: now}, "turn"); err != nil {
					t.Fatal(err)
				}
				job, _, err := s.ClaimNext(ctx, source, now)
				if err != nil || job == nil || job.SessionID != "existing-session" {
					t.Fatalf("session lost: %+v %v", job, err)
				}
				if err := s.FinishAcknowledgement(ctx, source, job.ID, "accepted"); err != nil {
					t.Fatal(err)
				}
				return job
			}
			checkContext := func(job *Job, wantArchive bool) {
				t.Helper()
				prompt, err := s.RequestContext(ctx, source, job, now)
				if err != nil || strings.Contains(prompt, "pending archive fact") != wantArchive {
					t.Fatalf("archive wanted=%v: %q %v", wantArchive, prompt, err)
				}
				if strings.Contains(prompt, "Completed private-chat turns") {
					t.Fatal("continuing session received completed turns again")
				}
			}
			job := claim(11)
			checkContext(job, true)
			checkContext(job, true) // Rendering context must not acknowledge delivery.
			switch outcome {
			case "completion-rollback":
				// Fail after the session and job update; the entire completion must roll back.
				if _, err := s.db.Exec(`CREATE TRIGGER fail_completion BEFORE DELETE ON dm_history_pending_context BEGIN SELECT RAISE(ABORT,'test completion failure'); END`); err != nil {
					t.Fatal(err)
				}
				if err := s.CompleteTurn(ctx, source, job.ID, "must-rollback", []string{"reply"}); err == nil {
					t.Fatal("completion unexpectedly succeeded")
				}
				status, err := s.Status(ctx, source)
				if err != nil || status.SessionID != "existing-session" || status.Running != 1 {
					t.Fatalf("completion did not roll back: %+v %v", status, err)
				}
				checkContext(job, true)
				if _, err := s.db.Exec(`DROP TRIGGER fail_completion`); err != nil {
					t.Fatal(err)
				}
			case "unknown":
				if err := s.MarkTurnUnknown(ctx, source, job.ID, errors.New("uncertain model response")); err != nil {
					t.Fatal(err)
				}
				if err := s.CompleteTurn(ctx, source, job.ID, "must-not-save", nil); err == nil {
					t.Fatal("completed an uncertain turn")
				}
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				s, err = Open(path)
				if err != nil {
					t.Fatal(err)
				}
				status, err := s.Status(ctx, source)
				if err != nil || !status.Paused || status.SessionID != "existing-session" {
					t.Fatalf("uncertain reopen: %+v %v", status, err)
				}
				if err := s.DiscardRepliesAndResume(ctx, source, "existing-session"); err != nil {
					t.Fatal(err)
				}
				job = claim(12)
				checkContext(job, true)
			}
			// Empty returned session preserves the existing resumable session.
			if err := s.CompleteTurn(ctx, source, job.ID, "", nil); err != nil {
				t.Fatal(err)
			}
			if n, err := s.BootstrapDMHistory(ctx, source, history); err != nil || n != 0 {
				t.Fatalf("repeat bootstrap: %d %v", n, err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = Open(path)
			if err != nil {
				t.Fatal(err)
			}
			job = claim(13)
			checkContext(job, false)
			var resets, pending int
			if err := s.db.QueryRow(`SELECT COUNT(*) FROM inbox WHERE source=? AND disposition='new'`, source.key()).Scan(&resets); err != nil {
				t.Fatal(err)
			}
			if err := s.db.QueryRow(`SELECT COUNT(*) FROM dm_history_pending_context WHERE source=?`, source.key()).Scan(&pending); err != nil {
				t.Fatal(err)
			}
			if resets != 0 || pending != 0 {
				t.Fatalf("unexpected resets=%d pending=%d", resets, pending)
			}
		})
	}
}

func TestBootstrapDMHistoryCompletionSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	source := Source{Name: "messages", Sender: "alex", ChatID: 1, ChatGUID: "dm-alex"}
	if err := s.CheckHistory(ctx, source, []Anchor{{ID: 10, GUID: "archive-10"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BootstrapDMHistory(ctx, source, []HistoryMessage{archiveMessage(source, 10, source.Sender, "original archive", false)}); err != nil {
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
	if n, err := s.BootstrapDMHistory(ctx, source, []HistoryMessage{archiveMessage(source, 11, source.Sender, "must not import reconnect", false)}); err != nil || n != 0 {
		t.Fatalf("reopened bootstrap: %d %v", n, err)
	}
	var text string
	if err := s.db.QueryRow(`SELECT text FROM history_messages WHERE source=?`, source.key()).Scan(&text); err != nil || text != "original archive" {
		t.Fatalf("archive changed: %q %v", text, err)
	}
}

func TestBootstrapDMHistoryPreservesPendingReplies(t *testing.T) {
	ctx := context.Background()
	s, source := privateHistoryStore(t)
	now := time.Now()
	if _, err := s.Accept(ctx, source, Event{ID: 11, GUID: "queued", Text: "question", Sender: source.Sender, CreatedAt: now}, "turn"); err != nil {
		t.Fatal(err)
	}
	job, _, err := s.ClaimNext(ctx, source, now)
	if err != nil || job == nil {
		t.Fatalf("claim: %+v %v", job, err)
	}
	if err := s.CompleteTurn(ctx, source, job.ID, "existing-session", []string{"pending response"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BootstrapDMHistory(ctx, source, []HistoryMessage{archiveMessage(source, 10, source.Sender, "private archive", false)}); err != nil {
		t.Fatal(err)
	}
	_, reply, err := s.ClaimNext(ctx, source, now)
	if err != nil || reply == nil || reply.Text != "pending response" {
		t.Fatalf("pending reply lost: %+v %v", reply, err)
	}
}

func TestBootstrapDMHistoryRollbackRetryAndEmptyCompletion(t *testing.T) {
	ctx := context.Background()
	s, source := privateHistoryStore(t)
	good := archiveMessage(source, 9, source.Sender, "complete older history", false)
	bad := archiveMessage(source, 10, source.Sender, strings.Repeat("x", MaxHistoryBytes), false)
	if _, err := s.BootstrapDMHistory(ctx, source, []HistoryMessage{good, bad}); err == nil {
		t.Fatal("accepted oversized history")
	}
	var count int
	for _, table := range []string{"history_messages", "dm_history_bootstraps", "dm_history_pending_context"} {
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("partial %s: %d %v", table, count, err)
		}
	}
	// CheckHistory already initialized successfully; import must still retry.
	if err := s.CheckHistory(ctx, source, []Anchor{{ID: 10, GUID: "archive-10"}}); err != nil {
		t.Fatal(err)
	}
	if n, err := s.BootstrapDMHistory(ctx, source, []HistoryMessage{good}); err != nil || n != 1 {
		t.Fatalf("retry: %d %v", n, err)
	}
	other := Source{Name: source.Name, Sender: "jordan", ChatID: 2, ChatGUID: "dm-jordan"}
	if err := s.Initialize(ctx, other, 0, ""); err != nil {
		t.Fatal(err)
	}
	if n, err := s.BootstrapDMHistory(ctx, other, nil); err != nil || n != 0 {
		t.Fatalf("empty bootstrap: %d %v", n, err)
	}
	if n, err := s.BootstrapDMHistory(ctx, other, []HistoryMessage{archiveMessage(other, 1, other.Sender, "later", false)}); err != nil || n != 0 {
		t.Fatalf("empty completion not durable: %d %v", n, err)
	}
}

func TestBootstrapDMHistoryRejectsIncompleteAndForeignSnapshots(t *testing.T) {
	for _, kind := range []string{"cap", "duplicate", "foreign-sender", "foreign-chat", "group", "busy"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			s, source := privateHistoryStore(t)
			history := []HistoryMessage{archiveMessage(source, 10, source.Sender, "private", false)}
			switch kind {
			case "cap":
				history = make([]HistoryMessage, MaxHistoryMessages)
			case "duplicate":
				history = append(history, history[0])
			case "foreign-sender":
				history[0].Sender = "jordan"
			case "foreign-chat":
				history[0].ChatGUID = "dm-jordan"
			case "group":
				history[0].IsGroup = true
			case "busy":
				if _, err := s.db.Exec(`INSERT INTO jobs(source,guid,prompt,state,created_at) VALUES(?,'live','live','running',?)`, source.key(), time.Now().UnixNano()); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.BootstrapDMHistory(ctx, source, history); err == nil {
				t.Fatal("accepted unsafe history")
			}
			var count int
			if err := s.db.QueryRow(`SELECT COUNT(*) FROM dm_history_bootstraps`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("marked complete: %d %v", count, err)
			}
		})
	}
}

func TestPrivateContextIncludesFullOwnArchiveAndAllCompletedTurns(t *testing.T) {
	ctx := context.Background()
	s, source := privateHistoryStore(t)
	archive := "very old private fact " + strings.Repeat("a", 5000) + " archive-end"
	if _, err := s.BootstrapDMHistory(ctx, source, []HistoryMessage{archiveMessage(source, 10, source.Sender, archive, false)}); err != nil {
		t.Fatal(err)
	}
	other := Source{Name: source.Name, Sender: "jordan", ChatID: 2, ChatGUID: "dm-jordan"}
	group := Source{Name: source.Name, AllowedSenders: []string{"alex", "jordan"}, ChatID: 3, ChatGUID: "family", Group: true}
	for _, foreign := range []Source{other, group} {
		if err := s.Initialize(ctx, foreign, 0, ""); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ImportHistory(ctx, foreign, []HistoryMessage{archiveMessage(foreign, 1, "jordan", "FOREIGN-SECRET", false)}); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	for id := int64(11); id <= 35; id++ {
		text := fmt.Sprintf("completed-%d-", id) + strings.Repeat("b", 4100) + "-end"
		guid := fmt.Sprintf("live-%d", id)
		if _, err := s.Accept(ctx, source, Event{ID: id, GUID: guid, Text: text, Sender: source.Sender, CreatedAt: now}, "turn"); err != nil {
			t.Fatal(err)
		}
		job, _, err := s.ClaimNext(ctx, source, now)
		if err != nil || job == nil {
			t.Fatalf("claim: %+v %v", job, err)
		}
		if err := s.CompleteTurn(ctx, source, job.ID, "", []string{"submitted reply"}); err != nil {
			t.Fatal(err)
		}
		_, reply, err := s.ClaimNext(ctx, source, now)
		if err != nil || reply == nil {
			t.Fatalf("reply: %+v %v", reply, err)
		}
		if err := s.MarkReplySubmitted(ctx, source, reply.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Accept(ctx, source, Event{ID: 36, GUID: "current", Text: "current question", Sender: source.Sender, CreatedAt: now}, "turn"); err != nil {
		t.Fatal(err)
	}
	job, _, err := s.ClaimNext(ctx, source, now)
	if err != nil || job == nil {
		t.Fatalf("claim: %+v %v", job, err)
	}
	prompt, err := s.RequestContext(ctx, source, job, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{archive, "completed-11-", "completed-35-", "submitted reply", "current question", `"actions_enabled":false`, source.ChatGUID} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("missing %q", expected[:min(len(expected), 60)])
		}
	}
	for _, forbidden := range []string{"FOREIGN-SECRET", "SHARED", "shared chat", "default_time_zone", "America/Los_Angeles", "pending_reminders"} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("private context contains %q", forbidden)
		}
	}
	job.SessionID = "continuing-private-session"
	prompt, err = s.RequestContext(ctx, source, job, now)
	if err != nil || strings.Contains(prompt, "archive-end") || strings.Contains(prompt, "completed-11-") || !strings.Contains(prompt, "current question") {
		t.Fatalf("continuation context: %q %v", prompt, err)
	}
	job.SessionID = ""
	job.Prompt = strings.Repeat("x", MaxRequestBytes)
	if _, err := s.RequestContext(ctx, source, job, now); err == nil {
		t.Fatal("silently truncated oversized private context")
	}
}

func TestPrivateNewClearsPendingArchive(t *testing.T) {
	ctx := context.Background()
	s, source := privateHistoryStore(t)
	if _, err := s.db.Exec(`UPDATE sources SET session='existing-session' WHERE source=?`, source.key()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BootstrapDMHistory(ctx, source, []HistoryMessage{archiveMessage(source, 10, source.Sender, "pre-reset-secret", false)}); err != nil {
		t.Fatal(err)
	}
	var pending int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM dm_history_pending_context WHERE source=?`, source.key()).Scan(&pending); err != nil || pending != 1 {
		t.Fatalf("archive not pending: %d %v", pending, err)
	}
	now := time.Now()
	if receipt, err := s.Accept(ctx, source, Event{ID: 11, GUID: "reset", Text: "/new", Sender: source.Sender, CreatedAt: now}, "new"); err != nil || receipt.Disposition != "new" {
		t.Fatalf("reset: %+v %v", receipt, err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM dm_history_pending_context WHERE source=?`, source.key()).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("reset left pending archive: %d %v", pending, err)
	}
	if _, err := s.Accept(ctx, source, Event{ID: 12, GUID: "fresh", Text: "new request", Sender: source.Sender, CreatedAt: now}, "turn"); err != nil {
		t.Fatal(err)
	}
	job, _, err := s.ClaimNext(ctx, source, now)
	if err != nil || job == nil || job.SessionID != "" {
		t.Fatalf("reset claim: %+v %v", job, err)
	}
	prompt, err := s.RequestContext(ctx, source, job, now)
	if err != nil || strings.Contains(prompt, "pre-reset-secret") {
		t.Fatalf("reset leaked archive: %q %v", prompt, err)
	}
}

func TestPrivateBootstrapDoesNotResurrectHistoryAfterNew(t *testing.T) {
	ctx := context.Background()
	s, source := privateHistoryStore(t)
	now := time.Now()
	if _, err := s.Accept(ctx, source, Event{ID: 11, GUID: "reset", Text: "/new", Sender: source.Sender, CreatedAt: now}, "new"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BootstrapDMHistory(ctx, source, []HistoryMessage{archiveMessage(source, 10, source.Sender, "pre-reset-secret", false)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Accept(ctx, source, Event{ID: 12, GUID: "fresh", Text: "new request", Sender: source.Sender, CreatedAt: now}, "turn"); err != nil {
		t.Fatal(err)
	}
	job, _, err := s.ClaimNext(ctx, source, now)
	if err != nil || job == nil {
		t.Fatalf("claim: %+v %v", job, err)
	}
	prompt, err := s.RequestContext(ctx, source, job, now)
	if err != nil || strings.Contains(prompt, "pre-reset-secret") {
		t.Fatalf("reset leaked archive: %q %v", prompt, err)
	}
	wrong := Source{Name: source.Name, Sender: "jordan", ChatID: 2, ChatGUID: "dm-jordan"}
	if _, err := s.RequestContext(ctx, wrong, job, now); err == nil || errors.Is(err, ErrBusy) {
		t.Fatalf("cross-source context accepted: %v", err)
	}
}
