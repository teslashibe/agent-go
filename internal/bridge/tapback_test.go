package bridge

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/teslashibe/agent-go/internal/store"
)

func TestReactToolAttachesInboundTapback(t *testing.T) {
	b, s, _, messenger := familyNotesBridge(t)
	b.notes = &fakeNotes{}
	intake(t, b, noteMessage(b, 80, "add Theragun"), "turn")
	job, _, err := s.ClaimNext(context.Background(), b.config.Source, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	turn := newNotesTurn(context.Background(), b, job)
	got := rpc(t, turn, "react", `{"operation_id":"ack","reaction":"love"}`)
	if strings.Contains(got, `"isError":true`) || !strings.Contains(got, "Tapback request accepted: love") {
		t.Fatal(got)
	}
	if !strings.EqualFold(strings.Join(messenger.reactions, ","), "love:"+job.GUID) {
		t.Fatalf("reactions=%q guid=%s", messenger.reactions, job.GUID)
	}
	replay := rpc(t, turn, "react", `{"operation_id":"ack","reaction":"love"}`)
	if strings.Contains(replay, `"isError":true`) || len(messenger.reactions) != 1 {
		t.Fatalf("replay=%s reactions=%q", replay, messenger.reactions)
	}
	if got := rpc(t, turn, "react", `{"operation_id":"bad","reaction":"thumbs-up"}`); !strings.Contains(got, "unsupported tapback") {
		t.Fatal(got)
	}
}

func TestReactToolListedWithoutNotesActionsWhenNotesDisabled(t *testing.T) {
	s, b, _, _, _ := setup(t)
	ctx := context.Background()
	if _, err := s.Accept(ctx, b.config.Source, store.Event{ID: 9, GUID: "inbound-9", Sender: source.Sender, Text: "hi", CreatedAt: time.Now()}, "turn"); err != nil {
		t.Fatal(err)
	}
	job, _, err := s.ClaimNext(ctx, b.config.Source, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	turn := newNotesTurn(ctx, b, job)
	names := make([]string, 0, len(turn.tools()))
	for _, tool := range turn.tools() {
		names = append(names, tool["name"].(string))
	}
	if strings.Join(names, ",") != "react" {
		t.Fatalf("tools=%q", names)
	}
}

func TestTapbackOnlySendsNoChatText(t *testing.T) {
	b, _, _, messenger := familyNotesBridge(t)
	b.notes = &fakeNotes{}
	runner := &nativeTestRunner{runNative: func(_ context.Context, h http.Handler) (Result, error) {
		got := rpc(t, h, "react", `{"operation_id":"ack","reaction":"like"}`)
		if strings.Contains(got, `"isError":true`) {
			t.Fatal(got)
		}
		return Result{SessionID: "tapback-session", Text: actionText(store.Action{Action: "none"})}, nil
	}}
	b.runner = runner
	intake(t, b, noteMessage(b, 81, "ok"), "turn")
	drain(t, b)
	if len(messenger.texts) != 0 {
		t.Fatalf("unexpected text %q", messenger.texts)
	}
	if len(messenger.reactions) != 1 || !strings.HasPrefix(messenger.reactions[0], "like:") {
		t.Fatalf("reactions=%q", messenger.reactions)
	}
}

func TestTextReplySkipsTapback(t *testing.T) {
	b, _, _, messenger := familyNotesBridge(t)
	b.notes = &fakeNotes{}
	runner := &nativeTestRunner{runNative: func(context.Context, http.Handler) (Result, error) {
		return Result{SessionID: "text-session", Text: actionText(store.Action{Action: "none", Reply: "Theragun is already on the list."})}, nil
	}}
	b.runner = runner
	intake(t, b, noteMessage(b, 82, "is theragun on the list"), "turn")
	drain(t, b)
	if len(messenger.reactions) != 0 {
		t.Fatalf("reacted: %q", messenger.reactions)
	}
	if len(messenger.texts) != 1 || messenger.texts[0] != "Theragun is already on the list." {
		t.Fatalf("replies=%q", messenger.texts)
	}
}

func TestAcknowledgementSkipsFinalStructuredTapback(t *testing.T) {
	b, _, r, messenger := familyNotesBridge(t)
	b.config.Acknowledge = true
	b.notes = &fakeNotes{}
	r.reaction = "like"
	b.runner = r
	r.run = func(context.Context, string, string) (Result, error) {
		return Result{SessionID: "ack-then-work", Text: actionText(store.Action{Action: "none", Reaction: "laugh", Reply: "On it."})}, nil
	}
	intake(t, b, noteMessage(b, 91, "thanks"), "turn")
	drain(t, b)
	if len(messenger.reactions) != 1 || !strings.HasPrefix(messenger.reactions[0], "like:") {
		t.Fatalf("expected one early like, got %q", messenger.reactions)
	}
	if len(messenger.texts) != 1 || messenger.texts[0] != "On it." {
		t.Fatalf("replies=%q", messenger.texts)
	}
}

func TestReactToolSkipsAfterEarlyAck(t *testing.T) {
	b, s, _, messenger := familyNotesBridge(t)
	b.notes = &fakeNotes{}
	intake(t, b, noteMessage(b, 92, "add Theragun"), "turn")
	job, _, err := s.ClaimNext(context.Background(), b.config.Source, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	turn := newNotesTurn(context.Background(), b, job)
	if err := s.SaveAcknowledgementChoice(context.Background(), b.config.Source, job.ID, "like"); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishAcknowledgement(context.Background(), b.config.Source, job.ID, "accepted"); err != nil {
		t.Fatal(err)
	}
	got := rpc(t, turn, "react", `{"operation_id":"late","reaction":"love"}`)
	if !strings.Contains(got, `"isError":true`) || !strings.Contains(got, "already attempted") {
		t.Fatal(got)
	}
	if len(messenger.reactions) != 0 {
		t.Fatalf("late tapback fired: %q", messenger.reactions)
	}
}

func TestStaleFinalReactionHasNoEffect(t *testing.T) {
	b, _, r, messenger := familyNotesBridge(t)
	r.run = func(context.Context, string, string) (Result, error) {
		return Result{Text: `{"reply":"","reaction":"laugh"}`}, nil
	}
	intake(t, b, noteMessage(b, 93, "hello"), "turn")
	drain(t, b)
	if len(messenger.reactions) != 0 || len(messenger.texts) != 1 || !strings.Contains(messenger.texts[0], "No action was taken from that final response") {
		t.Fatalf("reactions=%v texts=%v", messenger.reactions, messenger.texts)
	}
}

func TestReactToolCanBeTapbackReplyOrBoth(t *testing.T) {
	for _, tc := range []struct {
		name, reaction, reply string
		wantText, wantReact   bool
	}{
		{"tapback-only", "laugh", "", false, true},
		{"reply-only", "", "Theragun is already on the list.", true, false},
		{"both", "love", "Added it.", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, _, _, messenger := familyNotesBridge(t)
			b.notes = &fakeNotes{}
			action := store.Action{Action: "none", Reaction: tc.reaction, Reply: tc.reply}
			b.runner = &nativeTestRunner{runNative: func(_ context.Context, h http.Handler) (Result, error) {
				if tc.reaction != "" {
					got := rpc(t, h, "react", `{"operation_id":"react","reaction":"`+tc.reaction+`"}`)
					if strings.Contains(got, `"isError":true`) {
						t.Fatal(got)
					}
				}
				return Result{SessionID: "choice-" + tc.name, Text: actionText(action)}, nil
			}}
			intake(t, b, noteMessage(b, 90, tc.name), "turn")
			drain(t, b)
			if tc.wantReact != (len(messenger.reactions) == 1 && strings.HasPrefix(messenger.reactions[0], tc.reaction+":")) {
				t.Fatalf("reactions=%q", messenger.reactions)
			}
			if tc.wantText {
				if len(messenger.texts) != 1 || messenger.texts[0] != tc.reply {
					t.Fatalf("replies=%q", messenger.texts)
				}
			} else if len(messenger.texts) != 0 {
				t.Fatalf("unexpected text %q", messenger.texts)
			}
		})
	}
}

func TestUncertainTapbackCannotUseAnotherOperationID(t *testing.T) {
	b, s, _, messenger := familyNotesBridge(t)
	messenger.react = func(string, string) (bool, error) { return false, errors.New("uncertain native result") }
	intake(t, b, noteMessage(b, 188, "fixture"), "turn")
	job, _, err := s.ClaimNext(context.Background(), b.config.Source, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	first := newNotesTurn(context.Background(), b, job)
	got := rpc(t, first, "react", `{"operation_id":"first","reaction":"like"}`)
	if !strings.Contains(got, "outcome is unknown") || len(messenger.reactions) != 1 {
		t.Fatal(got, messenger.reactions)
	}
	// Recreating the turn proves suppression does not depend on t.tapped.
	again := newNotesTurn(context.Background(), b, job)
	replay := rpc(t, again, "react", `{"operation_id":"first","reaction":"like"}`)
	if replay != got || len(messenger.reactions) != 1 {
		t.Fatal(replay, messenger.reactions)
	}
	other := rpc(t, again, "react", `{"operation_id":"second","reaction":"love"}`)
	if !strings.Contains(other, "already attempted") || len(messenger.reactions) != 1 {
		t.Fatal(other, messenger.reactions)
	}
	if err := again.close(); err != nil {
		t.Fatal("cosmetic failure paused requested work", err)
	}
}
