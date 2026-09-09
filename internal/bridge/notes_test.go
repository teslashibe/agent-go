package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/teslashibe/agent-go/internal/store"
	"github.com/teslashibe/notes"
	"strings"
	"testing"
	"time"
)

type fakeNotes struct{ creates, adds, edits, deletes, reads int }

func (f *fakeNotes) EditText(context.Context, string, string, string) error { return nil }

func (f *fakeNotes) List(context.Context) ([]notes.Note, error) {
	return []notes.Note{{ID: "shopping-id", Name: "Shopping List", Shared: true}, {ID: "created-id", Name: "New List", Shared: true}}, nil
}
func (f *fakeNotes) Get(_ context.Context, id string) (notes.Note, error) {
	f.reads++
	return notes.Note{ID: id, Name: "Shopping List"}, nil
}
func (f *fakeNotes) Checklist(context.Context, string) ([]notes.ChecklistItem, error) {
	return nil, nil
}
func (f *fakeNotes) Create(_ context.Context, title, body string) (notes.Note, error) {
	f.creates++
	return notes.Note{ID: "created-id", Name: title}, nil
}
func (f *fakeNotes) Share(context.Context, string, []string) error              { return nil }
func (f *fakeNotes) VerifyParticipants(context.Context, string, []string) error { return nil }
func (f *fakeNotes) AddChecklistItem(_ context.Context, _ string, text string) ([]notes.ChecklistItem, error) {
	f.adds++
	return []notes.ChecklistItem{{Text: text}}, nil
}
func (f *fakeNotes) SetChecked(_ context.Context, _ string, text string, checked bool) ([]notes.ChecklistItem, error) {
	return []notes.ChecklistItem{{Text: text, Checked: checked}}, nil
}
func (f *fakeNotes) EditChecklistItem(_ context.Context, _ string, _ string, replacement string) ([]notes.ChecklistItem, error) {
	f.edits++
	return []notes.ChecklistItem{{Text: replacement, Checked: true}}, nil
}
func (f *fakeNotes) MoveToRecentlyDeleted(context.Context, string) error { f.deletes++; return nil }
func actionText(action store.Action) string {
	if action.Action != "" && action.Action != "none" {
		data, _ := json.Marshal(action)
		return string(data)
	}
	data, _ := json.Marshal(map[string]string{"reply": action.Reply})
	return string(data)
}
func noteMessage(b *Bridge, id int64, text string) Message {
	return Message{ID: id, GUID: time.Unix(id, 0).String(), Sender: b.config.Source.AllowedSenders[0], ChatGUID: b.config.Source.ChatGUID, ChatID: b.config.Source.ChatID, IsGroup: true, Text: text, CreatedAt: time.Now()}
}
func familyNotesBridge(t *testing.T) (*Bridge, *store.Store, *fakeRunner, *fakeMessenger) {
	t.Helper()
	s, _, r, m, _ := setup(t)
	source := store.Source{Name: "notes-test", ChatID: 1, ChatGUID: "any;+;family-test", Group: true, AllowedSenders: []string{"+15555501001", "+15555501002"}}
	ctx := context.Background()
	if err := s.Initialize(ctx, source, 0, ""); err != nil {
		t.Fatal(err)
	}
	b, err := New(s, r, m, Config{Source: source, StructuredActions: true})
	if err != nil {
		t.Fatal(err)
	}
	if err = b.EnableFamilyNotes(ctx, notes.Client{}); err != nil {
		t.Fatal(err)
	}
	if err = s.ConfigureNotes(ctx, source, []store.NoteScope{{ID: "shopping-id", Title: "Shopping List"}}); err != nil {
		t.Fatal(err)
	}
	b.notes = &fakeNotes{}
	return b, s, r, m
}
func seedNoteJob(t *testing.T, s *store.Store, source store.Source, sender string) int64 {
	t.Helper()
	ctx := context.Background()
	id := time.Now().UnixNano()
	_, err := s.Accept(ctx, source, store.Event{ID: id, GUID: fmt.Sprintf("job-%d", id), Sender: sender, Text: "notes", CreatedAt: time.Now()}, "turn")
	if err != nil {
		t.Fatal(err)
	}
	job, _, err := s.ClaimNext(ctx, source, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return job.ID
}

type listingNotes struct {
	fakeNotes
	notes []notes.Note
}

func (l listingNotes) List(context.Context) ([]notes.Note, error) { return l.notes, nil }

type scriptedNotes struct {
	*fakeNotes
	note  notes.Note
	items []notes.ChecklistItem
}

func (s *scriptedNotes) Get(_ context.Context, id string) (notes.Note, error) {
	note := s.note
	note.ID = id
	return note, nil
}
func (s *scriptedNotes) Checklist(context.Context, string) ([]notes.ChecklistItem, error) {
	return s.items, nil
}
func TestSharedFamilyNotesIncludesDuplicateTitles(t *testing.T) {
	got := sharedFamilyNotes([]notes.Note{{ID: "p18", Name: "ABGT700", Shared: true}, {ID: "priv", Name: "Secrets"}, {ID: "lock", Name: "Vault", Shared: true, PasswordProtected: true}, {ID: "a", Name: "Dup", Shared: true}, {ID: "b", Name: "Dup", Shared: true}})
	if len(got) != 3 || got[0].ID != "p18" {
		t.Fatalf("discovered=%+v", got)
	}
}
func TestRefreshSharedNotesUsesHarnessIDs(t *testing.T) {
	b, s, _, _ := familyNotesBridge(t)
	ctx := context.Background()
	if err := s.ConfigureNotes(ctx, b.config.Source, []store.NoteScope{{ID: "p16", Title: "ABGT700"}}); err != nil {
		t.Fatal(err)
	}
	b.notes = &listingNotes{notes: []notes.Note{{ID: "p18", Name: "ABGT700", Shared: true}}}
	if err := b.refreshSharedNotes(ctx); err != nil {
		t.Fatal(err)
	}
	note, err := s.NoteByID(ctx, b.config.Source, "p18")
	if err != nil || note.ID != "p18" {
		t.Fatal(note, err)
	}
	if _, err = s.NoteByID(ctx, b.config.Source, "p16"); err == nil {
		t.Fatal("stale ID retained")
	}
}
func TestNotesRejectDirectChat(t *testing.T) {
	_, b, _, _, _ := setup(t)
	if err := b.EnableFamilyNotes(context.Background(), notes.Client{}); err == nil {
		t.Fatal("enabled direct chat")
	}
}
func TestEditChecklistItemUsesExactText(t *testing.T) {
	b, s, _, _ := familyNotesBridge(t)
	client := &fakeNotes{}
	b.notes = &scriptedNotes{fakeNotes: client, note: notes.Note{ID: "shopping-id", Name: "Shopping List"}, items: []notes.ChecklistItem{{Text: "Milk", Checked: true}}}
	job := seedNoteJob(t, s, b.config.Source, b.config.Source.AllowedSenders[0])
	reply, err := b.performNoteAction(context.Background(), job, store.Action{Action: "edit_note_item", NoteName: "shopping list", Text: `{"old_text":"Milk","new_text":"Oat milk"}`})
	if err != nil || client.edits != 1 || !strings.Contains(reply, "Oat milk") {
		t.Fatal(reply, client.edits, err)
	}
}
func TestDeleteRequiresSameSenderAndUnchangedVersion(t *testing.T) {
	for _, mode := range []string{"same", "sender", "modified", "title"} {
		t.Run(mode, func(t *testing.T) {
			b, s, _, _ := familyNotesBridge(t)
			client := &fakeNotes{}
			native := &scriptedNotes{fakeNotes: client, note: notes.Note{ID: "shopping-id", Name: "Shopping List", ModifiedAt: time.Now().Add(-time.Minute)}}
			b.notes = native
			sender := b.config.Source.AllowedSenders[0]
			job := seedNoteJob(t, s, b.config.Source, sender)
			reply, err := b.performNoteAction(context.Background(), job, store.Action{Action: "delete_note", NoteName: "Shopping List"})
			if err != nil || !strings.Contains(reply, "confirmation_required") || client.deletes != 0 {
				t.Fatal(reply, err)
			}
			switch mode {
			case "sender":
				if err = s.ClearNoteConfirmation(context.Background(), b.config.Source, sender); err != nil {
					t.Fatal(err)
				}
			case "modified":
				native.note.ModifiedAt = time.Now()
			case "title":
				native.note.Name = "Changed"
			}
			reply, err = b.performNoteAction(context.Background(), job, store.Action{Action: "confirm_delete_note", NoteName: "Shopping List"})
			want := 0
			if mode == "same" {
				want = 1
			}
			if err != nil || client.deletes != want {
				t.Fatal(reply, client.deletes, err)
			}
		})
	}
}
