package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestNativeNoteActionOperationBoundary(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	source := Source{Name: "native", Sender: "sender", ChatGUID: "chat", ChatID: 1}
	if err = s.Initialize(ctx, source, 0, ""); err != nil {
		t.Fatal(err)
	}
	if err = s.ConfigureNotes(ctx, source, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`INSERT INTO jobs(id,source,guid,prompt,created_at,state) VALUES(1,?,'fixture','fixture',0,'running')`, source.key()); err != nil {
		t.Fatal(err)
	}
	if err = s.StartNativeNoteAction(ctx, source, 1, "unclaimed", "add_note_item", "note"); !errors.Is(err, ErrUncertain) {
		t.Fatal(err)
	}
	if _, _, err = s.ClaimToolOperation(ctx, source, 1, "first", "{}"); err != nil {
		t.Fatal(err)
	}
	other := source
	other.ChatID = 2
	if err = s.StartNativeNoteAction(ctx, other, 1, "first", "add_note_item", "note"); !errors.Is(err, ErrUncertain) {
		t.Fatal(err)
	}
	if err = s.StartNativeNoteAction(ctx, source, 1, "first", "add_note_item", "note"); err != nil {
		t.Fatal(err)
	}
	if err = s.FinishNoteAction(ctx, source, 1, "failed", "safe rejection"); err != nil {
		t.Fatal(err)
	}
	if err = s.StartNativeNoteAction(ctx, source, 1, "first", "add_note_item", "note"); !errors.Is(err, ErrUncertain) {
		t.Fatalf("same failed operation reused: %v", err)
	}
	if err = s.StartNoteAction(ctx, source, 1, "add_note_item", "note"); err == nil {
		t.Fatal("legacy API reused failed action")
	}
	if err = s.CompleteToolOperation(ctx, source, 1, "first", "safe failure"); err != nil {
		t.Fatal(err)
	}
	claimed, cached, err := s.ClaimToolOperation(ctx, source, 1, "first", "{}")
	if err != nil || claimed || cached != "safe failure" {
		t.Fatalf("cached=%q err=%v", cached, err)
	}
	if _, _, err = s.ClaimToolOperation(ctx, source, 1, "second", "{}"); err != nil {
		t.Fatal(err)
	}
	if err = s.StartNativeNoteAction(ctx, source, 1, "second", "add_note_item", "note"); err != nil {
		t.Fatal(err)
	}
	if err = s.FinishNoteAction(ctx, source, 1, "completed", "added"); err != nil {
		t.Fatal(err)
	}
	if err = s.StartNativeNoteAction(ctx, source, 1, "second", "add_note_item", "note"); err != nil {
		t.Fatalf("next batch item: %v", err)
	}
}
