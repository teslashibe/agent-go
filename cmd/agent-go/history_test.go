//go:build darwin || linux

package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/teslashibe/agent-go/internal/config"
	"github.com/teslashibe/agent-go/internal/store"
)

func TestHistoryCommandRequiresExclusiveDaemonLock(t *testing.T) {
	cfg := config.Config{StatePath: filepath.Join(t.TempDir(), "state.db")}
	lock, err := lockState(cfg.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := runHistoryCommand(context.Background(), cfg, "must-not-open.jsonl"); err == nil || !strings.Contains(err.Error(), "locked") {
		t.Fatalf("history import did not fail at exclusive lock: %v", err)
	}
}

func TestReadHistoryExportPreservesTranscriptAndIdentity(t *testing.T) {
	cfg := config.Config{ChatID: 42, ChatGUID: "any;+;test", Group: true}
	text := strings.Repeat("long trip itinerary ", 300) + "last-detail"
	object := map[string]any{"id": 1, "guid": "first", "chat_id": 42, "chat_guid": cfg.ChatGUID, "is_group": true, "sender": "first@example.test", "text": text, "is_from_me": false, "created_at": "2026-09-01T12:00:00Z", "attachments": []any{map[string]any{"transcription": "voice words"}}}
	line, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	messages, err := readHistoryExport(strings.NewReader(string(line)+"\n"), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].Text != text || !strings.Contains(string(messages[0].Metadata), "voice words") {
		t.Fatal("export fidelity lost")
	}
	for _, input := range []string{"", `{`, strings.Replace(string(line), `"chat_id":42`, `"chat_id":43`, 1), strings.Repeat(string(line), 100)} {
		if _, err := readHistoryExport(strings.NewReader(input), cfg); err == nil {
			t.Fatal("invalid export accepted")
		}
	}
}

func TestReadHistoryExportRejectsFullUpstreamWindow(t *testing.T) {
	cfg := config.Config{ChatID: 1, ChatGUID: "g", Group: true}
	line := `{"chat_id":1,"chat_guid":"g","is_group":true}` + "\n"
	// Use short objects to hit the row limit before the raw-file byte limit.
	_, err := readHistoryExport(strings.NewReader(strings.Repeat(line, store.MaxHistoryMessages)), cfg)
	if err == nil || !strings.Contains(err.Error(), "completeness") {
		t.Fatalf("full window not rejected clearly: %v", err)
	}
}
