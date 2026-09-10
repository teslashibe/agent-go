package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/teslashibe/agent-go/internal/reminders"
)

func reminderResult(t *testing.T, s *Store, source Source, job *Job, name string, args reminders.Args, now time.Time) ReminderToolResult {
	t.Helper()
	raw, err := s.ReminderTool(context.Background(), source, job.ID, name, args, now)
	if err != nil {
		t.Fatal(err)
	}
	var result ReminderToolResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestReminderCreatorAndRecipientRights(t *testing.T) {
	for _, actor := range []string{"alex", "sam", "lee"} {
		t.Run(actor, func(t *testing.T) {
			s, _, _ := reminderStore(t)
			ctx, now := context.Background(), time.Now().UTC()
			source := Source{Name: "rights", Group: true, ChatID: 43, ChatGUID: "any;+;rights", AllowedSenders: []string{"alex@example.test", "sam@example.test", "lee@example.test"}}
			if err := s.Initialize(ctx, source, 0, ""); err != nil {
				t.Fatal(err)
			}
			profiles := []Profile{{ID: "alex", Sender: source.AllowedSenders[0], TimeZone: "UTC"}, {ID: "sam", Sender: source.AllowedSenders[1], TimeZone: "Asia/Tokyo"}, {ID: "lee", Sender: source.AllowedSenders[2], TimeZone: "UTC"}}
			if err := s.SeedProfiles(ctx, source, profiles); err != nil {
				t.Fatal(err)
			}
			job := actionJob(t, s, source, source.AllowedSenders[0])
			args := reminders.Args{OperationID: "create", RecipientID: "sam", Text: "blue folder", LocalTime: now.Add(time.Hour).Format(WallTimeLayout)}
			created := reminderResult(t, s, source, job, "create_reminder", args, now)
			if created.IsError || created.Reminder == nil || created.Reminder.UserID != "sam" || created.Reminder.CreatedBy != "alex" || created.Reminder.CreatedZone != "UTC" || !strings.Contains(created.Message, "requested by alex") {
				t.Fatal(created)
			}
			replay := reminderResult(t, s, source, job, "create_reminder", args, now)
			if replay.Reminder.ID != created.Reminder.ID {
				t.Fatal("duplicate reminder")
			}
			args.RecipientID = "lee"
			if _, err := s.ReminderTool(ctx, source, job.ID, "create_reminder", args, now); err == nil {
				t.Fatal("changed replay target accepted")
			}
			if err := s.CompleteTurn(ctx, source, job.ID, "session", nil); err != nil {
				t.Fatal(err)
			}
			job = actionJob(t, s, source, actor+"@example.test")
			listed := reminderResult(t, s, source, job, "list_reminders", reminders.Args{OperationID: "list"}, now)
			allowed := actor != "lee"
			if (len(listed.Reminders) == 1) != allowed {
				t.Fatal("incorrect visibility", listed)
			}
			cancelled := reminderResult(t, s, source, job, "cancel_reminder", reminders.Args{OperationID: "cancel", ReminderID: fmt.Sprint(created.Reminder.ID)}, now)
			if cancelled.Changed != allowed || cancelled.IsError == allowed {
				t.Fatal("incorrect cancellation authority", cancelled)
			}
			var zone string
			if err := s.db.QueryRow(`SELECT time_zone FROM profiles WHERE source=? AND user_id='sam'`, source.key()).Scan(&zone); err != nil || zone != "Asia/Tokyo" {
				t.Fatal("recipient timezone changed", zone, err)
			}
		})
	}
}

func TestReminderRecipientClarificationSurvivesRestart(t *testing.T) {
	s, source, path := reminderStore(t)
	ctx, now := context.Background(), time.Now().UTC()
	job := conversationJob(t, s, source, "first@example.test", "Remind Sam to pack the blue folder")
	result := reminderResult(t, s, source, job, "set_pending_reminder", reminders.Args{OperationID: "pending", RecipientID: "sam", Question: "When should I remind Sam in this group?"}, now)
	if result.Pending == nil || result.Pending.RecipientID != "sam" {
		t.Fatal(result)
	}
	if err := s.CompleteTurn(ctx, source, job.ID, "session", nil); err != nil {
		t.Fatal(err)
	}
	s.Close()
	var err error
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	other := conversationJob(t, s, source, "second@example.test", "Tomorrow")
	prompt, err := s.RequestContext(ctx, source, other, now)
	if err != nil || !strings.Contains(prompt, `"own_pending_reminder_recipient":""`) {
		t.Fatal("pending target leaked", err)
	}
	if err := s.CompleteTurn(ctx, source, other.ID, "session", nil); err != nil {
		t.Fatal(err)
	}
	job = conversationJob(t, s, source, "first@example.test", "Tomorrow at 09:00 UTC")
	prompt, err = s.RequestContext(ctx, source, job, now)
	if err != nil || !strings.Contains(prompt, `"own_pending_reminder_recipient":"sam"`) || !strings.Contains(prompt, `"reminder_recipients":["alex","sam"]`) {
		t.Fatal("lost target or directory", err)
	}
	args := reminders.Args{OperationID: "changed", RecipientID: "alex", Text: "blue folder", LocalTime: now.Add(24 * time.Hour).Format(WallTimeLayout), Timezone: "UTC"}
	if _, err := s.ReminderTool(ctx, source, job.ID, "create_reminder", args, now); err == nil {
		t.Fatal("silently changed pending recipient")
	}
	args.OperationID = "create"
	args.RecipientID = ""
	result = reminderResult(t, s, source, job, "create_reminder", args, now)
	if result.IsError || result.Reminder == nil || result.Reminder.UserID != "sam" || result.Reminder.CreatedBy != "alex" {
		t.Fatal(result)
	}
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM reminder_clarifications`).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
}

func TestReminderRecipientAuthorizationAndRemoval(t *testing.T) {
	s, source, _ := reminderStore(t)
	ctx, now := context.Background(), time.Now().UTC()
	job := actionJob(t, s, source, "first@example.test")
	args := reminders.Args{OperationID: "create", Text: "fixture", LocalTime: now.Add(time.Minute).Format(WallTimeLayout), Timezone: "UTC"}
	// A profile belonging to another source must not be exposed or selectable.
	other := source
	other.ChatID = 70
	other.ChatGUID = "any;+;other"
	if err := s.Initialize(ctx, other, 0, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedProfiles(ctx, other, []Profile{{ID: "lee", Sender: "first@example.test", TimeZone: "UTC"}}); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"unknown", "lee", "Sam", "sam\n"} {
		args.RecipientID = target
		if _, err := s.ReminderTool(ctx, source, job.ID, "create_reminder", args, now); err == nil {
			t.Fatal("invalid recipient allowed", target)
		}
	}
	args.RecipientID = "sam"
	created := reminderResult(t, s, source, job, "create_reminder", args, now)
	if created.IsError {
		t.Fatal(created)
	}
	if err := s.CompleteTurn(ctx, source, job.ID, "session", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedProfiles(ctx, source, testProfiles()[:1]); err != nil {
		t.Fatal(err)
	}
	if due, err := s.ClaimDue(ctx, source, now.Add(2*time.Minute)); err != nil || due != nil {
		t.Fatal("removed recipient received dispatch", due, err)
	}
	job = actionJob(t, s, source, "first@example.test")
	if _, err := s.ReminderTool(ctx, source, job.ID, "create_reminder", args, now); err == nil {
		t.Fatal("inactive recipient allowed")
	}
	prompt, err := s.RequestContext(ctx, source, job, now)
	if err != nil || !strings.Contains(prompt, `"reminder_recipients":["alex"]`) {
		t.Fatal("inactive target exposed", err)
	}
	self := reminderResult(t, s, source, job, "create_reminder", reminders.Args{OperationID: "self", Text: "self fixture", LocalTime: args.LocalTime, Timezone: "UTC"}, now)
	if self.Reminder == nil || self.Reminder.UserID != "alex" || self.Reminder.CreatedBy != "alex" {
		t.Fatal("self reminder changed", self)
	}
}

func TestReminderCreatorMigrationPreservesExistingRows(t *testing.T) {
	s, source, path := reminderStore(t)
	ctx, now := context.Background(), time.Now().UTC().Truncate(time.Second)
	runAction(t, s, source, "first@example.test", Action{Action: "create_reminder", Text: "retained", LocalTime: now.Add(time.Hour).Format(WallTimeLayout), Timezone: "UTC"}, now)
	flushReplies(t, s, source)
	if _, err := s.db.Exec(`ALTER TABLE reminders DROP COLUMN created_by; ALTER TABLE reminder_clarifications DROP COLUMN recipient_id`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	var err error
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var recipient, creator, text, status string
	var due int64
	if err := s.db.QueryRow(`SELECT user_id,created_by,text,status,due_utc FROM reminders`).Scan(&recipient, &creator, &text, &status, &due); err != nil {
		t.Fatal(err)
	}
	if recipient != "alex" || creator != "alex" || text != "retained" || status != "pending" || due != now.Add(time.Hour).Unix() {
		t.Fatal("migration reinterpreted reminder")
	}
	claimed, err := s.ClaimDue(ctx, source, now.Add(2*time.Hour))
	if err != nil || claimed == nil || claimed.CreatedBy != "alex" {
		t.Fatal(claimed, err)
	}
}
