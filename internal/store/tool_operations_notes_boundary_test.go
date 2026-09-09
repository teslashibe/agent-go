package store

import (
	"context"
	"errors"
	"testing"
)

// Documents the safe current boundary pending an operation-aware bridge/store
// API: legacy callers cannot reuse a failed action, even during a native claim.
func TestFailedNoteActionRequiresOperationAwareStart(t *testing.T) {
	s, source, _ := reminderStore(t)
	ctx := context.Background()
	if err := s.ConfigureNotes(ctx, source, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO jobs(id,source,guid,prompt,created_at,state) VALUES(99,?,'failed-native','test',0,'running')`, source.key()); err != nil {
		t.Fatal(err)
	}
	if claimed, _, err := s.ClaimToolOperation(ctx, source, 99, "failed", "{}"); err != nil || !claimed {
		t.Fatal(claimed, err)
	}
	if err := s.StartNoteAction(ctx, source, 99, "add_note_item", "note"); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishNoteAction(ctx, source, 99, "failed", "definite failure"); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteToolOperation(ctx, source, 99, "failed", "tool_error:definite failure"); err != nil {
		t.Fatal(err)
	}
	if claimed, cached, err := s.ClaimToolOperation(ctx, source, 99, "failed", "{}"); err != nil || claimed || cached != "tool_error:definite failure" {
		t.Fatal(claimed, cached, err)
	}
	if claimed, _, err := s.ClaimToolOperation(ctx, source, 99, "independent", "{}"); err != nil || !claimed {
		t.Fatal(claimed, err)
	}
	if err := s.StartNoteAction(ctx, source, 99, "add_note_item", "other-note"); err == nil {
		t.Fatal("legacy failed-action reuse must remain denied")
	}
	if claimed, _, err := s.ClaimToolOperation(ctx, source, 99, "after-unknown", "{}"); claimed || !errors.Is(err, ErrUncertain) {
		t.Fatal(claimed, err)
	}
}
