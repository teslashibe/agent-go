package bridge

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/teslashibe/agent-go/internal/store"
	"github.com/teslashibe/notes"
)

// Authenticated model, temporary durable state and fake message transport only.
func TestRealCodexReminderContinuation(t *testing.T) {
	runner := newRealSmokeRunner(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "reminders.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	source := store.Source{Name: "profiles-test", AllowedSenders: []string{"owner", "partner"}, Group: true, ChatGUID: "any;+;profiles", ChatID: 9}
	if err = s.Initialize(ctx, source, 0, ""); err != nil {
		t.Fatal(err)
	}
	if err = s.SeedProfiles(ctx, source, []store.Profile{{ID: "alex", Sender: "owner", TimeZone: "UTC"}, {ID: "sam", Sender: "partner", TimeZone: "UTC"}}); err != nil {
		t.Fatal(err)
	}
	messenger := &fakeMessenger{}
	b, err := New(s, runner, messenger, Config{Source: source, StructuredActions: true})
	if err != nil {
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
	turn := func(id int64, text string) {
		t.Helper()
		if _, err := b.Receive(ctx, profileMessage(id, fmt.Sprintf("fixture-%d", id), text)); err != nil {
			t.Fatal(err)
		}
		drain(t, b)
		if len(messenger.texts) != int(id) {
			t.Fatalf("expected one reply per turn, got %d", len(messenger.texts))
		}
	}
	const request = "Don't let me forget to pack the blue folder."
	turn(1, request)
	var pending, question string
	if err = db.QueryRow(`SELECT prompt,question FROM reminder_clarifications WHERE sender='owner'`).Scan(&pending, &question); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pending, request) || strings.TrimSpace(question) == "" || count(`SELECT count(*) FROM reminders`) != 0 {
		t.Fatal("ambiguous request was not saved without scheduling")
	}
	target := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Second)
	turn(2, target.Format("2006-01-02 at 15:04:05 UTC")+", please.")
	if count(`SELECT count(*) FROM reminders`) != 1 || count(`SELECT count(*) FROM reminder_clarifications`) != 0 {
		t.Fatal("clarification must consume pending request and create exactly one reminder")
	}
	var id, due int64
	var owner, text, status string
	if err = db.QueryRow(`SELECT id,user_id,text,due_utc,status FROM reminders`).Scan(&id, &owner, &text, &due, &status); err != nil {
		t.Fatal(err)
	}
	if owner != "alex" || due != target.Unix() || !strings.Contains(strings.ToLower(text), "blue folder") || status != "pending" {
		t.Fatalf("incorrect reminder: owner=%s due=%d text=%q status=%s", owner, due, text, status)
	}
	if count(`SELECT count(*) FROM tool_operations WHERE state='completed' AND json_extract(arguments,'$.Name')='create_reminder'`) != 1 {
		t.Fatal("expected one committed creation receipt")
	}
	turn(3, fmt.Sprintf("Cancel my reminder #%d; I have packed the blue folder.", id))
	if err = db.QueryRow(`SELECT status FROM reminders WHERE id=?`, id).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "cancelled" {
		t.Fatalf("reminder was not canceled: %s", status)
	}
	if count(`SELECT count(*) FROM reminders`) != 1 {
		t.Fatal("cancellation created another reminder")
	}
	client := &smokeNotes{nativeFixtureNotes: nativeFixtureNotes{catalog: []notes.Note{{ID: "packing", Name: "Packing", Shared: true, Body: "<div>Blue folder and earplugs.</div>"}}}}
	b.notes = client
	if err = s.ConfigureNotes(ctx, source, []store.NoteScope{{ID: "packing", Title: "Packing", State: "shared"}}); err != nil {
		t.Fatal(err)
	}
	next := target.Add(time.Hour)
	turn(4, "Read my Packing note and tell me what it says, then schedule a reminder to review the packing list on "+next.Format("2006-01-02 at 15:04:05 UTC")+".")
	if !slices.Contains(client.events, "get:packing") {
		t.Fatal("combined request did not read the note")
	}
	if count(`SELECT count(*) FROM reminders`) != 2 {
		t.Fatal("combined request did not create exactly one additional reminder")
	}
	if err = db.QueryRow(`SELECT user_id,due_utc,status FROM reminders WHERE id!=?`, id).Scan(&owner, &due, &status); err != nil {
		t.Fatal(err)
	}
	if owner != "alex" || due != next.Unix() || status != "pending" {
		t.Fatal("combined request scheduled incorrect reminder")
	}
	if count(`SELECT count(*) FROM tool_operations WHERE state='completed' AND json_extract(arguments,'$.Name')='create_reminder'`) != 2 {
		t.Fatal("combined request missing committed creation receipt")
	}

	// A named group recipient remains bound through a clarification turn.
	turn(5, "Remind Sam to pack the green folder.")
	var recipient string
	if err = db.QueryRow(`SELECT recipient_id FROM reminder_clarifications WHERE sender='owner'`).Scan(&recipient); err != nil || recipient != "sam" {
		t.Fatal("missing selected group recipient", recipient, err)
	}
	if count(`SELECT count(*) FROM reminders`) != 2 {
		t.Fatal("scheduled without a time")
	}
	peerDue := target.Add(2 * time.Hour)
	turn(6, peerDue.Format("2006-01-02 at 15:04:05 UTC")+", here in this group.")
	var creator string
	if err = db.QueryRow(`SELECT id,user_id,created_by,text,due_utc,status FROM reminders ORDER BY id DESC LIMIT 1`).Scan(&id, &owner, &creator, &text, &due, &status); err != nil {
		t.Fatal(err)
	}
	if count(`SELECT count(*) FROM reminders`) != 3 || owner != "sam" || creator != "alex" || due != peerDue.Unix() || status != "pending" || !strings.Contains(strings.ToLower(text), "green folder") {
		t.Fatal("wrong cross-participant reminder", owner, creator, due, status, text)
	}
	if count(`SELECT count(*) FROM reminder_clarifications`) != 0 {
		t.Fatal("pending request not consumed")
	}
	turn(7, fmt.Sprintf("Cancel the reminder #%d I just requested for Sam.", id))
	if err = db.QueryRow(`SELECT status FROM reminders WHERE id=?`, id).Scan(&status); err != nil || status != "cancelled" {
		t.Fatal("creator could not cancel", status, err)
	}

}
