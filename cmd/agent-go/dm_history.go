//go:build darwin || linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/teslashibe/agent-go/internal/config"
	"github.com/teslashibe/agent-go/internal/store"
	"github.com/teslashibe/imessage"
)

// prepareChatHistory validates the exact configured chat before establishing the
// live cursor and archiving a DM startup snapshot. The caller must request
// MaxHistoryMessages+1 for DMs, and call this before subscriptions and workers.
// History is newest-first and has no paging: a capped response is not complete.
func prepareChatHistory(ctx context.Context, cfg config.Config, db *store.Store, messages []imessage.Message) error {
	source := configuredSource(cfg)
	bootstrapped := false
	if !cfg.Group {
		var err error
		bootstrapped, err = db.DMHistoryBootstrapped(ctx, source)
		if err != nil {
			return err
		}
		if !bootstrapped && len(messages) >= store.MaxHistoryMessages {
			return fmt.Errorf("%w: DM history reached the upstream %d-message cap; completeness cannot be established", store.ErrUncertain, store.MaxHistoryMessages)
		}
	}
	anchors := make([]store.Anchor, 0, len(messages))
	history := make([]store.HistoryMessage, 0, len(messages))
	seenIDs := make(map[int64]bool, len(messages))
	seenGUIDs := make(map[string]bool, len(messages))
	for _, message := range messages {
		if !validChat(message, cfg) || message.ID <= 0 || message.GUID == "" {
			return errIdentity
		}
		if !cfg.Group && ((!message.IsFromMe && !source.AllowsSender(message.Sender)) || seenIDs[message.ID] || seenGUIDs[message.GUID]) {
			return errIdentity
		}
		seenIDs[message.ID], seenGUIDs[message.GUID] = true, true
		anchors = append(anchors, store.Anchor{ID: message.ID, GUID: message.GUID})
		if !cfg.Group && !bootstrapped {
			// The pinned transport does not expose attachment bodies or raw
			// export fields; retain exactly the metadata it actually provides.
			metadata, err := json.Marshal(message)
			if err != nil {
				return err
			}
			history = append(history, store.HistoryMessage{
				ID: message.ID, GUID: message.GUID, ChatID: message.ChatID,
				ChatGUID: message.ChatGUID, IsGroup: message.IsGroup,
				Sender: message.Sender, IsFromMe: message.IsFromMe,
				CreatedAt: message.CreatedAt, Text: message.Text, Metadata: metadata,
			})
		}
	}
	if !cfg.Group && len(messages) == 0 {
		if err := db.InitializeEmptyDM(ctx, source); err != nil {
			return err
		}
	}
	if err := db.CheckHistory(ctx, source, anchors); err != nil {
		if errors.Is(err, store.ErrUninitialized) {
			return fmt.Errorf("%w: empty history has no trusted row/GUID anchor", errIdentity)
		}
		return err
	}
	if cfg.Group || bootstrapped {
		return nil
	}
	_, err := db.BootstrapDMHistory(ctx, source, history)
	if err != nil {
		return fmt.Errorf("%w: DM history bootstrap failed: %w", store.ErrUncertain, err)
	}
	return nil
}
