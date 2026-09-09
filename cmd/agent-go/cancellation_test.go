package main

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestCancellationOnly(t *testing.T) {
	fatal := errors.New("schema rejected")
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"canceled", context.Canceled, true},
		{"wrapped", fmt.Errorf("worker: %w", context.Canceled), true},
		{"siblings", errors.Join(context.Canceled, context.Canceled), true},
		{"fatal", fatal, false},
		{"mixed", errors.Join(fatal, context.Canceled), false},
		{"nested", fmt.Errorf("workers: %w", errors.Join(context.Canceled, fatal)), false},
		{"deadline", context.DeadlineExceeded, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := cancellationOnly(tc.err); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}
