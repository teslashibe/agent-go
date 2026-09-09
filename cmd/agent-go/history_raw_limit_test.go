//go:build darwin || linux

package main

import (
	"strings"
	"testing"

	"github.com/teslashibe/agent-go/internal/config"
	"github.com/teslashibe/agent-go/internal/store"
)

func TestHistoryExportCountsRawCRLFBytes(t *testing.T) {
	cfg := config.Config{ChatID: 1, ChatGUID: "g", Group: true}
	first := `{"id":1,"guid":"one","chat_id":1,"chat_guid":"g","is_group":true}` + "\r\n"
	second := `{"id":2,"guid":"two","chat_id":1,"chat_guid":"g","is_group":true}` + "\r\n"
	prefix := first + strings.Repeat("\r\n", (store.MaxRequestBytes-len(first))/2)
	if len(prefix) < store.MaxRequestBytes {
		prefix += "\n"
	}
	if _, err := readHistoryExport(strings.NewReader(prefix+second), cfg); err == nil || !strings.Contains(err.Error(), "512 KiB") {
		t.Fatalf("oversized CRLF input must not import a prefix: %v", err)
	}
	messages, err := readHistoryExport(strings.NewReader(first+"\r\n"+second), cfg)
	if err != nil || len(messages) != 2 {
		t.Fatalf("valid CRLF input: messages=%d err=%v", len(messages), err)
	}
}
