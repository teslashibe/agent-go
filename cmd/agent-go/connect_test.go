//go:build darwin || linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/teslashibe/agent-go/internal/config"
	"github.com/teslashibe/agent-go/internal/store"
	"github.com/teslashibe/codex"
	"github.com/teslashibe/imessage"
)

// The test executable doubles as fake OpenSSH and local imsg processes, so the
// real adapters' process, stream, intake and control-response paths need no Mac.
func init() {
	if os.Getenv("AGENT_TEST_SSH") != "1" {
		return
	}
	if name := filepath.Base(os.Args[0]); name != "ssh" && name != "imsg" {
		return
	}
	if filepath.Base(os.Args[0]) == "imsg" && (len(os.Args) != 2 || os.Args[1] != "rpc") {
		os.Exit(8)
	}
	if os.Getenv("AGENT_TEST_MULTI") != "" {
		fakeMultiChatProcess()
		os.Exit(0)
	}
	decoder := json.NewDecoder(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for {
		var req struct {
			ID     string          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := decoder.Decode(&req); err != nil {
			os.Exit(0)
		}
		result := any(map[string]any{})
		switch req.Method {
		case "messages.history":
			result = map[string]any{"messages": []any{map[string]any{"id": 1, "guid": "baseline", "chat_id": 42, "chat_guid": "iMessage;-;owner@example.com", "sender": "owner@example.com", "text": "old", "created_at": time.Now().UTC()}}}
			if history := os.Getenv("AGENT_TEST_HISTORY"); history != "" {
				result = map[string]any{"messages": json.RawMessage(history)}
			}
		case "watch.subscribe":
			if marker := os.Getenv("AGENT_TEST_SUBSCRIBED"); marker != "" {
				if err := os.WriteFile(marker, []byte("subscribed"), 0600); err != nil {
					os.Exit(7)
				}
			}
			result = map[string]any{"subscription": 1}
		case "send":
			var p struct {
				Text string `json:"text"`
			}
			if json.Unmarshal(req.Params, &p) != nil {
				os.Exit(2)
			}
			if p.Text == "" {
				os.Exit(3)
			}
			if err := os.WriteFile(os.Getenv("AGENT_TEST_REPLY"), []byte(p.Text), 0600); err != nil {
				os.Exit(4)
			}
			result = map[string]any{"ok": true}
		}
		if encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result}) != nil {
			os.Exit(5)
		}
		if req.Method == "watch.subscribe" {
			note := map[string]any{"jsonrpc": "2.0", "method": "message", "params": map[string]any{"subscription": 1, "message": map[string]any{"id": 2, "guid": "status-guid", "chat_id": 42, "chat_guid": "iMessage;-;owner@example.com", "sender": "owner@example.com", "text": "/status", "created_at": time.Now().UTC()}}}
			if message := os.Getenv("AGENT_TEST_MESSAGE"); message != "" {
				note["params"] = map[string]any{"subscription": 1, "message": json.RawMessage(message)}
			}
			if encoder.Encode(note) != nil {
				os.Exit(6)
			}
		}
	}
}

func TestConnectRejectsChangedHistoryBeforeWorker(t *testing.T) {
	for _, higher := range []bool{false, true} {
		t.Run(fmt.Sprint(higher), func(t *testing.T) {
			dir := t.TempDir()
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(executable, filepath.Join(dir, "ssh")); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("AGENT_TEST_SSH", "1")
			reply := filepath.Join(dir, "reply.txt")
			subscribed := filepath.Join(dir, "subscribed")
			t.Setenv("AGENT_TEST_REPLY", reply)
			t.Setenv("AGENT_TEST_SUBSCRIBED", subscribed)
			cfg := config.Config{Host: "fake", ImsgPath: "/usr/bin/imsg", ChatID: 42, ChatGUID: "iMessage;-;owner@example.com", Owner: "owner@example.com", Source: "fake-mac"}
			source := store.Source{Name: cfg.Source, Sender: cfg.Owner, ChatGUID: cfg.ChatGUID, ChatID: cfg.ChatID}
			history := []imessage.Message{{ID: 2, GUID: "replacement", ChatID: cfg.ChatID, ChatGUID: cfg.ChatGUID, Sender: cfg.Owner, CreatedAt: time.Now()}}
			if higher {
				history = append([]imessage.Message{{ID: 3, GUID: "latest", ChatID: cfg.ChatID, ChatGUID: cfg.ChatGUID, Sender: cfg.Owner, CreatedAt: time.Now()}}, history...)
			}
			data, err := json.Marshal(history)
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("AGENT_TEST_HISTORY", string(data))
			db, err := store.Open(filepath.Join(dir, "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := db.CheckHistory(ctx, source, []store.Anchor{{ID: 1, GUID: "baseline"}}); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Accept(ctx, source, store.Event{ID: 2, GUID: "cursor", Text: "pending turn", CreatedAt: time.Now()}, "turn"); err != nil {
				t.Fatal(err)
			}
			job, _, err := db.ClaimNext(ctx, source, time.Now())
			if err != nil || job == nil {
				t.Fatalf("claim: %+v, %v", job, err)
			}
			if err := db.CompleteTurn(ctx, source, job.ID, "", []string{"must not send"}); err != nil {
				t.Fatal(err)
			}
			if err := connect(ctx, cfg, source, db, &codex.Client{Binary: "must-not-run"}); !errors.Is(err, store.ErrUncertain) {
				t.Fatalf("connect: %v", err)
			}
			for _, path := range []string{reply, subscribed} {
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("side effect before source validation: %s, %v", path, err)
				}
			}
			status, err := db.Status(ctx, source)
			if err != nil || !status.Paused || status.UnresolvedReplies != 1 || status.Cursor != 2 {
				t.Fatalf("status: %+v, %v", status, err)
			}
		})
	}
}

func TestConnectStatusRoundTrip(t *testing.T) {
	for _, transport := range []string{"", "ssh", "local"} {
		t.Run("transport="+transport, func(t *testing.T) {
			testConnectStatusRoundTrip(t, transport, "")
		})
	}
}

func TestConnectGroupStatusRoundTrip(t *testing.T) {
	for _, sender := range []string{"first@example.com", "second@example.com"} {
		t.Run(sender, func(t *testing.T) { testConnectStatusRoundTrip(t, "local", sender) })
	}
}

func testConnectStatusRoundTrip(t *testing.T, transport, groupSender string) {
	dir := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.Open(executable)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	name := "ssh"
	if transport == "local" {
		name = "imsg"
	}
	dst, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_WRONLY, 0700)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.Copy(dst, src); err != nil {
		dst.Close()
		t.Fatal(err)
	}
	if err = dst.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("AGENT_TEST_SSH", "1")
	reply := filepath.Join(dir, "reply.txt")
	t.Setenv("AGENT_TEST_REPLY", reply)
	db, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := config.Config{Transport: transport, Host: "fake", ImsgPath: "/usr/bin/imsg", ChatID: 42, ChatGUID: "iMessage;-;owner@example.com", Owner: "owner@example.com", Source: "fake-mac"}
	if transport == "local" {
		cfg.Host = ""
		cfg.ImsgPath = filepath.Join(dir, name)
	}
	if groupSender != "" {
		cfg.Owner = ""
		cfg.Group = true
		cfg.ChatGUID = "any;+;configured-group"
		cfg.AllowedSenders = []string{"first@example.com", "second@example.com"}
		message := imessage.Message{ID: 1, GUID: "baseline", ChatID: cfg.ChatID, ChatGUID: cfg.ChatGUID, IsGroup: true, Sender: groupSender, Text: "old plain text must not run", CreatedAt: time.Now()}
		history, err := json.Marshal([]imessage.Message{message})
		if err != nil {
			t.Fatal(err)
		}
		t.Setenv("AGENT_TEST_HISTORY", string(history))
		message.ID, message.GUID, message.Text = 2, "status-guid", "/status"
		data, err := json.Marshal(message)
		if err != nil {
			t.Fatal(err)
		}
		t.Setenv("AGENT_TEST_MESSAGE", string(data))
	}
	source := configuredSource(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- connect(ctx, cfg, source, db, &codex.Client{Binary: "must-not-run"}) }()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case err := <-done:
			t.Fatalf("connection stopped before reply: %v", err)
		case <-ctx.Done():
			t.Fatal("timed out waiting for status reply")
		case <-tick.C:
			if data, err := os.ReadFile(reply); err == nil && len(data) > 0 {
				cancel()
				select {
				case err := <-done:
					if err != nil && !errors.Is(err, context.Canceled) {
						t.Fatalf("shutdown: %v", err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("connection shutdown hung")
				}
				status, err := db.Status(context.Background(), source)
				if err != nil {
					t.Fatal(err)
				}
				if status.Cursor != 2 || status.Running != 0 {
					t.Fatalf("unexpected status: %+v", status)
				}
				return
			}
		}
	}
}
