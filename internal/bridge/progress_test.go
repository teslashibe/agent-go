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

func toolNames(turn *notesTurn) string {
	names := make([]string, 0, len(turn.tools()))
	for _, tool := range turn.tools() {
		names = append(names, tool["name"].(string))
	}
	return strings.Join(names, ",")
}

func TestProgressToolHiddenUnlessEnabled(t *testing.T) {
	s, b, _, _, _ := setup(t)
	ctx := context.Background()
	if _, err := s.Accept(ctx, b.config.Source, store.Event{ID: 11, GUID: "inbound-11", Sender: source.Sender, Text: "hi", CreatedAt: time.Now()}, "turn"); err != nil {
		t.Fatal(err)
	}
	job, _, err := s.ClaimNext(ctx, b.config.Source, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	turn := newNotesTurn(ctx, b, job)
	if got := toolNames(turn); got != "react" {
		t.Fatalf("tools=%q", got)
	}
	if HandlerHasProgress(turn) {
		t.Fatal("progress enabled by default")
	}
	if got := rpc(t, turn, "report_progress", `{"operation_id":"step","text":"Filed the issue"}`); !strings.Contains(got, "unknown Notes tool") {
		t.Fatal(got)
	}
	b.config.ProgressUpdates = true
	if !HandlerHasProgress(turn) || toolNames(turn) != "react,report_progress" {
		t.Fatalf("tools=%q progress=%v", toolNames(turn), HandlerHasProgress(turn))
	}
}

func TestProgressToolSendsDuringCodingTurn(t *testing.T) {
	s, _, _, messenger, _ := setup(t)
	runner := &nativeTestRunner{runNative: func(_ context.Context, h http.Handler) (Result, error) {
		got := rpc(t, h, "report_progress", `{"operation_id":"issue","text":"Filed https://example.test/issues/28"}`)
		if strings.Contains(got, `"isError":true`) || !strings.Contains(got, "Sent progress update.") {
			t.Fatal(got)
		}
		replay := rpc(t, h, "report_progress", `{"operation_id":"issue","text":"Filed https://example.test/issues/28"}`)
		if strings.Contains(replay, `"isError":true`) || len(messenger.texts) != 1 {
			t.Fatalf("replay=%s texts=%q", replay, messenger.texts)
		}
		if got := rpc(t, h, "report_progress", `{"operation_id":"bad","text":"line one\nline two"}`); !strings.Contains(got, "one nonempty line") {
			t.Fatal(got)
		}
		return Result{SessionID: "progress-session", Text: actionText(store.Action{Action: "none", Reply: "PR is up."})}, nil
	}}
	b, err := New(s, runner, messenger, Config{Source: source, StructuredActions: true, ProgressUpdates: true, ChunkRunes: 2000})
	if err != nil {
		t.Fatal(err)
	}
	intake(t, b, message(40, "write a ticket then work on this"), "turn")
	drain(t, b)
	if len(messenger.texts) != 2 || messenger.texts[0] != "Filed https://example.test/issues/28" || messenger.texts[1] != "PR is up." {
		t.Fatalf("texts=%q", messenger.texts)
	}
}

func TestProgressSendFailureDoesNotPauseTurn(t *testing.T) {
	s, _, _, messenger, _ := setup(t)
	messenger.send = func(text string) error {
		if text == "Filed the issue" {
			return errors.New("imessage unavailable")
		}
		return nil
	}
	runner := &nativeTestRunner{runNative: func(_ context.Context, h http.Handler) (Result, error) {
		got := rpc(t, h, "report_progress", `{"operation_id":"issue","text":"Filed the issue"}`)
		if strings.Contains(got, `"isError":true`) || !strings.Contains(got, "Progress text was not sent.") {
			t.Fatal(got)
		}
		return Result{SessionID: "progress-fail", Text: actionText(store.Action{Action: "none", Reply: "Still working."})}, nil
	}}
	b, err := New(s, runner, messenger, Config{Source: source, StructuredActions: true, ProgressUpdates: true, ChunkRunes: 2000})
	if err != nil {
		t.Fatal(err)
	}
	intake(t, b, message(41, "keep going"), "turn")
	drain(t, b)
	if len(messenger.texts) < 1 || messenger.texts[len(messenger.texts)-1] != "Still working." {
		t.Fatalf("texts=%q", messenger.texts)
	}
}
