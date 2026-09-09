package bridge

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/teslashibe/agent-go/internal/store"
	"github.com/teslashibe/codex"
)

type approvalTestRunner struct {
	run func(context.Context, codex.ApprovalHandler, http.Handler) (Result, error)
}

func (r *approvalTestRunner) Run(context.Context, string, string) (Result, error) {
	return Result{}, errors.New("exec fallback used")
}
func (r *approvalTestRunner) RunWithApprovals(ctx context.Context, _, _ string, h codex.ApprovalHandler, notes http.Handler) (Result, error) {
	return r.run(ctx, h, notes)
}

type approvalMessenger struct {
	sent chan string
	err  error
}

func (m *approvalMessenger) Send(ctx context.Context, _ int64, text string) error {
	select {
	case m.sent <- text:
		return m.err
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (*approvalMessenger) React(context.Context, int64, string, string) (bool, error) {
	return false, nil
}

func TestApproveCommandsAreOrdinaryTurns(t *testing.T) {
	_, b, _, _, _ := setup(t)
	for i, text := range []string{"/approve", "/approve deadbeefdeadbeefdeadbeefdeadbeef", "/deny token", "/cancel token"} {
		intake(t, b, message(int64(i+1), text), "turn")
	}
}

func TestInteractiveTurnDoesNotPromptForApproval(t *testing.T) {
	b, _, _, _ := familyNotesBridge(t)
	b.notes = &fakeNotes{}
	sent := &approvalMessenger{sent: make(chan string, 4)}
	b.messenger = sent
	called := make(chan codex.ApprovalHandler, 1)
	b.runner = &approvalTestRunner{run: func(ctx context.Context, h codex.ApprovalHandler, notes http.Handler) (Result, error) {
		if notes == nil {
			return Result{}, errors.New("Notes MCP lost")
		}
		called <- h
		return Result{SessionID: "thread", Text: actionText(store.Action{Action: "none", Reply: "Added walnuts."})}, nil
	}}
	msg := noteMessage(b, 100, "Add walnuts to the shopping list")
	msg.Sender = b.config.Source.AllowedSenders[0]
	intake(t, b, msg, "turn")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := b.ProcessNext(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case h := <-called:
		if h != nil {
			t.Fatal("iMessage approval callback was registered")
		}
	case <-ctx.Done():
		t.Fatal("runner not invoked")
	}
	select {
	case text := <-sent.sent:
		if strings.Contains(text, "/approve") || strings.Contains(text, "/deny") || strings.Contains(text, "/cancel") {
			t.Fatalf("approval command leaked: %s", text)
		}
	default:
	}
}
