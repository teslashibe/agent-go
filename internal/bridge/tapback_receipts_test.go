package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/teslashibe/imessage"
)

func TestNativeReactionReceiptReachesModelWithoutReplay(t *testing.T) {
	for _, tc := range []struct {
		name     string
		verified bool
		err      error
		want     string
	}{
		{"verified", true, nil, "verified_on_sender"},
		{"accepted", false, nil, "accepted"},
		{"not started", false, fmt.Errorf("wrapped: %w", &imessage.RPCError{Data: json.RawMessage(`{"disposition":"not_started","retry_safe":true}`)}), "not_started"},
		{"unknown", false, context.DeadlineExceeded, "unknown"},
		{"contradictory", true, &imessage.RPCError{Data: json.RawMessage(`{"disposition":"not_started","retry_safe":false}`)}, "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, b, r, m, _ := setup(t)
			b.config.Acknowledge = true
			r.reaction = "love"
			m.verified = tc.verified
			m.err = tc.err
			m.send = func(string) error { return nil }
			input := message(1, "receipt regression")
			intake(t, b, input, "turn")
			drain(t, b)
			if len(r.prompts) != 1 || !strings.Contains(r.prompts[0], "Application acknowledgement outcome: "+tc.want+".") {
				t.Fatal(r.prompts)
			}
			got, err := s.AcknowledgementOutcome(context.Background(), b.config.Source, 1)
			if err != nil || got != tc.want {
				t.Fatal(got, err)
			}
			intake(t, b, input, "turn")
			drain(t, b)
			if len(m.reactions) != 1 || len(r.prompts) != 1 || strings.Join(m.texts, "") != "reply" {
				t.Fatal("receipt blocked work or repeated a dispatch")
			}
		})
	}
}

func TestReactToolKeepsReceiptAcrossReplay(t *testing.T) {
	for _, verified := range []bool{true, false} {
		b, s, _, m := familyNotesBridge(t)
		b.notes = &fakeNotes{}
		m.verified = verified
		expected := "Tapback verified on sender: love"
		if !verified {
			m.err = &imessage.RPCError{Data: json.RawMessage(`{"disposition":"not_started","retry_safe":true}`)}
			expected = "Tapback not started:"
		}
		intake(t, b, noteMessage(b, 81, "receipt regression"), "turn")
		job, _, err := s.ClaimNext(context.Background(), b.config.Source, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		turn := newNotesTurn(context.Background(), b, job)
		got := rpc(t, turn, "react", `{"operation_id":"receipt","reaction":"love"}`)
		// Recreate the per-turn adapter: the result must come from durable evidence.
		replay := newNotesTurn(context.Background(), b, job)
		again := rpc(t, replay, "react", `{"operation_id":"receipt","reaction":"love"}`)
		if !strings.Contains(got, expected) || again != got || replay.attachedTapback() != verified || len(m.reactions) != 1 {
			t.Fatal("lost or replayed receipt", got, again)
		}
		if next := rpc(t, replay, "react", `{"operation_id":"another","reaction":"love"}`); !strings.Contains(next, "already attempted") || len(m.reactions) != 1 {
			t.Fatal(next)
		}
	}
}

func TestChooserFailureLogsOnlyBoundedCategory(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	defer slog.SetDefault(previous)
	_, b, r, _, _ := setup(t)
	b.config.Acknowledge = true
	r.chooseErr = errors.New("private fixture chooser detail")
	intake(t, b, message(1, "private fixture message"), "turn")
	drain(t, b)
	logs := output.String()
	if !strings.Contains(logs, "reaction chooser fallback") || strings.Contains(logs, "private fixture") {
		t.Fatal("chooser fallback missing or private detail leaked")
	}
}
