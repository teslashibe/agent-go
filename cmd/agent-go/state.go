//go:build darwin || linux

package main

import (
	"context"
	"encoding/json"
	"os"

	"github.com/teslashibe/agent-go/internal/config"
	"github.com/teslashibe/agent-go/internal/store"
)

// runStateCommand requires the same exclusive lock as the worker. Opening state
// performs crash recovery even for status-only requests. Discarding unresolved
// replies and resuming is explicit; uncertain work is never automatically retried.
func runStateCommand(ctx context.Context, cfg config.Config, discardRepliesAndResume bool, session string) error {
	lock, err := lockState(cfg.StatePath)
	if err != nil {
		return err
	}
	defer lock.Close()
	if _, err := os.Stat(cfg.StatePath); err != nil {
		return err
	}
	db, err := store.Open(cfg.StatePath)
	if err != nil {
		return err
	}
	defer db.Close()
	source := configuredSource(cfg)
	if discardRepliesAndResume {
		if err := db.DiscardRepliesAndResume(ctx, source, session); err != nil {
			return err
		}
	}
	status, err := db.Status(ctx, source)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(status)
}
