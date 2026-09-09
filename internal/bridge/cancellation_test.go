package bridge

import (
	"context"
	"errors"
	"testing"
)

func TestCancellationStillPersistsUncertainty(t *testing.T) {
	_, b, r, _, _ := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	r.run = func(turnCtx context.Context, _, _ string) (Result, error) {
		cancel()
		return Result{}, turnCtx.Err()
	}
	intake(t, b, message(1, "work"), "turn")
	if _, err := b.ProcessNext(ctx); !errors.Is(err, ErrUncertain) || !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	status, err := b.Status(context.Background())
	if err != nil || !status.Paused || status.UnknownTurns != 1 || status.Running != 0 {
		t.Fatalf("%+v %v", status, err)
	}
}
