package main

import (
	"context"
	"encoding/json"
	"github.com/teslashibe/agent-go/internal/config"
	"github.com/teslashibe/agent-go/internal/store"
	"os"
)

func runNoteRecovery(ctx context.Context, cfg config.Config, id int64, operation string, resolve bool, fingerprint, reason string, abandonUnknownAck bool) error {
	lock, err := lockState(cfg.StatePath)
	if err != nil {
		return err
	}
	defer lock.Close()
	if _, err := os.Stat(cfg.StatePath); err != nil {
		return err
	}
	db, err := store.OpenForHistoryImport(cfg.StatePath)
	if err != nil {
		return err
	}
	defer db.Close()
	if resolve {
		return db.ResolveReviewedNoteAttempt(ctx, configuredSource(cfg), id, operation, fingerprint, reason, abandonUnknownAck)
	}
	review, err := db.ReviewNoteAttempt(ctx, configuredSource(cfg), id, operation)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(review)
}

func runSchemaRecovery(ctx context.Context, cfg config.Config, id int64, reason, transcriptPath string) error {
	lock, err := lockState(cfg.StatePath)
	if err != nil {
		return err
	}
	defer lock.Close()
	if _, err = os.Stat(cfg.StatePath); err != nil {
		return err
	}
	transcript, err := os.ReadFile(transcriptPath)
	if err != nil {
		return err
	}
	db, err := store.OpenForHistoryImport(cfg.StatePath)
	if err != nil {
		return err
	}
	defer db.Close()
	return db.RetrySchemaRejectedAttempt(ctx, configuredSource(cfg), id, reason, transcript)
}

func runBindingRecovery(ctx context.Context, cfg config.Config, id int64, reason string) error {
	lock, err := lockState(cfg.StatePath)
	if err != nil {
		return err
	}
	defer lock.Close()
	if _, err := os.Stat(cfg.StatePath); err != nil {
		return err
	}
	db, err := store.OpenForHistoryImport(cfg.StatePath)
	if err != nil {
		return err
	}
	defer db.Close()
	return db.RetryBindingRejectedAttempt(ctx, configuredSource(cfg), id, reason)
}

func runInterruptedRecovery(ctx context.Context, cfg config.Config, id int64, reason string) error {
	lock, err := lockState(cfg.StatePath)
	if err != nil {
		return err
	}
	defer lock.Close()
	if _, err := os.Stat(cfg.StatePath); err != nil {
		return err
	}
	db, err := store.OpenForHistoryImport(cfg.StatePath)
	if err != nil {
		return err
	}
	defer db.Close()
	return db.ResolveInterruptedAttempt(ctx, configuredSource(cfg), id, reason)
}
