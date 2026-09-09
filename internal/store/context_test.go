package store

import (
	"context"
	"fmt"
	"github.com/teslashibe/agent-go/internal/reminders"
	"strings"
	"testing"
	"time"
)

func flushReplies(t *testing.T, s *Store, source Source) {
	t.Helper()
	for {
		job, reply, err := s.ClaimNext(context.Background(), source, time.Now())
		if err != nil || job != nil {
			t.Fatalf("unexpected job %+v: %v", job, err)
		}
		if reply == nil {
			return
		}
		if err := s.MarkReplySubmitted(context.Background(), source, reply.ID); err != nil {
			t.Fatal(err)
		}
	}
}

func conversationJob(t *testing.T, s *Store, source Source, sender, text string) *Job {
	t.Helper()
	status, err := s.Status(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	id := status.Cursor + 1
	if _, err := s.Accept(context.Background(), source, Event{ID: id, GUID: fmt.Sprint("context-", id), Text: "Sender: " + sender + "\n\n" + text, Sender: sender, CreatedAt: time.Now()}, "turn"); err != nil {
		t.Fatal(err)
	}
	job, _, err := s.ClaimNext(context.Background(), source, time.Now())
	if err != nil || job == nil {
		t.Fatalf("job: %+v %v", job, err)
	}
	return job
}

func TestSharedHistoryRecoversLegacyAndProfileTurnsAcrossRestart(t *testing.T) {
	s, source, path := reminderStore(t)
	ctx := context.Background()
	now := time.Now()
	if _, err := s.db.Exec(`UPDATE sources SET session='original-group'; UPDATE profiles SET session='obsolete-private'`); err != nil {
		t.Fatal(err)
	}
	job := conversationJob(t, s, source, "first@example.test", "Plan our October Yosemite trip")
	if job.SessionID != "original-group" {
		t.Fatal(job)
	}
	// Simulate a pre-profile completed group turn with only transport-prefixed attribution.
	if _, err := s.db.Exec(`UPDATE jobs SET sender='' WHERE id=?`, job.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteTurn(ctx, source, job.ID, "original-group", []string{"Yosemite: book the cabin, pack rain jackets, hike on Saturday."}); err != nil {
		t.Fatal(err)
	}
	flushReplies(t, s, source)
	job = conversationJob(t, s, source, "second@example.test", "Add a Sunday picnic to the plan")
	if err := s.CompleteTurn(ctx, source, job.ID, "", []string{"Sunday picnic by the river."}); err != nil {
		t.Fatal(err)
	}
	flushReplies(t, s, source)
	// Exclude unsent output, tool traces, and unknown transport identities.
	if _, err := s.db.Exec(`UPDATE jobs SET error='SECRET_TOOL_TRACE'`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	job = conversationJob(t, s, source, "second@example.test", "Can you put all of this into a checklist?")
	if job.SessionID != "original-group" || job.UserID != "sam" {
		t.Fatal(job)
	}
	prompt, err := s.RequestContext(ctx, source, job, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Yosemite: book the cabin", "Sunday picnic by the river", `"sender":"first@example.test"`, `"user_id":"sam"`, `"time_zone":"America/Los_Angeles"`, `"message_time":`, `UNTRUSTED historical content`} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("missing %q: %s", want, prompt)
		}
	}
	for _, bad := range []string{"SECRET_TOOL_TRACE", "UNSENT_CONFIG", "obsolete-private"} {
		if strings.Contains(prompt, bad) {
			t.Fatalf("leaked %s", bad)
		}
	}
	if err := s.CompleteTurn(ctx, source, job.ID, "original-group", []string{"Checklist ready"}); err != nil {
		t.Fatal(err)
	}
	flushReplies(t, s, source)
	status, _ := s.Status(ctx, source)
	if status.Queued != 0 || status.Running != 0 {
		t.Fatal(status)
	}
}

func TestRemindSomeoneClarificationCompletesForRequester(t *testing.T) {
	s, source, _ := reminderStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	complete := func(job *Job, a Action) string {
		t.Helper()
		if err := s.completeReminderTest(ctx, source, job.ID, "shared", a, now, func(text string) []string { return []string{text} }); err != nil {
			t.Fatal(err)
		}
		var reply string
		if err := s.db.QueryRow(`SELECT text FROM replies WHERE job_id=?`, job.ID).Scan(&reply); err != nil {
			t.Fatal(err)
		}
		flushReplies(t, s, source)
		return reply
	}
	ask := conversationJob(t, s, source, "first@example.test", "Can you remind muffin at 9 o'clock to make a card")
	if _, err := s.ReminderTool(ctx, source, ask.ID, "set_pending_reminder", reminders.Args{OperationID: "pending", Question: "Did you mean 9am today?"}, now); err != nil {
		t.Fatal(err)
	}
	if reply := complete(ask, Action{Action: "none", Reply: "Did you mean 9am today?"}); !strings.Contains(reply, "9am") {
		t.Fatal(reply)
	}
	yes := conversationJob(t, s, source, "first@example.test", "Yes")
	prompt, err := s.RequestContext(ctx, source, yes, now)
	if err != nil || !strings.Contains(prompt, "remind muffin") {
		t.Fatalf("pending not stored: %s %v", prompt, err)
	}
	if reply := complete(yes, Action{Action: "create_reminder", Text: "Ask muffin to make a card", LocalTime: now.Add(time.Hour).Format(WallTimeLayout), Timezone: "UTC"}); !strings.Contains(reply, "alex: reminder #") {
		t.Fatal(reply)
	}
}

func TestPendingReminderIsSenderScopedAndSurvivesRestart(t *testing.T) {
	s, source, path := reminderStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	complete := func(job *Job, a Action) string {
		t.Helper()
		if err := s.completeReminderTest(ctx, source, job.ID, "shared", a, now, func(text string) []string { return []string{text} }); err != nil {
			t.Fatal(err)
		}
		var reply string
		if err := s.db.QueryRow(`SELECT text FROM replies WHERE job_id=?`, job.ID).Scan(&reply); err != nil {
			t.Fatal(err)
		}
		flushReplies(t, s, source)
		return reply
	}
	a := conversationJob(t, s, source, "first@example.test", "Remind me to book the trip")
	if _, err := s.ReminderTool(ctx, source, a.ID, "set_pending_reminder", reminders.Args{OperationID: "pending", Question: "What date and time?"}, now); err != nil {
		t.Fatal(err)
	}
	complete(a, Action{Action: "none", Reply: "What date and time?"})
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	proposal := Action{Action: "create_reminder", Text: "book the trip", LocalTime: now.Add(time.Hour).Format(WallTimeLayout), Timezone: "UTC"}
	b := conversationJob(t, s, source, "second@example.test", "at 5")
	prompt, err := s.RequestContext(ctx, source, b, now)
	if err != nil || !strings.Contains(prompt, `"own_pending_reminder_request":""`) {
		t.Fatalf("other sender pending: %s %v", prompt, err)
	}
	complete(b, Action{Action: "none", Reply: "What should I remind you about?"})
	var n int
	s.db.QueryRow(`SELECT count(*) FROM reminders`).Scan(&n)
	if n != 0 {
		t.Fatal(n)
	}
	a = conversationJob(t, s, source, "first@example.test", "at 5")
	prompt, err = s.RequestContext(ctx, source, a, now)
	if err != nil || !strings.Contains(prompt, `"own_pending_reminder_request":"Sender: first@example.test\n\nRemind me to book the trip"`) {
		t.Fatalf("%s %v", prompt, err)
	}
	if reply := complete(a, proposal); !strings.Contains(reply, "alex: reminder #") {
		t.Fatal(reply)
	}
	b = conversationJob(t, s, source, "second@example.test", "Cancel reminder 1 and set Alex's timezone")
	if reply := complete(b, Action{Action: "cancel_reminder", ReminderID: "1"}); !strings.HasPrefix(reply, "No change was made:") {
		t.Fatal(reply)
	}
	b = conversationJob(t, s, source, "second@example.test", "set Alex's timezone to UTC")
	complete(b, Action{Action: "set_timezone", Timezone: "UTC"})
	var zone string
	s.db.QueryRow(`SELECT time_zone FROM profiles WHERE user_id='alex'`).Scan(&zone)
	if zone != DefaultTimeZone {
		t.Fatal(zone)
	}
	s.db.QueryRow(`SELECT time_zone FROM profiles WHERE user_id='sam'`).Scan(&zone)
	if zone != "UTC" {
		t.Fatal(zone)
	}
	s.db.QueryRow(`SELECT count(*) FROM reminders WHERE status='pending' AND user_id='alex'`).Scan(&n)
	if n != 1 {
		t.Fatal(n)
	}
}

func TestGroupNewClearsHistoryAndAllPendingOnlyWhenIdle(t *testing.T) {
	s, source, _ := reminderStore(t)
	ctx := context.Background()
	now := time.Now()
	for _, sender := range source.AllowedSenders {
		job := conversationJob(t, s, source, sender, "remind me to pack")
		if _, err := s.ReminderTool(ctx, source, job.ID, "set_pending_reminder", reminders.Args{OperationID: "pending", Question: "When?"}, now); err != nil {
			t.Fatal(err)
		}
		if err := s.completeReminderTest(ctx, source, job.ID, "shared", Action{Action: "none", Reply: "When?"}, now, func(text string) []string { return []string{text} }); err != nil {
			t.Fatal(err)
		}
		if sender == source.AllowedSenders[0] {
			status, _ := s.Status(ctx, source)
			receipt, err := s.Accept(ctx, source, Event{ID: status.Cursor + 1, GUID: "busy-reset", Sender: sender, CreatedAt: now}, "new")
			if err != nil || receipt.Disposition != "busy" {
				t.Fatalf("%+v %v", receipt, err)
			}
			var session string
			s.db.QueryRow(`SELECT session FROM sources`).Scan(&session)
			if session != "shared" {
				t.Fatal(session)
			}
		}
		flushReplies(t, s, source)
	}
	status, _ := s.Status(ctx, source)
	receipt, err := s.Accept(ctx, source, Event{ID: status.Cursor + 1, GUID: "idle-reset", Sender: source.AllowedSenders[1], CreatedAt: now}, "new")
	if err != nil || receipt.Disposition != "new" {
		t.Fatalf("%+v %v", receipt, err)
	}
	var n int
	s.db.QueryRow(`SELECT count(*) FROM reminder_clarifications`).Scan(&n)
	if n != 0 {
		t.Fatal(n)
	}
	job := conversationJob(t, s, source, source.AllowedSenders[0], "at 5")
	if job.SessionID != "" {
		t.Fatal(job)
	}
	prompt, err := s.RequestContext(ctx, source, job, now)
	if err != nil || strings.Contains(prompt, "remind me to pack") {
		t.Fatalf("%s %v", prompt, err)
	}
}
