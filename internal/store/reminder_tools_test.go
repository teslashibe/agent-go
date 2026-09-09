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

func TestReminderToolStructuredOutcomes(t *testing.T) {
	s, source, _ := reminderStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	job := actionJob(t, s, source, "first@example.test")
	call := func(name string, args reminders.Args) ReminderToolResult {
		t.Helper()
		raw, err := s.ReminderTool(ctx, source, job.ID, name, args, now)
		if err != nil {
			t.Fatal(err)
		}
		retry, err := s.ReminderTool(ctx, source, job.ID, name, args, now.Add(time.Second))
		if err != nil || retry != raw {
			t.Fatalf("receipt mismatch %s %s %v", raw, retry, err)
		}
		var result ReminderToolResult
		if err := json.Unmarshal([]byte(raw), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	zone := call("get_timezone", reminders.Args{OperationID: "zone"})
	if !zone.TimezoneConfigured || zone.Timezone != DefaultTimeZone || zone.Changed {
		t.Fatalf("zone=%+v", zone)
	}
	unchanged := call("set_timezone", reminders.Args{OperationID: "same-zone", Timezone: zone.Timezone})
	if unchanged.Changed || unchanged.IsError {
		t.Fatalf("unchanged=%+v", unchanged)
	}
	changed := call("set_timezone", reminders.Args{OperationID: "new-zone", Timezone: "UTC"})
	if !changed.Changed || changed.Timezone != "UTC" {
		t.Fatalf("changed=%+v", changed)
	}
	empty := call("clear_pending_reminder", reminders.Args{OperationID: "empty"})
	if empty.Changed || empty.Pending != nil {
		t.Fatalf("empty=%+v", empty)
	}
	pending := call("set_pending_reminder", reminders.Args{OperationID: "pending", Question: "When?"})
	if !pending.Changed || pending.Pending == nil || pending.Pending.Question != "When?" || pending.Pending.Text == "" || !pending.Pending.ExpiresAt.Equal(now.Add(24*time.Hour)) {
		t.Fatalf("pending=%+v", pending)
	}
	again := call("set_pending_reminder", reminders.Args{OperationID: "pending-same", Question: "When?"})
	if again.Changed || again.Pending == nil || *again.Pending != *pending.Pending {
		t.Fatalf("again=%+v", again)
	}
	cleared := call("clear_pending_reminder", reminders.Args{OperationID: "clear"})
	if !cleared.Changed || cleared.Pending != nil {
		t.Fatalf("clear=%+v", cleared)
	}
	created := call("create_reminder", reminders.Args{OperationID: "create", Text: "stretch", LocalTime: now.Add(time.Hour).Format(WallTimeLayout)})
	if !created.Changed || created.Reminder == nil || created.Reminder.ID == 0 || created.Reminder.Text != "stretch" || created.Reminder.CreatedZone != "UTC" || !created.Reminder.DueUTC.Equal(now.Add(time.Hour)) {
		t.Fatalf("created=%+v", created)
	}
	listed := call("list_reminders", reminders.Args{OperationID: "list"})
	if listed.Changed || len(listed.Reminders) != 1 || listed.Reminders[0].ID != created.Reminder.ID {
		t.Fatalf("list=%+v", listed)
	}
	cancelled := call("cancel_reminder", reminders.Args{OperationID: "cancel", ReminderID: fmt.Sprint(created.Reminder.ID)})
	if !cancelled.Changed || cancelled.Reminder == nil || cancelled.Reminder.Status != "cancelled" {
		t.Fatalf("cancel=%+v", cancelled)
	}
	refused := call("cancel_reminder", reminders.Args{OperationID: "cancel-again", ReminderID: fmt.Sprint(created.Reminder.ID)})
	if refused.Changed || !refused.IsError || refused.Reminder == nil {
		t.Fatalf("refused=%+v", refused)
	}
	listed = call("list_reminders", reminders.Args{OperationID: "empty-list"})
	if listed.Reminders == nil || len(listed.Reminders) != 0 {
		t.Fatalf("list=%+v", listed)
	}
}

// completeReminderTest exercises tool mutation then the separate final turn.
func (s *Store) completeReminderTest(ctx context.Context, source Source, jobID int64, session string, a Action, now time.Time, split func(string) []string) error {
	reply := a.Reply
	if a.Action != "none" && a.Action != "" {
		result, err := s.ReminderTool(ctx, source, jobID, a.Action, reminders.Args{OperationID: "reminder", Text: a.Text, LocalTime: a.LocalTime, Timezone: a.Timezone, ReminderID: a.ReminderID}, now)
		if err != nil {
			reply = "No change was made: " + err.Error()
		} else {
			var outcome struct {
				Message string `json:"message"`
			}
			if err := json.Unmarshal([]byte(result), &outcome); err != nil {
				return err
			}
			reply = outcome.Message
		}
	}
	return s.CompleteTurn(ctx, source, jobID, session, split(reply))
}

func TestReminderToolAtomicReceiptAndRetry(t *testing.T) {
	s, source, _ := reminderStore(t)
	sender := source.AllowedSenders[0]
	ctx := context.Background()
	now := time.Now().UTC()
	job := actionJob(t, s, source, sender)
	args := reminders.Args{OperationID: "create", Text: "test", LocalTime: now.Add(time.Hour).Format(WallTimeLayout), Timezone: "UTC"}
	if _, err := s.db.Exec(`CREATE TRIGGER reject_receipt BEFORE INSERT ON tool_operations BEGIN SELECT RAISE(ABORT,'receipt failed'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReminderTool(ctx, source, job.ID, "create_reminder", args, now); err == nil {
		t.Fatal("receipt failure accepted")
	}
	var count int
	s.db.QueryRow(`SELECT count(*) FROM reminders`).Scan(&count)
	if count != 0 {
		t.Fatal("mutation survived failed receipt")
	}
	s.db.Exec(`DROP TRIGGER reject_receipt`)
	first, err := s.ReminderTool(ctx, source, job.ID, "create_reminder", args, now)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.ReminderTool(ctx, source, job.ID, "create_reminder", args, now)
	if err != nil || first != again {
		t.Fatalf("retry %s %v", again, err)
	}
	s.db.QueryRow(`SELECT count(*) FROM reminders`).Scan(&count)
	if count != 1 {
		t.Fatal(count)
	}
	args.Text = "changed"
	if _, err = s.ReminderTool(ctx, source, job.ID, "create_reminder", args, now); err == nil {
		t.Fatal("changed arguments accepted")
	}
	var state string
	s.db.QueryRow(`SELECT state FROM jobs WHERE id=?`, job.ID).Scan(&state)
	if state != "running" {
		t.Fatal("tool completed turn")
	}
	if _, err := s.ReminderTool(ctx, source, job.ID, "get_timezone", reminders.Args{OperationID: "timezone"}, now); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteTurn(ctx, source, job.ID, "session", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReminderTool(ctx, source, job.ID, "get_timezone", reminders.Args{OperationID: "timezone"}, now); err == nil {
		t.Fatal("closed job accepted tool")
	}
	if err := s.CompleteAction(ctx, source, job.ID, "", Action{Action: "create_reminder"}, now, func(string) []string { return nil }); err == nil {
		t.Fatal("legacy final action accepted")
	}
}

func TestPendingToolPreservesOriginalRequesterAndExpiry(t *testing.T) {
	s, source, _ := reminderStore(t)
	sender := source.AllowedSenders[0]
	ctx := context.Background()
	now := time.Now()
	first := conversationJob(t, s, source, sender, "Don't let me forget the bread")
	if _, err := s.ReminderTool(ctx, source, first.ID, "set_pending_reminder", reminders.Args{OperationID: "pending", Question: "At what time?"}, now); err != nil {
		t.Fatal(err)
	}
	s.CompleteTurn(ctx, source, first.ID, "session", []string{"At what time?"})
	flushReplies(t, s, source)
	next := conversationJob(t, s, source, sender, "yes")
	if _, err := s.ReminderTool(ctx, source, next.ID, "set_pending_reminder", reminders.Args{OperationID: "pending", Question: "Which time today?"}, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	prompt, err := s.RequestContext(ctx, source, next, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, "Don't let me forget the bread") || !strings.Contains(prompt, "Which time today?") {
		t.Fatal(prompt)
	}
	var original string
	var created int64
	s.db.QueryRow(`SELECT prompt,created_at FROM reminder_clarifications`).Scan(&original, &created)
	if !strings.Contains(original, "bread") || created != now.UnixNano() {
		t.Fatalf("overwritten %s %d", original, created)
	}
	s.CompleteTurn(ctx, source, next.ID, "session", nil)
	other := conversationJob(t, s, source, "second@example.test", "yes")
	prompt, err = s.RequestContext(ctx, source, other, now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, `"own_pending_reminder_request":""`) {
		t.Fatal("cross-requester pending leak")
	}
	s.CompleteTurn(ctx, source, other.ID, "session", nil)
	expired := conversationJob(t, s, source, sender, "new task")
	prompt, err = s.RequestContext(ctx, source, expired, now.Add(25*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, `"own_pending_reminder_request":""`) {
		t.Fatal("expired pending leak")
	}
	if _, err := s.ReminderTool(ctx, source, expired.ID, "clear_pending_reminder", reminders.Args{OperationID: "clear"}, now.Add(25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	var count int
	s.db.QueryRow(`SELECT count(*) FROM reminder_clarifications`).Scan(&count)
	if count != 0 {
		t.Fatal("pending not cleared")
	}
}
