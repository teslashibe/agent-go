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
	"strings"
	"testing"
	"time"

	"github.com/teslashibe/agent-go/internal/bridge"
	"github.com/teslashibe/agent-go/internal/config"
	"github.com/teslashibe/agent-go/internal/store"
	"github.com/teslashibe/codex"
	"github.com/teslashibe/imessage"
)

// Exercise the real RPC transport with independently identified subscriptions.
func fakeMultiChatProcess() {
	decoder, encoder := json.NewDecoder(os.Stdin), json.NewEncoder(os.Stdout)
	message := func(chat int64, id int64, text string) imessage.Message {
		return imessage.Message{ID: id, GUID: fmt.Sprintf("guid-%d-%d", chat, id), ChatID: chat, ChatGUID: fmt.Sprintf("iMessage;-;owner%d@example.com", chat), Sender: fmt.Sprintf("owner%d@example.com", chat), Text: text, CreatedAt: time.Now().UTC()}
	}
	for {
		var req struct {
			ID     string `json:"id"`
			Method string `json:"method"`
			Params struct {
				ChatID int64  `json:"chat_id"`
				Text   string `json:"text"`
			} `json:"params"`
		}
		if decoder.Decode(&req) != nil {
			return
		}
		var result any = map[string]any{}
		switch req.Method {
		case "messages.history":
			history := []imessage.Message{message(req.Params.ChatID, 1, "old")}
			if os.Getenv("AGENT_TEST_MULTI") == "worker-failure" && req.Params.ChatID == 42 {
				pending := message(42, 2, "private")
				pending.GUID = "uncertain"
				history = append([]imessage.Message{pending}, history...)
			}
			if os.Getenv("AGENT_TEST_MULTI") == "capped-chat" && req.Params.ChatID == 42 {
				history = make([]imessage.Message, store.MaxHistoryMessages)
				for i := range history {
					history[i] = message(42, int64(i+1), "old")
				}
			}
			result = map[string]any{"messages": history}
		case "watch.subscribe":
			result = map[string]any{"subscription": req.Params.ChatID + 100}
		case "send":
			file, err := os.OpenFile(os.Getenv("AGENT_TEST_REPLY"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				os.Exit(2)
			}
			_ = json.NewEncoder(file).Encode(map[string]any{"chat_id": req.Params.ChatID, "text": req.Params.Text})
			_ = file.Close()
			result = map[string]any{"ok": true}
		}
		if encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result}) != nil {
			return
		}
		if req.Method == "watch.subscribe" && !(os.Getenv("AGENT_TEST_MULTI") == "worker-failure" && req.Params.ChatID == 42) {
			m := message(req.Params.ChatID, 2, "/status")
			if os.Getenv("AGENT_TEST_MULTI") == "wrong-chat" && req.Params.ChatID == 42 {
				m = message(43, 3, "private content must not enter chat 42")
			}
			if encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": "message", "params": map[string]any{"subscription": req.Params.ChatID + 100, "message": m}}) != nil {
				return
			}
		}
	}
}

func TestConnectIndependentSubscriptions(t *testing.T) {
	for _, scenario := range []string{"routing", "wrong-chat", "paused-chat", "worker-failure", "capped-chat"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			if err = os.Symlink(executable, filepath.Join(dir, "imsg")); err != nil {
				t.Fatal(err)
			}
			t.Setenv("AGENT_TEST_SSH", "1")
			t.Setenv("AGENT_TEST_MULTI", scenario)
			replies := filepath.Join(dir, "replies.jsonl")
			t.Setenv("AGENT_TEST_REPLY", replies)
			cfg := config.Config{Source: "test-mac", Transport: "local", ImsgPath: filepath.Join(dir, "imsg")}
			for _, id := range []int64{42, 43} {
				cfg.Agents = append(cfg.Agents, config.Agent{Name: fmt.Sprint(id), Owner: fmt.Sprintf("owner%d@example.com", id), ChatID: id, ChatGUID: fmt.Sprintf("iMessage;-;owner%d@example.com", id)})
			}
			db, err := store.Open(filepath.Join(dir, "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if scenario == "paused-chat" || scenario == "worker-failure" {
				source := configuredSource(cfg.ForAgent(cfg.Agents[0]))
				if err = db.CheckHistory(context.Background(), source, []store.Anchor{{ID: 1, GUID: "guid-42-1"}}); err != nil {
					t.Fatal(err)
				}
				_, err = db.Accept(context.Background(), source, store.Event{ID: 2, GUID: "uncertain", Text: "private", Sender: source.Sender, CreatedAt: time.Now()}, "turn")
				if err != nil {
					t.Fatal(err)
				}
				if scenario == "paused-chat" {
					job, _, err := db.ClaimNext(context.Background(), source, time.Now())
					if err != nil || job == nil {
						t.Fatalf("claim: %v %v", job, err)
					}
					if err = db.MarkTurnUnknown(context.Background(), source, job.ID, errors.New("private failure")); err != nil {
						t.Fatal(err)
					}
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- connect(ctx, cfg, configuredSource(cfg), db, &codex.Client{Binary: "must-not-run"}) }()
			expected := 1
			if scenario == "routing" {
				expected = 2
			}
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
		wait:
			for {
				select {
				case err := <-done:
					t.Fatalf("stopped before healthy reply: %v", err)
				case <-ctx.Done():
					t.Fatal("timed out waiting for routed replies")
				case <-ticker.C:
					data, _ := os.ReadFile(replies)
					if strings.Count(string(data), "\n") >= expected {
						if scenario == "worker-failure" {
							status, err := db.Status(ctx, configuredSource(cfg.ForAgent(cfg.Agents[0])))
							if err != nil {
								t.Fatal(err)
							}
							if !status.Paused {
								continue
							}
						}
						break wait
					}
				}
			}
			cancel()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("shutdown failed to join all workers")
			}
			data, err := os.ReadFile(replies)
			if err != nil {
				t.Fatal(err)
			}
			decoder := json.NewDecoder(strings.NewReader(string(data)))
			seen := map[int64]int{}
			for {
				var reply struct {
					ChatID int64  `json:"chat_id"`
					Text   string `json:"text"`
				}
				err := decoder.Decode(&reply)
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				seen[reply.ChatID]++
				if !strings.Contains(reply.Text, "Paused: false") {
					t.Fatalf("bad status: %q", reply.Text)
				}
			}
			if seen[43] != 1 || (scenario == "routing" && seen[42] != 1) || (scenario != "routing" && seen[42] != 0) {
				t.Fatalf("cross-chat or missing replies: %v", seen)
			}
			if scenario == "wrong-chat" {
				status, err := db.Status(context.Background(), configuredSource(cfg.ForAgent(cfg.Agents[0])))
				if err != nil || status.Cursor != 1 || status.Queued != 0 {
					t.Fatalf("wrong identity advanced inbox: %+v %v", status, err)
				}
			}
		})
	}
}

type failingRuntimeRunner struct{}

func (failingRuntimeRunner) Run(context.Context, string, string) (bridge.Result, error) {
	return bridge.Result{}, errors.New("private model failure")
}

type runtimeMessenger struct{}

func (runtimeMessenger) Send(context.Context, int64, string) error { return nil }
func (runtimeMessenger) React(context.Context, int64, string, string) (bridge.ReactionResult, error) {
	return bridge.ReactionResult{}, nil
}

func TestRunChatWorkersJoinsReminderSiblingOnFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	source := store.Source{Name: "mac", ChatID: 42, ChatGUID: "iMessage;-;owner@example.com", Sender: "owner@example.com"}
	if err = db.CheckHistory(ctx, source, []store.Anchor{{ID: 1, GUID: "baseline"}}); err != nil {
		t.Fatal(err)
	}
	_, err = db.Accept(ctx, source, store.Event{ID: 2, GUID: "incoming", Sender: source.Sender, Text: "hello", CreatedAt: time.Now()}, "turn")
	if err != nil {
		t.Fatal(err)
	}
	worker, err := bridge.New(db, failingRuntimeRunner{}, runtimeMessenger{}, bridge.Config{Source: source})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- runChatWorkers(ctx, worker) }()
	select {
	case err := <-done:
		if !errors.Is(err, store.ErrUncertain) {
			t.Fatalf("lost uncertainty: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("failed worker did not cancel and join reminder sibling")
	}
}

func TestSenderRejectsUnconfiguredChat(t *testing.T) {
	s := sender{chats: map[int64]config.Config{42: {ChatID: 42, Transport: "local"}}}
	if err := s.Send(context.Background(), 43, "private"); !errors.Is(err, errIdentity) {
		t.Fatalf("send: %v", err)
	}
	if _, err := s.React(context.Background(), 43, "private-guid", "like"); !errors.Is(err, errIdentity) {
		t.Fatalf("reaction: %v", err)
	}
}
