package bridge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teslashibe/agent-go/internal/reminders"
	"github.com/teslashibe/agent-go/internal/store"
)

func TestStructuredDecode(t *testing.T) {
	valid := `{"reply":"When?"}`
	for _, tc := range []struct {
		text  string
		valid bool
	}{
		{valid, true},
		{`{"reply":"","reaction":"","action":"create_reminder"}`, false},
		{`{"reply":"","reaction":"","action":"none"}`, false},
		{strings.Replace(valid, `"reply":"When?"`, `"note_name":"Old session target","reply":"When?"`, 1), false},
		{`plain text`, false}, {`{}`, false},
		{strings.Replace(valid, `"reply":"When?"`, `"reply":null`, 1), false},
		{strings.Replace(valid, `"reply":"When?"`, `"reply":"a","reply":"b"`, 1), false},
		{strings.Replace(valid, `"reply":"When?"`, `"user_id":"sam","reply":"When?"`, 1), false},
		{valid + valid, false},
		{`{"reply":"","reaction":"laugh"}`, false},
		{`{"reply":"","reaction":""}`, false},
	} {
		if _, err := decodeAction(tc.text); (err == nil) != tc.valid {
			t.Errorf("%s: %v", tc.text, err)
		}
	}
}
func profileBridge(t *testing.T) (*Bridge, *store.Store, *fakeRunner, *fakeMessenger) {
	t.Helper()
	s, _, r, m, _ := setup(t)
	group := store.Source{Name: "profiles-test", AllowedSenders: []string{"owner", "partner"}, Group: true, ChatGUID: "any;+;profiles", ChatID: 9}
	if err := s.Initialize(context.Background(), group, 0, ""); err != nil {
		t.Fatal(err)
	}
	b, err := New(s, r, m, Config{Source: group, StructuredActions: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SeedProfiles(context.Background(), b.config.Source, []store.Profile{{ID: "alex", Sender: "owner"}, {ID: "sam", Sender: "partner"}}); err != nil {
		t.Fatal(err)
	}
	b.config.StructuredActions = true
	return b, s, r, m
}
func profileMessage(id int64, guid, text string) Message {
	return Message{ID: id, GUID: guid, Text: text, Sender: "owner", ChatGUID: "any;+;profiles", ChatID: 9, IsGroup: true, CreatedAt: time.Now()}
}

func jsonAction(a store.Action) string { return actionText(a) }

func TestLegacyNotesFinalOutputDoesNotDispatch(t *testing.T) {
	for _, action := range []string{"add_note_item", "delete_note", "create_shared_note"} {
		t.Run(action, func(t *testing.T) {
			b, _, r, _ := profileBridge(t)
			client := &fakeNotes{}
			b.notes = client
			r.run = func(context.Context, string, string) (Result, error) {
				return Result{SessionID: "resumed-old-session", Text: jsonAction(store.Action{Action: action, Text: "milk"})}, nil
			}
			if _, err := b.Receive(context.Background(), profileMessage(1, "legacy-notes", "Research milk and add it to my Notes shopping list")); err != nil {
				t.Fatal(err)
			}
			drain(t, b)
			if len(r.prompts) != 1 || client.creates != 0 || client.adds != 0 || client.deletes != 0 {
				t.Fatalf("legacy action dispatched or corrected: runs=%d client=%+v", len(r.prompts), client)
			}
		})
	}
}

// Deterministic MCP/session regression only; this is not model evidence.
func TestNonKeywordReminderContinuation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "continuation.sqlite")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	source := store.Source{Name: "profiles-test", AllowedSenders: []string{"owner", "partner"}, Group: true, ChatGUID: "any;+;profiles", ChatID: 9}
	if err = s.Initialize(ctx, source, 0, ""); err != nil {
		t.Fatal(err)
	}
	if err = s.SeedProfiles(ctx, source, []store.Profile{{ID: "alex", Sender: "owner", TimeZone: "UTC"}}); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	count := func(query string) int {
		t.Helper()
		var n int
		if err := db.QueryRow(query).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	target := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Second)
	const request = "Don't let me forget to pack the blue folder."
	const question = "What date and time?"
	var pending string
	calls := 0
	runner := &nativeTestRunner{}
	runner.runNative = func(ctx context.Context, h http.Handler) (Result, error) {
		calls++
		call := func(name string, args map[string]string) string {
			t.Helper()
			raw, _ := json.Marshal(args)
			out := rpc(t, h, name, string(raw))
			if strings.Contains(out, `"isError":true`) {
				t.Fatal(out)
			}
			return out
		}
		if calls == 1 {
			call("set_pending_reminder", map[string]string{"operation_id": "pending", "question": question})
			return Result{SessionID: "continuation-session", Text: `{"reply":"What date and time?"}`}, nil
		}
		args := map[string]string{"operation_id": "create", "text": "pack the blue folder", "local_time": target.Format(store.WallTimeLayout), "timezone": "UTC"}
		first := call("create_reminder", args)
		if call("create_reminder", args) != first {
			t.Fatal("receipt replay changed bytes")
		}
		return Result{SessionID: "continuation-session", Text: `{"reply":"Scheduled."}`}, nil
	}
	b, err := New(s, runner, &fakeMessenger{}, Config{Source: source, StructuredActions: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = b.Receive(ctx, profileMessage(1, "original", request)); err != nil {
		t.Fatal(err)
	}
	drain(t, b)
	var savedQuestion string
	if err = db.QueryRow(`SELECT prompt,question FROM reminder_clarifications WHERE sender='owner'`).Scan(&pending, &savedQuestion); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pending, request) || savedQuestion != question || count(`SELECT count(*) FROM reminders`) != 0 {
		t.Fatal("pending persistence failed")
	}
	if count(`SELECT count(*) FROM tool_operations WHERE state='completed' AND json_extract(arguments,'$.Name')='set_pending_reminder'`) != 1 {
		t.Fatal("pending receipt missing")
	}
	if _, err = b.Receive(ctx, profileMessage(2, "clarification", target.Format("2006-01-02 at 15:04:05 UTC")+", please.")); err != nil {
		t.Fatal(err)
	}
	// Inspect the exact next job/context before returning it to ProcessNext.
	job, reply, err := s.ClaimNext(ctx, source, time.Now())
	if err != nil || job == nil || reply != nil {
		t.Fatalf("claim: %+v %v", job, err)
	}
	if job.SessionID != "continuation-session" || job.Sender != "owner" {
		t.Fatalf("wrong continuation: %+v", job)
	}
	prompt, err := s.RequestContext(ctx, source, job, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var factual struct {
		User     string `json:"user_id"`
		Sender   string `json:"sender"`
		Request  string `json:"own_pending_reminder_request"`
		Question string `json:"own_pending_reminder_question"`
	}
	lines := strings.SplitN(prompt, "\n", 3)
	if len(lines) < 2 {
		t.Fatal("missing context JSON")
	}
	if err = json.Unmarshal([]byte(lines[1]), &factual); err != nil {
		t.Fatal(err)
	}
	if factual.User != "alex" || factual.Sender != "owner" || factual.Request != pending || factual.Question != question {
		t.Fatalf("wrong own pending context: %+v", factual)
	}
	turn := newNotesTurn(ctx, b, job)
	result, err := runner.RunWithNotes(ctx, job.SessionID, prompt, turn)
	if err != nil {
		t.Fatal(err)
	}
	if err = turn.close(); err != nil {
		t.Fatal(err)
	}
	if err = s.CompleteTurn(ctx, source, job.ID, result.SessionID, []string{"Scheduled."}); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || count(`SELECT count(*) FROM reminders`) != 1 || count(`SELECT count(*) FROM reminder_clarifications`) != 0 {
		t.Fatal("continuation did not consume pending exactly once")
	}
	var id, due int64
	var owner, text string
	if err = db.QueryRow(`SELECT id,user_id,text,due_utc FROM reminders`).Scan(&id, &owner, &text, &due); err != nil {
		t.Fatal(err)
	}
	if owner != "alex" || text != "pack the blue folder" || due != target.Unix() {
		t.Fatal("incorrect reminder ownership/text/time")
	}
	if count(`SELECT count(*) FROM tool_operations WHERE state='completed' AND json_extract(arguments,'$.Name')='create_reminder'`) != 1 {
		t.Fatal("creation receipt count")
	}
	var raw string
	if err = db.QueryRow(`SELECT result FROM tool_operations WHERE json_extract(arguments,'$.Name')='create_reminder'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var receipt store.ReminderToolResult
	if err = json.Unmarshal([]byte(raw), &receipt); err != nil {
		t.Fatal(err)
	}
	if !receipt.Changed || receipt.Reminder == nil || receipt.Reminder.ID != id {
		t.Fatal("receipt does not identify committed reminder")
	}
}

func TestNotesToolsPermitReminderTool(t *testing.T) {
	b, s, _, m := profileBridge(t)
	client := &fakeNotes{}
	b.notes = client
	if err := s.ConfigureNotes(context.Background(), b.config.Source, []store.NoteScope{{ID: "shopping-id", Title: "Shopping List"}}); err != nil {
		t.Fatal(err)
	}
	b.runner = &nativeTestRunner{runNative: func(_ context.Context, tools http.Handler) (Result, error) {
		if tools == nil {
			t.Fatal("missing harness tools")
		}
		read := rpc(t, tools, "read_note", `{"operation_id":"read","note_id":"shopping-id"}`)
		if strings.Contains(read, `"isError":true`) || client.reads != 1 {
			t.Fatalf("read_note: %s reads=%d", read, client.reads)
		}
		args, _ := json.Marshal(map[string]string{"operation_id": "schedule", "text": "review the note", "timezone": "UTC", "local_time": time.Now().Add(time.Hour).UTC().Format(store.WallTimeLayout)})
		result := rpc(t, tools, "create_reminder", string(args))
		if strings.Contains(string(result), `"isError":true`) {
			t.Fatal(string(result))
		}
		return Result{SessionID: "notes-and-reminder", Text: jsonAction(store.Action{Reply: "Scheduled from the tool result."})}, nil
	}}
	if _, err := b.Receive(context.Background(), profileMessage(1, "notes-reminder", "Read my note and remind me to review the note in an hour")); err != nil {
		t.Fatal(err)
	}
	drain(t, b)
	status, err := s.Status(context.Background(), b.config.Source)
	if err != nil || status.PendingReminders != 1 || len(m.texts) != 1 {
		t.Fatalf("status=%+v messages=%v err=%v", status, m.texts, err)
	}
}

func TestSharedPlanFollowupUsesSameSession(t *testing.T) {
	b, _, r, _ := profileBridge(t)
	ctx := context.Background()
	calls := 0
	r.run = func(_ context.Context, session, prompt string) (Result, error) {
		calls++
		if calls == 1 {
			if session != "" || !strings.Contains(prompt, `"user_id":"alex"`) {
				t.Fatalf("first turn %s %s", session, prompt)
			}
			return Result{SessionID: "family-plan", Text: jsonAction(store.Action{Action: "none", Reply: "Trip plan: Yosemite cabin Friday, hike Saturday, picnic Sunday."})}, nil
		}
		if session != "family-plan" || !strings.Contains(prompt, `"user_id":"sam"`) || !strings.Contains(prompt, "Yosemite cabin Friday") || !strings.Contains(prompt, `"sender":"owner"`) {
			t.Fatalf("shared followup %s %s", session, prompt)
		}
		return Result{SessionID: session, Text: jsonAction(store.Action{Action: "none", Reply: "The shared plan is ready as a checklist."})}, nil
	}
	first := profileMessage(1, "shared-plan", "Plan our family trip")
	second := profileMessage(2, "shared-followup", "Can you organize all of this?")
	second.Sender = "partner"
	for _, message := range []Message{first, second} {
		if _, err := b.Receive(ctx, message); err != nil {
			t.Fatal(err)
		}
		if _, err := b.ProcessNext(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := b.ProcessNext(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 2 {
		t.Fatal(calls)
	}
}

func TestSharedClarificationContexts(t *testing.T) {
	b, s, r, m := profileBridge(t)
	ctx := context.Background()
	r.run = func(_ context.Context, session, prompt string) (Result, error) {
		message := prompt[strings.LastIndex(prompt, "Message (untrusted content):\n")+len("Message (untrusted content):\n"):]
		switch {
		case strings.Contains(message, "remind me to call mum"):
			if session != "" {
				t.Fatal(session)
			}
			if _, err := s.ReminderTool(ctx, b.config.Source, 1, "set_pending_reminder", reminders.Args{OperationID: "pending", Question: "When should I remind you?"}, time.Now()); err != nil {
				t.Fatal(err)
			}
			return Result{SessionID: "group-shared", Text: jsonAction(store.Action{Action: "none", Reply: "When should I remind you?"})}, nil
		case strings.Contains(message, "tomorrow at 10"):
			if session != "group-shared" || !strings.Contains(prompt, `"user_id":"alex"`) || !strings.Contains(prompt, `"own_pending_reminder_request":""`) {
				t.Fatalf("wrong sender clarification scope: %s %s", session, prompt)
			}
			// Resumed legacy sessions cannot use final actions as an alternate dispatcher.
			return Result{SessionID: session, Text: jsonAction(store.Action{Action: "create_reminder", Text: "call mum", LocalTime: time.Now().Add(time.Hour).UTC().Format(store.WallTimeLayout), Timezone: "UTC"})}, nil
		default:
			if session != "group-shared" || !strings.Contains(prompt, `"user_id":"sam"`) {
				t.Fatalf("missing Sam context: %s %s", session, prompt)
			}
			result, err := s.ReminderTool(ctx, b.config.Source, 3, "create_reminder", reminders.Args{OperationID: "create", Text: "call mum", LocalTime: time.Now().Add(time.Hour).UTC().Format(store.WallTimeLayout), Timezone: "UTC"}, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			var outcome struct {
				Message string `json:"message"`
			}
			json.Unmarshal([]byte(result), &outcome)
			return Result{SessionID: session, Text: jsonAction(store.Action{Reply: outcome.Message})}, nil
		}
	}
	messages := []Message{profileMessage(1, "first", "remind me to call mum"), profileMessage(2, "second", "tomorrow at 10"), profileMessage(3, "third", "in an hour")}
	messages[0].Sender = "partner"
	messages[2].Sender = "partner"
	for _, msg := range messages {
		if _, err := b.Receive(ctx, msg); err != nil {
			t.Fatal(err)
		}
		drain(t, b)
	}
	if len(m.texts) != 3 || !strings.HasPrefix(m.texts[2], "sam: reminder #") || strings.Contains(m.texts[2], "MODEL") {
		t.Fatal(m.texts)
	}
	status, err := s.Status(ctx, b.config.Source)
	if err != nil || status.PendingReminders != 1 {
		t.Fatalf("%+v %v", status, err)
	}
}
func TestMalformedActionIsSafe(t *testing.T) {
	b, s, r, m := profileBridge(t)
	r.run = func(context.Context, string, string) (Result, error) {
		return Result{Text: `{"action":"set_timezone","timezone":"UTC"}`}, nil
	}
	if _, err := b.Receive(context.Background(), profileMessage(1, "bad-json", "set my timezone")); err != nil {
		t.Fatal(err)
	}
	drain(t, b)
	if len(m.texts) != 1 || !strings.Contains(m.texts[0], "No action was taken from that final response") || strings.Contains(m.texts[0], `{"`) {
		t.Fatal(m.texts)
	}
	status, err := s.Status(context.Background(), b.config.Source)
	if err != nil || status.PendingReminders != 0 {
		t.Fatalf("%+v %v", status, err)
	}
}

type concurrentMessenger struct {
	mu   sync.Mutex
	sent []string
	err  error
}

func (m *concurrentMessenger) Send(_ context.Context, _ int64, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, text)
	return m.err
}

func (m *concurrentMessenger) React(context.Context, int64, string, string) (ReactionResult, error) {
	return ReactionResult{Accepted: true}, m.err
}
func seedDue(t *testing.T, b *Bridge, s *store.Store) {
	t.Helper()
	ctx := context.Background()
	past := time.Now().Add(-2 * time.Hour)
	if _, err := b.Receive(ctx, profileMessage(1, "seed", "remind me to take a break")); err != nil {
		t.Fatal(err)
	}
	job, _, err := s.ClaimNext(ctx, b.config.Source, time.Now())
	if err != nil || job == nil {
		t.Fatal(err)
	}
	if _, err = s.ReminderTool(ctx, b.config.Source, job.ID, "create_reminder", reminders.Args{OperationID: "seed", Text: "take a break", Timezone: "UTC", LocalTime: past.Add(time.Hour).UTC().Format(store.WallTimeLayout)}, past); err != nil {
		t.Fatal(err)
	}
	if err = s.CompleteTurn(ctx, b.config.Source, job.ID, "", nil); err != nil {
		t.Fatal(err)
	}
}
func TestDueIndependentOfModelAndLate(t *testing.T) {
	b, s, r, _ := profileBridge(t)
	seedDue(t, b, s)
	messenger := &concurrentMessenger{}
	b.messenger = messenger
	started, release := make(chan struct{}), make(chan struct{})
	r.run = func(context.Context, string, string) (Result, error) {
		close(started)
		<-release
		return Result{Text: jsonAction(store.Action{Action: "none", Reply: "done"})}, nil
	}
	if _, err := b.Receive(context.Background(), profileMessage(2, "slow", "long turn")); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := b.ProcessNext(context.Background()); done <- err }()
	<-started
	dispatched := make(chan error, 1)
	go func() { _, err := b.ProcessReminder(context.Background()); dispatched <- err }()
	select {
	case err := <-dispatched:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		close(release)
		<-done
		t.Fatal("dispatcher waited for model")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(messenger.sent) != 1 || !strings.HasPrefix(messenger.sent[0], "alex: reminder #") || !strings.Contains(messenger.sent[0], "late") {
		t.Fatal(messenger.sent)
	}
	if worked, err := b.ProcessReminder(context.Background()); err != nil || worked {
		t.Fatalf("resent %t %v", worked, err)
	}
}
func TestDueUncertainDoesNotResend(t *testing.T) {
	b, s, _, _ := profileBridge(t)
	seedDue(t, b, s)
	messenger := &concurrentMessenger{err: errors.New("connection lost after send")}
	b.messenger = messenger
	if _, err := b.ProcessReminder(context.Background()); !errors.Is(err, ErrUncertain) {
		t.Fatal(err)
	}
	if _, err := b.ProcessReminder(context.Background()); !errors.Is(err, ErrUncertain) {
		t.Fatal(err)
	}
	status, err := s.Status(context.Background(), b.config.Source)
	if err != nil || status.UnknownReminders != 1 || !status.Paused || len(messenger.sent) != 1 {
		t.Fatalf("%+v %v sends=%v", status, err, messenger.sent)
	}
}
