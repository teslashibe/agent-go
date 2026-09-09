package bridge

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/teslashibe/agent-go/internal/store"
)

func TestGroupRequestsQueueWhileOneTurnRuns(t *testing.T) {
	b, _, runner, messenger := profileBridge(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan error, 1)
	calls := 0
	runner.run = func(ctx context.Context, session, prompt string) (Result, error) {
		calls++
		if calls == 1 {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return Result{}, ctx.Err()
			}
		}
		return Result{SessionID: "group-session", Text: jsonAction(store.Action{Action: "none", Reply: "Received."})}, nil
	}
	first := profileMessage(1, "queued-first", "Plan a holiday")
	second := profileMessage(2, "queued-second", "Include a vineyard stay")
	second.Sender = "partner"
	if _, err := b.Receive(ctx, first); err != nil {
		t.Fatal(err)
	}
	go func() { _, err := b.ProcessNext(ctx); finished <- err }()
	// Always release and join the worker, even if an assertion fails.
	defer func() {
		cancel()
		select {
		case <-finished:
		case <-time.After(6 * time.Second):
			t.Error("worker did not stop")
		}
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("turn did not start")
	}
	if receipt, err := b.Receive(ctx, second); err != nil || receipt.Disposition != "turn" {
		t.Fatalf("second sender could not queue: %+v, %v", receipt, err)
	}
	if _, err := b.ProcessNext(ctx); !errors.Is(err, store.ErrBusy) {
		t.Fatalf("concurrent execution was not rejected: %v", err)
	}
	status, err := b.Status(ctx)
	if err != nil || status.Running != 1 || status.Queued != 1 {
		t.Fatalf("unexpected busy state: %+v, %v", status, err)
	}
	close(release)
	select {
	case err := <-finished:
		if err != nil {
			finished <- err
			t.Fatal(err)
		}
		finished <- nil
	case <-ctx.Done():
		t.Fatal("first turn did not finish")
	}
	drain(t, b)
	if calls != 2 || len(messenger.texts) != 2 {
		t.Fatalf("calls=%d replies=%d", calls, len(messenger.texts))
	}
	if !strings.Contains(runner.prompts[0], "Plan a holiday") || !strings.Contains(runner.prompts[1], "Include a vineyard stay") {
		t.Fatal("request order lost")
	}
}

func TestClarificationDoesNotReserveGroupWorker(t *testing.T) {
	b, _, runner, messenger := profileBridge(t)
	calls := 0
	runner.run = func(context.Context, string, string) (Result, error) {
		calls++
		reply := "What time should I remind you?"
		if calls == 2 {
			reply = "I can help with the trip."
		}
		return Result{SessionID: "session", Text: jsonAction(store.Action{Action: "none", Reply: reply})}, nil
	}
	sam := profileMessage(1, "sam-question", "Remind me to pack")
	sam.Sender = "partner"
	intake(t, b, sam, "turn")
	drain(t, b)
	intake(t, b, profileMessage(2, "alex-next", "Help plan the trip"), "turn")
	drain(t, b)
	status, err := b.Status(context.Background())
	if err != nil || status.Running != 0 || status.Queued != 0 || status.Paused {
		t.Fatalf("clarification held worker: %+v, %v", status, err)
	}
	if calls != 2 || len(messenger.texts) != 2 {
		t.Fatalf("calls=%d replies=%d", calls, len(messenger.texts))
	}
}
