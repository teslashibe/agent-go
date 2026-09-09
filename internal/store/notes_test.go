package store

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

func TestSyncAuthorizedNotesReplacesStaleIDs(t *testing.T) {
	s, source, _ := reminderStore(t)
	ctx := context.Background()
	if err := s.ConfigureNotes(ctx, source, []NoteScope{
		{ID: "p16", Title: "ABGT700 — Vineyard Glamping Checklist"},
		{ID: "p10", Title: "Shopping List"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.SyncAuthorizedNotes(ctx, source, []NoteScope{
		{ID: "p18", Title: "ABGT700"},
		{ID: "p10", Title: "Shopping List"},
	}); err != nil {
		t.Fatal(err)
	}
	note, candidates, err := s.ResolveNote(ctx, source, "ABGT700")
	if err != nil || note.ID != "p18" || candidates != nil {
		t.Fatalf("live note = %+v %v %v", note, candidates, err)
	}
	scope, err := s.Notes(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range scope {
		if n.ID == "p16" && n.State != "deleted" {
			t.Fatalf("stale ID still live: %+v", n)
		}
	}
}

func TestNormalizeAndResolveNoteName(t *testing.T) {
	s, source, _ := reminderStore(t)
	ctx := context.Background()
	scope := []NoteScope{
		{ID: "one", Title: "Café  List"},
		{ID: "two", Title: "Café List!"},
		{ID: "deleted", Title: "Deleted", State: "deleted"},
	}
	if err := s.ConfigureNotes(ctx, source, scope); err != nil {
		t.Fatal(err)
	}
	if got := NormalizeNoteName("  CAFÉ \t LIST! "); got != "café list!" {
		t.Fatalf("NormalizeNoteName = %q", got)
	}
	if note, candidates, err := s.ResolveNote(ctx, source, "café list!"); err != nil || note.ID != "two" || candidates != nil {
		t.Fatalf("unique = %+v %v %v", note, candidates, err)
	}
	if note, candidates, err := s.ResolveNote(ctx, source, "CAFÉ LIST"); err != nil || note.ID != "one" || candidates != nil {
		t.Fatalf("case-folded = %+v %v %v", note, candidates, err)
	}
	if note, candidates, err := s.ResolveNote(ctx, source, "missing"); err != nil || note.ID != "" || len(candidates) != 2 {
		t.Fatalf("missing = %+v %v %v", note, candidates, err)
	}
}

func TestResolveNoteDoesNotInterpretGroceryPhrasing(t *testing.T) {
	s, source, _ := reminderStore(t)
	ctx := context.Background()
	if err := s.ConfigureNotes(ctx, source, []NoteScope{{ID: "p10", Title: "Shopping List"}, {ID: "p19", Title: "Farmers Market"}}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"grocery list", "Grocery", "GROCERIES"} {
		note, candidates, err := s.ResolveNote(ctx, source, name)
		if err != nil || note.ID != "" || len(candidates) != 2 {
			t.Fatal(note, candidates, err)
		}
	}
	note, err := s.NoteByID(ctx, source, "p19")
	if err != nil || note.Title != "Farmers Market" {
		t.Fatal(note, err)
	}
	other := source
	other.Name = "other"
	if _, err = s.NoteByID(ctx, other, "p19"); err != sql.ErrNoRows {
		t.Fatal("cross source ID accepted", err)
	}
	if _, err = s.NoteByID(ctx, source, "Farmers Market"); err != sql.ErrNoRows {
		t.Fatal("title accepted as ID", err)
	}
}

func TestNoteConfirmationBoundToSenderAndExpires(t *testing.T) {
	s, source, _ := reminderStore(t)
	ctx := context.Background()
	if err := s.ConfigureNotes(ctx, source, []NoteScope{{ID: "one", Title: "List"}}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	c := NoteConfirmation{Sender: source.AllowedSenders[0], Kind: "delete_note", NoteID: "one", Title: "List", ModifiedAt: now, CreatedAt: now}
	if err := s.SetNoteConfirmation(ctx, source, c); err != nil {
		t.Fatal(err)
	}
	if _, err := s.NoteConfirmation(ctx, source, source.AllowedSenders[1], now); err != sql.ErrNoRows {
		t.Fatalf("other sender confirmation err = %v", err)
	}
	got, err := s.NoteConfirmation(ctx, source, source.AllowedSenders[0], now.Add(9*time.Minute))
	if err != nil || got.NoteID != "one" {
		t.Fatalf("confirmation = %+v, %v", got, err)
	}
	if _, err := s.NoteConfirmation(ctx, source, source.AllowedSenders[0], now.Add(11*time.Minute)); err != sql.ErrNoRows {
		t.Fatalf("expired confirmation err = %v", err)
	}
}

func TestNoteCreationSurvivesSharingFailure(t *testing.T) {
	s, source, _ := reminderStore(t)
	ctx := context.Background()
	if err := s.ConfigureNotes(ctx, source, []NoteScope{{ID: "seed", Title: "Shopping List"}}); err != nil {
		t.Fatal(err)
	}
	job := actionJob(t, s, source, source.AllowedSenders[0])
	if err := s.StartNoteAction(ctx, source, job.ID, "create_shared_note", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.StartNoteAction(ctx, source, job.ID, "create_shared_note", ""); err == nil {
		t.Fatal("external action replay accepted")
	}
	if err := s.RecordCreatedNote(ctx, source, job.ID, "created-external-id", "Disposable test"); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishNoteAction(ctx, source, job.ID, "sharing_failed", "native dialog blocked"); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkTurnUnknown(ctx, source, job.ID, ErrUncertain); err != nil {
		t.Fatal(err)
	}
	scope, err := s.Notes(ctx, source)
	if err != nil || len(scope) != 2 {
		t.Fatal(scope, err)
	}
	found := false
	for _, note := range scope {
		if note.ID == "created-external-id" {
			found = true
			if note.State != "created_unshared" {
				t.Fatal(note)
			}
		}
	}
	if !found {
		t.Fatal("lost created note after sharing failure")
	}
	other := source
	other.Name = "different-chat"
	scope, err = s.Notes(ctx, other)
	if err != nil || len(scope) != 0 {
		t.Fatal("leaked note scope", scope, err)
	}
	var state, id string
	if err := s.db.QueryRow(`SELECT state,note_id FROM note_actions WHERE job_id=?`, job.ID).Scan(&state, &id); err != nil || state != "sharing_failed" || id != "created-external-id" {
		t.Fatal(state, id, err)
	}
}
