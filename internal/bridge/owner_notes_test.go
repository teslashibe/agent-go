package bridge

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/teslashibe/agent-go/internal/store"
	"github.com/teslashibe/notes"
)

type ownerFixtureNotes struct {
	fakeNotes
	catalog   []notes.Note
	shares    int
	uncertain bool
}

func (f *ownerFixtureNotes) List(context.Context) ([]notes.Note, error) { return f.catalog, nil }
func (f *ownerFixtureNotes) Create(_ context.Context, title, body string) (notes.Note, error) {
	f.creates++
	if f.uncertain {
		return notes.Note{}, &notes.OperationError{Operation: "create", Uncertain: true, Err: errors.New("fixture uncertain creation")}
	}
	n := notes.Note{ID: "private-created", Name: title, Plaintext: body}
	f.catalog = append(f.catalog, n)
	return n, nil
}
func (f *ownerFixtureNotes) ShareWithLink(context.Context, string, []string) (string, error) {
	f.shares++
	return "https://www.icloud.com/notes/fixture", nil
}

func TestOwnerNotesPrivateCreation(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		s, _, runner, messenger, _ := setup(t)
		src := store.Source{Name: "owner", ChatID: 2, ChatGUID: "iMessage;-;owner@example.test", AllowedSenders: []string{"owner@example.test"}}
		ctx := context.Background()
		if err := s.Initialize(ctx, src, 0, "owner-generation"); err != nil {
			t.Fatal(err)
		}
		b, err := New(s, runner, messenger, Config{Source: src, StructuredActions: true})
		if err != nil {
			t.Fatal(err)
		}
		if err := b.EnableOwnerNotes(ctx, notes.Client{}); err != nil {
			t.Fatal(err)
		}
		client := &ownerFixtureNotes{uncertain: uncertain, catalog: []notes.Note{{ID: "private-existing", Name: "Private fixture"}, {ID: "protected", Name: "Locked fixture", PasswordProtected: true}}}
		b.notes = client
		intake(t, b, Message{ID: 101, GUID: "owner-fixture", Sender: src.AllowedSenders[0], ChatID: src.ChatID, ChatGUID: src.ChatGUID, Text: "create private note", CreatedAt: time.Now()}, "turn")
		job, _, err := s.ClaimNext(ctx, src, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		turn := newNotesTurn(ctx, b, job)
		listed := rpc(t, turn, "list_notes", `{"operation_id":"list"}`)
		if !strings.Contains(listed, "private-existing") || strings.Contains(listed, "protected") {
			t.Fatal(listed)
		}
		for _, tool := range turn.tools() {
			if tool["name"] == "create_shared_note" {
				t.Fatal("owner catalog exposed invitations")
			}
		}
		shared := rpc(t, turn, "create_shared_note", `{"operation_id":"share","title":"Fixture","body":"","items":[]}`)
		if !strings.Contains(shared, "not enabled") || client.creates != 0 {
			t.Fatal(shared)
		}
		args := `{"operation_id":"create","title":"Private fixture","body":"Body","items":["Task"]}`
		got := rpc(t, turn, "create_note", args)
		out := decodeOutcome(t, got)
		if out.IsError != uncertain || out.ReplayUnsafe != uncertain || client.creates != 1 || client.shares != 0 {
			t.Fatal(got)
		}
		if retry := rpc(t, turn, "create_note", args); retry != got || client.creates != 1 {
			t.Fatal(retry)
		}
		if uncertain {
			rpc(t, turn, "create_note", strings.Replace(args, `"create"`, `"retry"`, 1))
			if client.creates != 1 || client.adds != 0 {
				t.Fatal("uncertain creation replayed")
			}
		} else {
			if client.adds != 1 || out.Sharing != "not_attempted" {
				t.Fatal(got)
			}
			if _, err := s.NoteByID(ctx, src, "private-created"); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestOwnerNotesGrantDoesNotApplyToGroups(t *testing.T) {
	b, s, _, _ := familyNotesBridge(t)
	if err := b.EnableOwnerNotes(context.Background(), notes.Client{}); err == nil {
		t.Fatal("owner grant admitted group")
	}
	b.notes = &ownerFixtureNotes{catalog: []notes.Note{{ID: "private", Name: "Private fixture"}, {ID: "shared", Name: "Shared fixture", Shared: true}}}
	if err := b.refreshSharedNotes(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.NoteByID(context.Background(), b.config.Source, "private"); err == nil {
		t.Fatal("group admitted private note")
	}
	job := seedNoteJob(t, s, b.config.Source, b.config.Source.AllowedSenders[0])
	out, err := b.executeNoteAction(context.Background(), job, store.Action{Action: "create_note", Text: `{"title":"Private fixture","body":""}`}, "", "")
	if err != nil || !out.IsError {
		t.Fatal(out, err)
	}
}
