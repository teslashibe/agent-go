//go:build darwin || linux

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/teslashibe/agent-go/internal/store"

	"github.com/teslashibe/agent-go/internal/config"
	"github.com/teslashibe/imessage"
	_ "modernc.org/sqlite"
)

func TestSafeErrorClass(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"uncertain", store.ErrUncertain, "uncertain"},
		{"wrapped uncertain", fmt.Errorf("private detail: %w", store.ErrUncertain), "uncertain"},
		{"joined uncertain and cancellation", errors.Join(context.Canceled, store.ErrUncertain), "uncertain"},
		{"identity", fmt.Errorf("private identity: %w", errIdentity), "identity"},
		{"joined identity and cancellation", errors.Join(context.Canceled, errIdentity), "identity"},
		{"canceled", context.Canceled, "canceled"},
		{"timeout", context.DeadlineExceeded, "timeout"},
		{"unknown private error", errors.New("secret token and message contents"), "transport"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := safeErrorClass(tc.err); got != tc.want {
				t.Fatalf("error class = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestValidGroupChat(t *testing.T) {
	cfg := config.Config{ChatID: 1, ChatGUID: "any;+;configured-group", Group: true}
	for _, tc := range []struct {
		name   string
		mutate func(*imessage.Message)
		valid  bool
	}{
		{"exact group", func(m *imessage.Message) {}, true},
		{"wrong group flag", func(m *imessage.Message) { m.IsGroup = false }, false},
		{"other group", func(m *imessage.Message) { m.ChatGUID = "any;+;other-group" }, false},
		{"other row", func(m *imessage.Message) { m.ChatID = 2 }, false},
		{"direct", func(m *imessage.Message) { m.ChatGUID = "iMessage;-;first@example.com"; m.IsGroup = false }, false},
		{"missing ID", func(m *imessage.Message) { m.ID = 0 }, false},
		{"missing GUID", func(m *imessage.Message) { m.GUID = "" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := imessage.Message{ID: 5, GUID: "message-guid", ChatID: cfg.ChatID, ChatGUID: cfg.ChatGUID, IsGroup: true}
			tc.mutate(&m)
			if validChat(m, cfg) != tc.valid {
				t.Fatalf("validChat = %t, want %t", validChat(m, cfg), tc.valid)
			}
		})
	}
}

func TestValidChat(t *testing.T) {
	cfg := config.Config{ChatID: 1, ChatGUID: "iMessage;-;owner@example.com"}
	m := imessage.Message{ID: 5, GUID: "message-guid", ChatID: 1, ChatGUID: cfg.ChatGUID}
	if !validChat(m, cfg) {
		t.Fatal("rejected direct chat")
	}
	anyCfg := config.Config{ChatID: 2, ChatGUID: "any;-;+15555501001"}
	anyMsg := imessage.Message{ID: 5, GUID: "message-guid", ChatID: 2, ChatGUID: anyCfg.ChatGUID}
	if !validChat(anyMsg, anyCfg) {
		t.Fatal("rejected any;-; direct chat")
	}
	m.IsGroup = true
	if validChat(m, cfg) {
		t.Fatal("accepted group chat")
	}
	m.IsGroup = false
	m.ChatGUID = "iMessage;+;group"
	if validChat(m, cfg) {
		t.Fatal("accepted wrong chat")
	}
	m.ChatGUID = cfg.ChatGUID
	m.ID = 0
	if validChat(m, cfg) {
		t.Fatal("accepted missing row ID")
	}
}

func TestIsolateCodingHome(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state.db")
	family := filepath.Join(root, "family-home")
	if err := os.MkdirAll(family, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(family, "auth.json"), []byte(`{"ok":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(family, "config.toml"), []byte("family"), 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "codex")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s' \"$CODEX_HOME\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	wrapped, err := isolateCodingHome(state, "coding", family, binary)
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(wrapped).Output()
	if err != nil {
		t.Fatal(err)
	}
	home := codingHome(state, "coding")
	if string(out) != home {
		t.Fatalf("CODEX_HOME=%q want %q", out, home)
	}
	if _, err := os.Stat(filepath.Join(home, "config.toml")); !os.IsNotExist(err) {
		t.Fatal("must not copy family config.toml")
	}
	auth, err := os.ReadFile(filepath.Join(home, "auth.json"))
	if err != nil || string(auth) != `{"ok":true}` {
		t.Fatalf("auth %s %v", auth, err)
	}
}

func TestInteractiveSessionPreservesMissingIdentity(t *testing.T) {
	home := t.TempDir()
	id := "01a079ef-0ead-7c62-8d80-3af4015793cd"
	if err := requireInteractiveSession(home, id); err == nil {
		t.Fatal("missing session accepted")
	}
	dir := filepath.Join(home, "sessions", "2026", "09")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := requireInteractiveSession(home, id); err == nil {
		t.Fatal("empty session directory accepted")
	}
	wrong := filepath.Join(dir, "rollout-"+id+"-another.jsonl")
	if err := os.WriteFile(wrong, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := requireInteractiveSession(home, id); err == nil {
		t.Fatal("substring match accepted")
	}
	file := filepath.Join(dir, "rollout-2026-09-06T20-35-05-"+id+".jsonl")
	if err := os.WriteFile(file, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := requireInteractiveSession(home, id); err != nil {
		t.Fatal(err)
	}
	if err := requireInteractiveSession(home, id+"/../escape"); err == nil {
		t.Fatal("invalid ID accepted")
	}
	if err := requireInteractiveSession(home, ""); err != nil {
		t.Fatal("explicit new session rejected", err)
	}
}
