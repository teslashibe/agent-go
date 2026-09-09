package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/teslashibe/agent-go/internal/store"
	"github.com/teslashibe/notes"
)

type deletionNotes struct {
	fakeNotes
	catalog     map[string]notes.Note
	attempts    []string
	failID      string
	failure     error
	afterDelete func(string)
}

func newDeletionNotes() *deletionNotes {
	f := &deletionNotes{catalog: map[string]notes.Note{}}
	for _, id := range []string{"a", "b", "c"} {
		f.catalog[id] = notes.Note{ID: id, Name: "Disposable " + id, Shared: true, ModifiedAt: time.Unix(1000, 0)}
	}
	return f
}

func (f *deletionNotes) List(context.Context) ([]notes.Note, error) {
	var list []notes.Note
	for _, n := range f.catalog {
		list = append(list, n)
	}
	slices.SortFunc(list, func(a, b notes.Note) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	return list, nil
}
func (f *deletionNotes) Get(_ context.Context, id string) (notes.Note, error) {
	n, ok := f.catalog[id]
	if !ok {
		return notes.Note{}, errors.New("fixture note missing")
	}
	return n, nil
}
func (f *deletionNotes) MoveToRecentlyDeleted(_ context.Context, id string) error {
	f.attempts = append(f.attempts, id)
	if id == f.failID {
		return f.failure
	}
	delete(f.catalog, id)
	if f.afterDelete != nil {
		f.afterDelete(id)
	}
	return nil
}

func deletionTurn(t *testing.T, b *Bridge, s *store.Store, sender string) *notesTurn {
	t.Helper()
	id := seedNoteJob(t, s, b.config.Source, sender)
	return newNotesTurn(context.Background(), b, &store.Job{ID: id, Sender: sender})
}
func finishDeletionTurn(t *testing.T, s *store.Store, turn *notesTurn) {
	t.Helper()
	if err := s.CompleteTurn(context.Background(), turn.bridge.config.Source, turn.jobID, "", nil); err != nil {
		t.Fatal(err)
	}
}
func deletionCall(t *testing.T, turn *notesTurn, name, op string, ids ...string) noteOutcome {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"operation_id": op, "note_ids": ids})
	text, err := turn.call(context.Background(), name, raw)
	var out noteOutcome
	if text == "" && err != nil {
		out.fail("tool rejected", err)
		return out
	}
	if json.Unmarshal([]byte(text), &out) != nil {
		t.Fatalf("invalid result: %s %v", text, err)
	}
	return out
}

func TestBatchDeletionConfirmationAndReplay(t *testing.T) {
	b, s, _, _ := familyNotesBridge(t)
	f := newDeletionNotes()
	b.notes = f
	sender := b.config.Source.AllowedSenders[0]
	request := deletionTurn(t, b, s, sender)
	preview := deletionCall(t, request, "delete_notes", "request", "b", "a")
	if preview.Status != "confirmation_required" || len(preview.Notes) != 2 || preview.Notes[0].NoteID != "a" || len(f.attempts) != 0 {
		t.Fatal(preview, f.attempts)
	}
	if got := deletionCall(t, request, "confirm_delete_notes", "same-turn", "a", "b"); !got.IsError || len(f.attempts) != 0 {
		t.Fatal(got)
	}
	finishDeletionTurn(t, s, request)
	other := deletionTurn(t, b, s, b.config.Source.AllowedSenders[1])
	if got := deletionCall(t, other, "confirm_delete_notes", "wrong-sender", "a", "b"); !got.IsError || len(f.attempts) != 0 {
		t.Fatal(got)
	}
	finishDeletionTurn(t, s, other)
	confirm := deletionTurn(t, b, s, sender)
	got := deletionCall(t, confirm, "confirm_delete_notes", "confirm", "b", "a")
	if got.IsError || len(got.Notes) != 2 || !slices.Equal(f.attempts, []string{"a", "b"}) {
		t.Fatal(got, f.attempts)
	}
	for _, n := range got.Notes {
		if n.Status != "completed" {
			t.Fatal(got)
		}
	}
	if _, ok := f.catalog["c"]; !ok {
		t.Fatal("unselected note deleted")
	}
	duplicate := deletionCall(t, confirm, "confirm_delete_notes", "confirm", "a", "b")
	if duplicate.encoded() != got.encoded() || len(f.attempts) != 2 {
		t.Fatal("cached result changed", duplicate, f.attempts)
	}
	if got = deletionCall(t, confirm, "confirm_delete_notes", "new-operation", "a", "b"); !got.IsError || len(f.attempts) != 2 {
		t.Fatal("consumed confirmation reused", got)
	}
}

func TestBatchDeletionRejectsChangedSetBeforeAnyWrite(t *testing.T) {
	for _, mode := range []string{"title", "modified", "missing", "locked", "unshared", "subset", "superset", "expired", "single-tool"} {
		t.Run(mode, func(t *testing.T) {
			b, s, _, _ := familyNotesBridge(t)
			f := newDeletionNotes()
			b.notes = f
			sender := b.config.Source.AllowedSenders[0]
			request := deletionTurn(t, b, s, sender)
			if got := deletionCall(t, request, "delete_notes", "request", "a", "b"); got.IsError {
				t.Fatal(got)
			}
			finishDeletionTurn(t, s, request)
			n := f.catalog["b"]
			ids := []string{"a", "b"}
			switch mode {
			case "title":
				n.Name = "Renamed"
				f.catalog["b"] = n
			case "modified":
				n.ModifiedAt = n.ModifiedAt.Add(time.Second)
				f.catalog["b"] = n
			case "missing":
				delete(f.catalog, "b")
			case "locked":
				n.PasswordProtected = true
				f.catalog["b"] = n
			case "unshared":
				n.Shared = false
				f.catalog["b"] = n
			case "subset":
				ids = []string{"a"}
			case "superset":
				ids = []string{"a", "b", "c"}
			case "expired":
				c, err := s.NoteConfirmation(context.Background(), b.config.Source, sender, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				c.CreatedAt = time.Now().Add(-11 * time.Minute)
				if err = s.SetNoteConfirmation(context.Background(), b.config.Source, c); err != nil {
					t.Fatal(err)
				}
			}
			confirm := deletionTurn(t, b, s, sender)
			var got noteOutcome
			if mode == "single-tool" {
				text, _ := confirm.call(context.Background(), "confirm_delete_note", []byte(`{"operation_id":"single","note_id":"a"}`))
				if err := json.Unmarshal([]byte(text), &got); err != nil {
					t.Fatal(err)
				}
			} else {
				got = deletionCall(t, confirm, "confirm_delete_notes", "confirm", ids...)
			}
			if !got.IsError || len(f.attempts) != 0 {
				t.Fatal(mode, got, f.attempts)
			}
		})
	}
}

func TestBatchDeletionStopsAndRetainsEachOutcome(t *testing.T) {
	for _, mode := range []string{"safe-failure", "uncertain", "changed-after-first"} {
		t.Run(mode, func(t *testing.T) {
			b, s, _, _ := familyNotesBridge(t)
			f := newDeletionNotes()
			b.notes = f
			sender := b.config.Source.AllowedSenders[0]
			request := deletionTurn(t, b, s, sender)
			deletionCall(t, request, "delete_notes", "request", "a", "b", "c")
			finishDeletionTurn(t, s, request)
			f.failID = "b"
			f.failure = errors.New("fixture refused before dispatch")
			if mode == "uncertain" {
				f.failure = &notes.OperationError{Operation: "delete", Uncertain: true, Err: errors.New("fixture lost outcome")}
			}
			if mode == "changed-after-first" {
				f.afterDelete = func(id string) { n := f.catalog["b"]; n.ModifiedAt = n.ModifiedAt.Add(time.Second); f.catalog["b"] = n }
			}
			confirm := deletionTurn(t, b, s, sender)
			got := deletionCall(t, confirm, "confirm_delete_notes", "confirm", "a", "b", "c")
			if !got.IsError || len(got.Notes) != 3 || got.Notes[0].Status != "completed" || got.Notes[2].Status != "not_attempted" {
				t.Fatal(got)
			}
			wantAttempts := []string{"a", "b"}
			if mode == "changed-after-first" {
				wantAttempts = []string{"a"}
			}
			if !slices.Equal(f.attempts, wantAttempts) {
				t.Fatal(f.attempts)
			}
			if mode == "uncertain" {
				if !got.ReplayUnsafe || confirm.uncertain == nil || got.Notes[1].Status != "uncertain" {
					t.Fatal(got)
				}
			} else if got.Status != "partial" || got.ReplayUnsafe {
				t.Fatal(got)
			}
			deletionCall(t, confirm, "confirm_delete_notes", "confirm", "a", "b", "c")
			deletionCall(t, confirm, "confirm_delete_notes", "new-id", "a", "b", "c")
			if !slices.Equal(f.attempts, wantAttempts) {
				t.Fatal("deletion replayed", f.attempts)
			}
			if _, err := s.NoteConfirmation(context.Background(), b.config.Source, sender, time.Now()); err == nil {
				t.Fatal("approval survived attempted batch")
			}
		})
	}
}

func TestBatchDeletionDoesNotAdmitPrivateGroupNotes(t *testing.T) {
	b, s, _, _ := familyNotesBridge(t)
	f := newDeletionNotes()
	b.notes = f
	n := f.catalog["b"]
	n.Shared = false
	f.catalog["b"] = n
	turn := deletionTurn(t, b, s, b.config.Source.AllowedSenders[0])
	got := deletionCall(t, turn, "delete_notes", "request", "a", "b")
	if !got.IsError || len(f.attempts) != 0 {
		t.Fatal(got)
	}
	if _, err := s.NoteConfirmation(context.Background(), b.config.Source, b.config.Source.AllowedSenders[0], time.Now()); err == nil {
		t.Fatal("partial approval persisted")
	}
}

func TestRealCodexBatchDeletion(t *testing.T) {
	runner := newRealSmokeRunner(t)
	for _, tc := range []struct {
		name, reply string
		deleted     int
	}{
		{"approve", "Yes, move both of those notes to Recently Deleted.", 2},
		{"decline", "No, keep both notes. Do not delete anything.", 0},
		{"partial", "Only Disposable a, please. Keep Disposable b.", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, s, _, messenger := familyNotesBridge(t)
			f := newDeletionNotes()
			b.notes = f
			runner.t = t
			b.runner = runner
			for i, prompt := range []string{"Please delete the notes named Disposable a and Disposable b. Keep Disposable c.", tc.reply} {
				intake(t, b, noteMessage(b, int64(910+i), prompt), "turn")
				for range 2 {
					if _, err := b.ProcessNext(context.Background()); err != nil {
						t.Fatal(err)
					}
				}
				if i == 0 {
					c, err := s.NoteConfirmation(context.Background(), b.config.Source, b.config.Source.AllowedSenders[0], time.Now())
					if err != nil || c.Kind != "delete_notes" || len(c.Targets) != 2 || len(f.attempts) != 0 {
						t.Fatalf("missing exact batch review: %+v %v attempts=%v", c, err, f.attempts)
					}
				}
			}
			if len(f.attempts) != tc.deleted || len(messenger.texts) != 2 {
				t.Fatalf("attempts=%v replies=%v", f.attempts, messenger.texts)
			}
			if _, ok := f.catalog["c"]; !ok {
				t.Fatal("unselected fixture note deleted")
			}
		})
	}
}
