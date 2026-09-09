package store

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestBatchConfirmationSurvivesRestartAndIsConsumedOnce(t *testing.T) {
	s, source, path := reminderStore(t)
	ctx := context.Background()
	now := time.Now()
	if err := s.ConfigureNotes(ctx, source, nil); err != nil {
		t.Fatal(err)
	}
	c := NoteConfirmation{Sender: source.AllowedSenders[0], Kind: "delete_notes", CreatedAt: now, Targets: []NoteDeletionTarget{{NoteID: "a", Title: "A", ModifiedAt: now}, {NoteID: "b", Title: "B", ModifiedAt: now}}}
	if err := s.SetNoteConfirmation(ctx, source, c); err != nil {
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
	got, err := s.NoteConfirmation(ctx, source, c.Sender, now)
	if err != nil || len(got.Targets) != 2 {
		t.Fatal(got, err)
	}
	other := source
	other.Name = "other-chat"
	if err = s.ConsumeNoteConfirmation(ctx, other, got, now); err == nil {
		t.Fatal("cross-chat approval consumed")
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); results <- s.ConsumeNoteConfirmation(ctx, source, got, now) }()
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatal("approval was not single-use", successes)
	}
	var remaining int
	if err = s.db.QueryRow(`SELECT count(*) FROM note_confirmation_targets`).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatal("targets survived consumption", remaining, err)
	}
}

func TestBatchConfirmationReplacementCannotConsumeStaleList(t *testing.T) {
	s, source, _ := reminderStore(t)
	ctx := context.Background()
	now := time.Now()
	if err := s.ConfigureNotes(ctx, source, nil); err != nil {
		t.Fatal(err)
	}
	c := NoteConfirmation{Sender: source.AllowedSenders[0], Kind: "delete_notes", CreatedAt: now, Targets: []NoteDeletionTarget{{NoteID: "a", Title: "A", ModifiedAt: now}, {NoteID: "b", Title: "B", ModifiedAt: now}}}
	if err := s.SetNoteConfirmation(ctx, source, c); err != nil {
		t.Fatal(err)
	}
	old, err := s.NoteConfirmation(ctx, source, c.Sender, now)
	if err != nil {
		t.Fatal(err)
	}
	c.Targets[1].NoteID = "c"
	if err = s.SetNoteConfirmation(ctx, source, c); err != nil {
		t.Fatal(err)
	}
	if err = s.ConsumeNoteConfirmation(ctx, source, old, now); err == nil {
		t.Fatal("replaced snapshot accepted")
	}
	current, err := s.NoteConfirmation(ctx, source, c.Sender, now)
	if err != nil || current.Targets[1].NoteID != "c" {
		t.Fatal(current, err)
	}
	if err = s.ConsumeNoteConfirmation(ctx, source, current, now.Add(11*time.Minute)); err == nil {
		t.Fatal("expired snapshot consumed")
	}
}

func TestLegacyNoteConfirmationMigrationPreservesApproval(t *testing.T) {
	s, source, _ := reminderStore(t)
	ctx := context.Background()
	now := time.Now()
	if err := s.ConfigureNotes(ctx, source, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DROP TABLE note_confirmation_targets`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO note_confirmations(source,sender,kind,note_id,title,modified_at,created_at) VALUES(?,?,'delete_note','legacy','Legacy',?,?)`, source.key(), source.AllowedSenders[0], now.UnixNano(), now.UnixNano()); err != nil {
		t.Fatal(err)
	}
	if err := s.ConfigureNotes(ctx, source, nil); err != nil {
		t.Fatal(err)
	}
	c, err := s.NoteConfirmation(ctx, source, source.AllowedSenders[0], now)
	if err != nil || len(c.Targets) != 1 || c.Targets[0].NoteID != "legacy" {
		t.Fatal(c, err)
	}
	if err = s.ConsumeNoteConfirmation(ctx, source, c, now); err != nil {
		t.Fatal(err)
	}
}
