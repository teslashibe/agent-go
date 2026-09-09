package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestImportHistoryAggregateBudgetAndRequestLimit(t *testing.T) {
	ctx := context.Background()
	s, source, _ := reminderStore(t)
	s.db.Exec(`UPDATE sources SET session='preserve' WHERE source=?`, source.key())
	messages := []HistoryMessage{archiveMessage(source, 1, source.AllowedSenders[0], strings.Repeat("x", MaxHistoryBytes/2), false), archiveMessage(source, 2, source.AllowedSenders[1], strings.Repeat("y", MaxHistoryBytes/2), false)}
	if _, err := s.ImportHistory(ctx, source, messages); err == nil {
		t.Fatal("aggregate overlimit accepted")
	}
	var count int
	s.db.QueryRow(`SELECT COUNT(*) FROM history_messages`).Scan(&count)
	if count != 0 {
		t.Fatal("aggregate failure partially committed")
	}
	state, _ := s.Status(ctx, source)
	if state.SessionID != "preserve" {
		t.Fatal("aggregate failure reset session")
	}
	if _, err := s.ImportHistory(ctx, source, []HistoryMessage{archiveMessage(source, 1, source.AllowedSenders[0], "safe", false)}); err != nil {
		t.Fatal(err)
	}
	job := conversationJob(t, s, source, source.AllowedSenders[0], strings.Repeat("x", MaxRequestBytes))
	if _, err := s.RequestContext(ctx, source, job, time.Now()); err == nil || !strings.Contains(err.Error(), "refusing to truncate") {
		t.Fatalf("oversize request should fail explicitly: %v", err)
	}
}

func TestImportHistoryPreservesRemindersAndDeduplicatesContext(t *testing.T) {
	ctx := context.Background()
	s, source, _ := reminderStore(t)
	old := conversationJob(t, s, source, source.AllowedSenders[0], "ARCHIVED-ONCE")
	if err := s.CompleteTurn(ctx, source, old.ID, "old-session", []string{"ARCHIVED-ANSWER"}); err != nil {
		t.Fatal(err)
	}
	flushReplies(t, s, source)
	if _, err := s.db.Exec(`INSERT INTO reminders(source,user_id,text,due_utc,created_zone,status) VALUES(?,'alex','keep me',?,'America/Los_Angeles','pending')`, source.key(), time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	var id int64
	var guid string
	if err := s.db.QueryRow(`SELECT message_id,guid FROM inbox WHERE source=? AND guid=(SELECT guid FROM jobs WHERE id=?)`, source.key(), old.ID).Scan(&id, &guid); err != nil {
		t.Fatal(err)
	}
	m := archiveMessage(source, id, source.AllowedSenders[0], "ARCHIVED-ONCE", false)
	m.GUID = guid
	before, _ := s.Status(ctx, source)
	if _, err := s.ImportHistory(ctx, source, []HistoryMessage{archiveMessage(source, id+1, "bot", "ARCHIVED-ANSWER", true), m}); err != nil {
		t.Fatal(err)
	}
	after, _ := s.Status(ctx, source)
	if after.Cursor != before.Cursor || after.PendingReminders != 1 {
		t.Fatalf("changed operational state: %+v", after)
	}
	// The synthetic outgoing archive is beyond the cursor; it cannot become work.
	_, err := s.Accept(ctx, source, Event{ID: id + 1, GUID: archiveMessage(source, id+1, "", "", true).GUID}, "rejected")
	if err != nil {
		t.Fatal(err)
	}
	job := conversationJob(t, s, source, source.AllowedSenders[1], "follow up")
	prompt, err := s.RequestContext(ctx, source, job, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(prompt, "ARCHIVED-ONCE") != 1 || strings.Count(prompt, "ARCHIVED-ANSWER") != 1 {
		t.Fatal("duplicated archived completed turn")
	}
	if strings.Index(prompt, "ARCHIVED-ONCE") > strings.Index(prompt, "ARCHIVED-ANSWER") {
		t.Fatal("archive not chronological")
	}
}
