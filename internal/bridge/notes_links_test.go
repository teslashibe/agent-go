package bridge

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/teslashibe/agent-go/internal/store"
	"github.com/teslashibe/notes"
)

type linkFixtureNotes struct {
	fakeNotes
	shares, links int
	participants  []string
	linkErr       error
	failItem      bool
}

func (f *linkFixtureNotes) ShareWithLink(_ context.Context, id string, participants []string) (string, error) {
	f.shares++
	f.participants = slices.Clone(participants)
	return "https://www.icloud.com/notes/" + id, nil
}

func (f *linkFixtureNotes) SharedLink(_ context.Context, id string, participants []string) (string, error) {
	f.links++
	f.participants = slices.Clone(participants)
	if f.linkErr != nil {
		return "", f.linkErr
	}
	return "https://www.icloud.com/notes/" + id, nil
}

func (f *linkFixtureNotes) AddChecklistItem(ctx context.Context, id, text string) ([]notes.ChecklistItem, error) {
	if f.failItem {
		return nil, &notes.OperationError{Operation: "add_checklist_item", Err: errors.New("fixture rejected before write")}
	}
	return f.fakeNotes.AddChecklistItem(ctx, id, text)
}

func TestCreationInvitationSurvivesModelOmissionAndPartialItems(t *testing.T) {
	for _, tc := range []struct {
		name, reply string
		partial     bool
	}{
		{"omitted", "Created the list.", false},
		{"empty", "", false},
		{"included", "Open https://www.icloud.com/notes/created-id", false},
		{"partial", "The note was created but an item failed.", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, _, _, messenger := familyNotesBridge(t)
			client := &linkFixtureNotes{failItem: tc.partial}
			b.notes = client
			b.runner = &nativeTestRunner{runNative: func(_ context.Context, h http.Handler) (Result, error) {
				args := `{"operation_id":"create","title":"New List","body":"","items":["Item"]}`
				first := rpc(t, h, "create_shared_note", args)
				out := decodeOutcome(t, first)
				if out.Link != "https://www.icloud.com/notes/created-id" || out.IsError != tc.partial {
					t.Fatal(first)
				}
				if cached := rpc(t, h, "create_shared_note", args); cached != first {
					t.Fatal("cached result changed")
				}
				return Result{SessionID: "fixture", Text: actionText(store.Action{Action: "none", Reply: tc.reply})}, nil
			}}
			intake(t, b, noteMessage(b, 501, "Create a new shared checklist"), "turn")
			drain(t, b)
			reply := strings.Join(messenger.texts, "\n")
			if strings.Count(reply, "https://www.icloud.com/notes/created-id") != 1 || client.shares != 1 || client.creates != 1 || !slices.Equal(client.participants, b.config.Source.AllowedSenders) {
				t.Fatalf("invitation lost or replayed: %q shares=%d creates=%d", reply, client.shares, client.creates)
			}
		})
	}
}

func TestExistingLinksKeepSourceAndParticipants(t *testing.T) {
	for _, mode := range []string{"allowed", "outside", "forged", "participants-mismatch"} {
		t.Run(mode, func(t *testing.T) {
			b, s, _, _ := familyNotesBridge(t)
			client := &linkFixtureNotes{}
			if mode == "participants-mismatch" {
				client.linkErr = errors.New("participant mismatch")
			}
			b.notes = client
			intake(t, b, noteMessage(b, 502, "Send the existing note link"), "turn")
			job, _, err := s.ClaimNext(context.Background(), b.config.Source, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			turn := newNotesTurn(context.Background(), b, job)
			args := `{"operation_id":"link","note_id":"shopping-id"}`
			if mode == "outside" {
				args = `{"operation_id":"link","note_id":"private-id"}`
			}
			if mode == "forged" {
				args = `{"operation_id":"link","note_id":"shopping-id","participants":["attacker"]}`
			}
			out := decodeOutcome(t, rpc(t, turn, "get_note_link", args))
			if mode == "allowed" {
				if out.Link != "https://www.icloud.com/notes/shopping-id" || out.IsError || !slices.Equal(client.participants, b.config.Source.AllowedSenders) {
					t.Fatalf("%+v", out)
				}
			} else if !out.IsError || out.Link != "" {
				t.Fatalf("unauthorized link: %+v", out)
			}
			if client.creates != 0 || client.shares != 0 {
				t.Fatal("retrieval changed sharing")
			}
			if (mode == "outside" || mode == "forged") && client.links != 0 {
				t.Fatal("unauthorized call reached backend")
			}
		})
	}
}

func TestCreationInvitationsComeOnlyFromThisJobsDurableProgress(t *testing.T) {
	b, s, _, _ := familyNotesBridge(t)
	ctx := context.Background()
	job := seedNoteJob(t, s, b.config.Source, b.config.Source.AllowedSenders[0])
	if _, _, err := s.ClaimToolOperation(ctx, b.config.Source, job, "create", `{}`); err != nil {
		t.Fatal(err)
	}
	for i, raw := range []string{
		`{"creation":"completed","sharing":"verified","title":"One","link":"https://www.icloud.com/notes/one"}`,
		`{"creation":"completed","sharing":"verified","title":"Two","link":"https://www.icloud.com/notes/two"}`,
		`{"creation":"completed","sharing":"unverified","link":"https://www.icloud.com/notes/unsafe"}`,
	} {
		if err := s.RecordToolProgress(ctx, b.config.Source, job, "create", i, raw); err != nil {
			t.Fatal(err)
		}
	}
	reply, err := b.withNoteInvitations(ctx, job, "Partial result.")
	if err != nil || !strings.Contains(reply, "/notes/one") || !strings.Contains(reply, "/notes/two") || strings.Contains(reply, "/notes/unsafe") {
		t.Fatal(reply, err)
	}
	if other, err := b.withNoteInvitations(ctx, job+1, "Other job"); err != nil || other != "Other job" {
		t.Fatal("cross-job invitation", other, err)
	}
	b.config.Source.ChatID++
	if other, err := b.withNoteInvitations(ctx, job, "Other chat"); err != nil || other != "Other chat" {
		t.Fatal("cross-source invitation", other, err)
	}
}

func TestRealCodexExistingSharedLinks(t *testing.T) {
	runner := newRealSmokeRunner(t)
	b, _, _, messenger := familyNotesBridge(t)
	client := &linkFixtureNotes{}
	b.notes, b.runner = client, runner
	intake(t, b, noteMessage(b, 503, "Please send me the opening links for both existing notes, Shopping List and New List. Do not create or share anything new."), "turn")
	drain(t, b)
	reply := strings.Join(messenger.texts, "\n")
	if !strings.Contains(reply, "https://www.icloud.com/notes/shopping-id") || !strings.Contains(reply, "https://www.icloud.com/notes/created-id") || client.links != 2 || client.shares != 0 || client.creates != 0 {
		t.Fatalf("link retrieval did not meet contract: %q links=%d", reply, client.links)
	}
}
