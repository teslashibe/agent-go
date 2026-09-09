//go:build darwin || linux

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"unicode/utf8"

	"github.com/teslashibe/agent-go/internal/config"
	"github.com/teslashibe/agent-go/internal/store"
)

// readHistoryExport accepts one complete imsg message object per line. The
// operator must export all history, requesting 10001 records and checking the
// upstream did not cap its response. A full 10000-row window is ambiguous and
// is rejected rather than represented as a complete conversation.
func readHistoryExport(r io.Reader, cfg config.Config) ([]store.HistoryMessage, error) {
	data, err := io.ReadAll(io.LimitReader(r, store.MaxRequestBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read history export: %w", err)
	}
	if len(data) > store.MaxRequestBytes {
		return nil, errors.New("history export exceeds 512 KiB; no import performed")
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), store.MaxRequestBytes+1)
	messages := []store.HistoryMessage{}
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		if !utf8.Valid(line) {
			return nil, errors.New("history export contains invalid UTF-8; refusing lossy import")
		}
		var message store.HistoryMessage
		if err := json.Unmarshal(line, &message); err != nil {
			return nil, fmt.Errorf("history JSONL message %d: %w", len(messages)+1, err)
		}
		if message.ChatID != cfg.ChatID || message.ChatGUID != cfg.ChatGUID || message.IsGroup != cfg.Group {
			return nil, errIdentity
		}
		// Preserve unknown upstream fields (including available transcripts) instead
		// of narrowing them through the pinned imessage SDK's older Message type.
		message.Metadata = append(json.RawMessage(nil), line...)
		messages = append(messages, message)
		if len(messages) >= store.MaxHistoryMessages {
			return nil, errors.New("history export reached the upstream 10000-message cap; completeness cannot be established")
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(messages) == 0 {
		return nil, errors.New("history export is empty")
	}
	return messages, nil
}

// runHistoryCommand does no messaging I/O: the supplied JSONL is explicitly
// asserted by the operator to be the full export of the configured chat.
func runHistoryCommand(ctx context.Context, cfg config.Config, path string) error {
	lock, err := lockState(cfg.StatePath)
	if err != nil {
		return err
	}
	defer lock.Close()
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	messages, err := readHistoryExport(file, cfg)
	if err != nil {
		return err
	}
	if _, err = os.Stat(cfg.StatePath); err != nil {
		return err
	}
	db, err := store.OpenForHistoryImport(cfg.StatePath)
	if err != nil {
		return err
	}
	defer db.Close()
	count, err := db.ImportHistory(ctx, configuredSource(cfg), messages)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(struct {
		Imported     int  `json:"imported"`
		Supplied     int  `json:"supplied"`
		SessionReset bool `json:"session_reset"`
	}{count, len(messages), count > 0})
}
