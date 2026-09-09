package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func reminderStore(t *testing.T) (*Store, Source, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	source := Source{Name: "test", AllowedSenders: []string{"first@example.test", "second@example.test"}, Group: true, ChatGUID: "any;+;test", ChatID: 42}
	if err = s.Initialize(context.Background(), source, 0, ""); err != nil {
		t.Fatal(err)
	}
	if err = s.SeedProfiles(context.Background(), source, testProfiles()); err != nil {
		t.Fatal(err)
	}
	return s, source, path
}
func testProfiles() []Profile {
	return []Profile{{ID: "alex", Sender: "first@example.test"}, {ID: "sam", Sender: "second@example.test"}}
}
func actionJob(t *testing.T, s *Store, source Source, sender string) *Job {
	t.Helper()
	ctx := context.Background()
	now := time.Now()
	cursor, err := s.Cursor(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Accept(ctx, source, Event{ID: cursor + 1, GUID: fmt.Sprint(cursor + 1), Sender: sender, Text: "remind me about this request", CreatedAt: now}, "turn"); err != nil {
		t.Fatal(err)
	}
	for {
		job, reply, err := s.ClaimNext(ctx, source, now)
		if err != nil {
			t.Fatal(err)
		}
		if reply != nil {
			if err = s.MarkReplySubmitted(ctx, source, reply.ID); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if job == nil {
			t.Fatal("no job")
		}
		return job
	}
}
func runAction(t *testing.T, s *Store, source Source, sender string, a Action, now time.Time) (int64, string) {
	t.Helper()
	job := actionJob(t, s, source, sender)
	if err := s.completeReminderTest(context.Background(), source, job.ID, "shared-session", a, now, func(text string) []string { return []string{text} }); err != nil {
		t.Fatal(err)
	}
	var reply string
	if err := s.db.QueryRow(`SELECT text FROM replies WHERE job_id=?`, job.ID).Scan(&reply); err != nil {
		t.Fatal(err)
	}
	return job.ID, reply
}
func TestNewResetsSharedSession(t *testing.T) {
	s, source, _ := reminderStore(t)
	ctx := context.Background()
	now := time.Now()
	runAction(t, s, source, "first@example.test", Action{Action: "none", Reply: "hello"}, now)
	runAction(t, s, source, "second@example.test", Action{Action: "none", Reply: "when?"}, now)
	for {
		_, r, err := s.ClaimNext(ctx, source, now)
		if err != nil {
			t.Fatal(err)
		}
		if r == nil {
			break
		}
		if err = s.MarkReplySubmitted(ctx, source, r.ID); err != nil {
			t.Fatal(err)
		}
	}
	cursor, _ := s.Cursor(ctx, source)
	if _, err := s.Accept(ctx, source, Event{ID: cursor + 1, GUID: "new", Sender: "first@example.test", CreatedAt: now}, "new"); err != nil {
		t.Fatal(err)
	}
	status, err := s.Status(ctx, source)
	if err != nil || status.SessionID != "" {
		t.Fatalf("session after /new: %+v %v", status, err)
	}
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM reminder_clarifications`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("pending after /new: %d %v", count, err)
	}
	changedProfiles := testProfiles()
	changedProfiles[0].Sender = "second@example.test"
	changedProfiles[1].Sender = "first@example.test"
	if err := s.SeedProfiles(ctx, source, changedProfiles); err == nil {
		t.Fatal("identity reassignment allowed")
	}
}

func TestProfileValidation(t *testing.T) {
	source := Source{AllowedSenders: []string{"a", "b"}, Group: true}
	for _, tc := range []struct {
		profiles []Profile
		valid    bool
	}{
		{[]Profile{{ID: "alex", Sender: "a"}, {ID: "sam", Sender: "b"}}, true},
		{[]Profile{{ID: "alex", Sender: "a", TimeZone: "Europe/London"}}, true},
		{[]Profile{{ID: "alex", Sender: "a", TimeZone: "PST"}}, false},
		{[]Profile{{ID: "alex", Sender: "a"}, {ID: "alex", Sender: "b"}}, false},
		{[]Profile{{ID: "alex", Sender: "a"}, {ID: "sam", Sender: "a"}}, false},
		{[]Profile{{ID: "Sam", Sender: "a"}}, false},
		{[]Profile{{ID: "sam", Sender: " a"}}, false},
		{[]Profile{{ID: "sam", Sender: "c"}}, false},
	} {
		if err := ValidateProfiles(source, tc.profiles); (err == nil) != tc.valid {
			t.Fatalf("%+v: %v", tc, err)
		}
	}
}
func TestReminderValidationAndPendingRestart(t *testing.T) {
	s, source, path := reminderStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	for _, a := range []Action{
		{Action: "create_reminder", Text: "missing time"},
		{Action: "create_reminder", Text: strings.Repeat("x", 501), LocalTime: now.Add(time.Hour).Format(WallTimeLayout), Timezone: "UTC"},
		{Action: "create_reminder", Text: "past", LocalTime: now.Add(-time.Hour).Format(WallTimeLayout), Timezone: "UTC"},
		{Action: "set_timezone", Timezone: "PST"},
	} {
		_, reply := runAction(t, s, source, "first@example.test", a, now)
		if !strings.HasPrefix(reply, "No change was made:") {
			t.Fatal(reply)
		}
	}
	_, reply := runAction(t, s, source, "first@example.test", Action{Action: "create_reminder", Text: "survive restart", LocalTime: now.Add(time.Hour).Format(WallTimeLayout), Timezone: "UTC"}, now)
	if !strings.Contains(reply, "scheduled") {
		t.Fatal(reply)
	}
	// Submit only the confirmation before restart; the pending reminder remains.
	for {
		_, r, err := s.ClaimNext(ctx, source, now)
		if err != nil {
			t.Fatal(err)
		}
		if r == nil {
			break
		}
		if err = s.MarkReplySubmitted(ctx, source, r.ID); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()
	var err error
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.SeedProfiles(ctx, source, testProfiles()); err != nil {
		t.Fatal(err)
	}
	reminder, err := s.ClaimDue(ctx, source, now.Add(2*time.Hour))
	if err != nil || reminder == nil || reminder.Text != "survive restart" {
		t.Fatalf("%+v %v", reminder, err)
	}
	if err = s.FinishReminder(ctx, source, reminder.ID, true); err != nil {
		t.Fatal(err)
	}
	if r, err := s.ClaimDue(ctx, source, now.Add(3*time.Hour)); err != nil || r != nil {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestActionsOwnershipSessionsAndRestart(t *testing.T) {
	s, source, path := reminderStore(t)
	ctx := context.Background()
	now := time.Now()
	for _, p := range testProfiles() {
		var zone string
		if err := s.db.QueryRow(`SELECT time_zone FROM profiles WHERE user_id=?`, p.ID).Scan(&zone); err != nil || zone != DefaultTimeZone {
			t.Fatalf("%s zone=%s err=%v", p.ID, zone, err)
		}
	}
	due := now.Add(time.Hour).UTC().Truncate(time.Second)
	a := Action{Action: "create_reminder", Text: "Sam's reminder", LocalTime: due.Format(WallTimeLayout), Timezone: "UTC", Reply: "FALSE CONFIRMATION"}
	jobID, reply := runAction(t, s, source, "second@example.test", a, now)
	if !strings.HasPrefix(reply, "sam: reminder #") || strings.Contains(reply, a.Reply) {
		t.Fatal(reply)
	}
	if err := s.completeReminderTest(ctx, source, jobID, "duplicate", a, now, func(s string) []string { return []string{s} }); err == nil {
		t.Fatal("duplicate completion succeeded")
	}
	var id, count int64
	if err := s.db.QueryRow(`SELECT min(id),count(*) FROM reminders`).Scan(&id, &count); err != nil || count != 1 {
		t.Fatalf("count %d: %v", count, err)
	}
	alex := actionJob(t, s, source, "first@example.test")
	if alex.SessionID != "shared-session" || alex.UserID != "alex" {
		t.Fatalf("leaked session: %+v", alex)
	}
	prompt, err := s.RequestContext(ctx, source, alex, now)
	if err != nil || !strings.Contains(prompt, `"pending_reminders":[]`) || !strings.Contains(prompt, `"user_id":"alex"`) {
		t.Fatalf("prompt=%s err=%v", prompt, err)
	}
	if err = s.completeReminderTest(ctx, source, alex.ID, "alex-session", Action{Action: "cancel_reminder", ReminderID: fmt.Sprint(id), Reply: "cancelled"}, now, func(s string) []string { return []string{s} }); err != nil {
		t.Fatal(err)
	}
	var status string
	s.db.QueryRow(`SELECT status FROM reminders WHERE id=?`, id).Scan(&status)
	if status != "pending" {
		t.Fatal("cross-user cancellation succeeded")
	}
	_, reply = runAction(t, s, source, "first@example.test", Action{Action: "list_reminders"}, now)
	if strings.Contains(reply, "Sam's reminder") {
		t.Fatal(reply)
	}
	_, reply = runAction(t, s, source, "second@example.test", Action{Action: "set_timezone", Timezone: "Europe/London", Reply: "wrong"}, now)
	if !strings.Contains(reply, "sam: timezone set to Europe/London") {
		t.Fatal(reply)
	}
	var storedDue int64
	var createdZone string
	s.db.QueryRow(`SELECT due_utc,created_zone FROM reminders WHERE id=?`, id).Scan(&storedDue, &createdZone)
	if storedDue != due.Unix() || createdZone != "UTC" {
		t.Fatal("timezone change rescheduled reminder")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.SeedProfiles(ctx, source, testProfiles()); err != nil {
		t.Fatal(err)
	}
	var zone string
	s.db.QueryRow(`SELECT time_zone FROM profiles WHERE user_id='sam'`).Scan(&zone)
	if zone != "Europe/London" {
		t.Fatal("seed overwrote saved zone", zone)
	}
	sam := actionJob(t, s, source, "second@example.test")
	if sam.SessionID != "shared-session" {
		t.Fatalf("lost shared conversation context: %+v", sam)
	}
	if err = s.completeReminderTest(ctx, source, sam.ID, sam.SessionID, Action{Action: "cancel_reminder", ReminderID: fmt.Sprint(id)}, now, func(s string) []string { return []string{s} }); err != nil {
		t.Fatal(err)
	}
	s.db.QueryRow(`SELECT status FROM reminders WHERE id=?`, id).Scan(&status)
	if status != "cancelled" {
		t.Fatal(status)
	}
}
func TestReminderReceiptSurvivesFinalOutboxFailure(t *testing.T) {
	s, source, _ := reminderStore(t)
	ctx := context.Background()
	now := time.Now()
	job := actionJob(t, s, source, "first@example.test")
	if _, err := s.db.Exec(`CREATE TRIGGER fail_reply BEFORE INSERT ON replies BEGIN SELECT RAISE(ABORT,'test outbox failure'); END`); err != nil {
		t.Fatal(err)
	}
	a := Action{Action: "set_timezone", Timezone: "Europe/London"}
	if err := s.completeReminderTest(ctx, source, job.ID, "session", a, now, func(s string) []string { return []string{s} }); err == nil {
		t.Fatal("expected outbox failure")
	}
	var zone, session, state string
	s.db.QueryRow(`SELECT time_zone,session FROM profiles WHERE user_id='alex'`).Scan(&zone, &session)
	s.db.QueryRow(`SELECT state FROM jobs WHERE id=?`, job.ID).Scan(&state)
	if zone != "Europe/London" || session != "" || state != "running" {
		t.Fatalf("partial commit %s %s %s", zone, session, state)
	}
}
func TestDueClaimRecoveryAndCancellation(t *testing.T) {
	s, source, path := reminderStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	_, _ = runAction(t, s, source, "first@example.test", Action{Action: "create_reminder", Text: "due", LocalTime: now.Add(time.Minute).Format(WallTimeLayout), Timezone: "UTC"}, now)
	if r, err := s.ClaimDue(ctx, source, now); err != nil || r != nil {
		t.Fatalf("early delivery %+v %v", r, err)
	}
	var wg sync.WaitGroup
	results := make(chan *Reminder, 2)
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := s.ClaimDue(ctx, source, now.Add(time.Hour))
			results <- r
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	claimed := 0
	var id int64
	for r := range results {
		if r != nil {
			claimed++
			id = r.ID
		}
	}
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if claimed != 1 {
		t.Fatalf("claimed %d", claimed)
	}
	_, reply := runAction(t, s, source, "first@example.test", Action{Action: "cancel_reminder", ReminderID: fmt.Sprint(id)}, now)
	if !strings.Contains(reply, "No change was made") {
		t.Fatal(reply)
	}
	s.Close()
	var err error
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	status, err := s.Status(ctx, source)
	if err != nil || !status.Paused || status.UnknownReminders != 1 {
		t.Fatalf("status %+v %v", status, err)
	}
	if _, err = s.ClaimDue(ctx, source, now.Add(time.Hour)); !errors.Is(err, ErrUncertain) {
		t.Fatal(err)
	}
	if err = s.DiscardRepliesAndResume(ctx, source, ""); err != nil {
		t.Fatal(err)
	}
	if r, err := s.ClaimDue(ctx, source, now.Add(time.Hour)); err != nil || r != nil {
		t.Fatalf("resent uncertain reminder %+v %v", r, err)
	}
}
func TestLegacyJobsMigrateWithoutAuthority(t *testing.T) {
	s, source, path := reminderStore(t)
	ctx := context.Background()
	now := time.Now()
	job := actionJob(t, s, source, "")
	if err := s.completeReminderTest(ctx, source, job.ID, "", Action{Action: "create_reminder", Text: "forged", Timezone: "UTC", LocalTime: now.Add(time.Hour).UTC().Format(WallTimeLayout)}, now, func(s string) []string { return []string{s} }); err != nil {
		t.Fatal(err)
	}
	var count int
	s.db.QueryRow(`SELECT count(*) FROM reminders`).Scan(&count)
	if count != 0 {
		t.Fatal("legacy job executed action")
	}
	s.Close()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`ALTER TABLE jobs DROP COLUMN sender`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var sender string
	if err = s.db.QueryRow(`SELECT sender FROM jobs LIMIT 1`).Scan(&sender); err != nil || sender != "" {
		t.Fatalf("migration sender=%q %v", sender, err)
	}
}
