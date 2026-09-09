//go:build darwin || linux

package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/teslashibe/agent-go/internal/config"
	"github.com/teslashibe/agent-go/internal/store"
	"github.com/teslashibe/imessage"
)

func dmHistoryFixture(t *testing.T) (config.Config, *store.Store, []imessage.Message) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	cfg := config.Config{Source: "messages", Owner: "alex", ChatID: 1, ChatGUID: "iMessage;-;alex"}
	messages := []imessage.Message{
		{ID: 10, GUID: "latest", ChatID: 1, ChatGUID: "iMessage;-;alex", Sender: "me", IsFromMe: true, Text: "prior outgoing", CreatedAt: time.Now()},
		{ID: 1, GUID: "oldest", ChatID: 1, ChatGUID: "iMessage;-;alex", Sender: "alex", Text: "old private fact", CreatedAt: time.Now().Add(-time.Hour)},
	}
	return cfg, db, messages
}

func TestPrepareChatHistoryArchivesBeforeLiveWorkAndContinuesOnce(t *testing.T) {
	ctx := context.Background()
	cfg, db, messages := dmHistoryFixture(t)
	if err := prepareChatHistory(ctx, cfg, db, messages); err != nil {
		t.Fatal(err)
	}
	source := configuredSource(cfg)
	status, err := db.Status(ctx, source)
	if err != nil || status.Cursor != 10 || status.Queued != 0 {
		t.Fatalf("bootstrap executed history: %+v %v", status, err)
	}
	now := time.Now()
	event := store.Event{ID: 11, GUID: "first-live", Text: "new question", Sender: cfg.Owner, CreatedAt: now}
	if receipt, err := db.Accept(ctx, source, event, "turn"); err != nil || receipt.Duplicate {
		t.Fatalf("first live: %+v %v", receipt, err)
	}
	if receipt, err := db.Accept(ctx, source, event, "turn"); err != nil || !receipt.Duplicate {
		t.Fatalf("duplicate live: %+v %v", receipt, err)
	}
	job, _, err := db.ClaimNext(ctx, source, now)
	if err != nil || job == nil {
		t.Fatalf("claim: %+v %v", job, err)
	}
	prompt, err := db.RequestContext(ctx, source, job, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"old private fact", "prior outgoing", `"is_from_me"`, "new question"} {
		if !strings.Contains(prompt, text) {
			t.Fatalf("missing %q in %s", text, prompt)
		}
	}
	if err := db.CompleteTurn(ctx, source, job.ID, "durable-session", nil); err != nil {
		t.Fatal(err)
	}
	messages = append([]imessage.Message{{ID: 11, GUID: "first-live", ChatID: cfg.ChatID, ChatGUID: cfg.ChatGUID, Sender: cfg.Owner, Text: event.Text, CreatedAt: now}}, messages...)
	if err := prepareChatHistory(ctx, cfg, db, messages); err != nil {
		t.Fatal(err)
	}
	status, err = db.Status(ctx, source)
	if err != nil || status.SessionID != "durable-session" || status.Queued != 0 {
		t.Fatalf("reconnect: %+v %v", status, err)
	}
}

func TestPrepareChatHistoryEmptyDMCompletesThenAcceptsFirstText(t *testing.T) {
	ctx := context.Background()
	cfg, db, _ := dmHistoryFixture(t)
	if err := prepareChatHistory(ctx, cfg, db, nil); err != nil {
		t.Fatal(err)
	}
	if err := prepareChatHistory(ctx, cfg, db, nil); err != nil {
		t.Fatal(err)
	}
	source := configuredSource(cfg)
	now := time.Now()
	if receipt, err := db.Accept(ctx, source, store.Event{ID: 1, GUID: "first", Text: "hello", Sender: cfg.Owner, CreatedAt: now}, "turn"); err != nil || receipt.Disposition != "turn" {
		t.Fatalf("first text: %+v %v", receipt, err)
	}
	if err := prepareChatHistory(ctx, cfg, db, nil); err == nil {
		t.Fatal("accepted empty history after the real cursor advanced")
	}
}

func TestPrepareChatHistoryCapOnlyAppliesBeforeBootstrap(t *testing.T) {
	ctx := context.Background()
	cfg, db, initial := dmHistoryFixture(t)
	if err := prepareChatHistory(ctx, cfg, db, initial); err != nil {
		t.Fatal(err)
	}
	capped := make([]imessage.Message, store.MaxHistoryMessages)
	for i := range capped {
		capped[i] = initial[0]
		capped[i].ID = int64(i + 1)
		capped[i].GUID = fmt.Sprintf("history-%d", i+1)
	}
	capped[0], capped[9] = initial[1], initial[0]
	if err := prepareChatHistory(ctx, cfg, db, capped); err != nil {
		t.Fatalf("already bootstrapped DM must reconnect with a capped anchor window: %v", err)
	}
	if cursor, err := db.Cursor(ctx, configuredSource(cfg)); err != nil || cursor != 10 {
		t.Fatalf("reconnect advanced cursor and skipped pending texts: %d %v", cursor, err)
	}
	capped[0].ChatGUID = "another-persons-dm"
	if err := prepareChatHistory(ctx, cfg, db, capped); !errors.Is(err, errIdentity) {
		t.Fatalf("bootstrapped DM must still validate history identity: %v", err)
	}
	freshCfg, freshDB, _ := dmHistoryFixture(t)
	if err := prepareChatHistory(ctx, freshCfg, freshDB, capped); !errors.Is(err, store.ErrUncertain) {
		t.Fatalf("initial cap must stop only this chat: %v", err)
	}
}

func TestPrepareChatHistoryFailsClosed(t *testing.T) {
	for _, kind := range []string{"cap", "above-cap", "chat-id", "chat-guid", "group", "sender", "duplicate", "missing-guid", "oversize"} {
		t.Run(kind, func(t *testing.T) {
			cfg, db, messages := dmHistoryFixture(t)
			switch kind {
			case "cap":
				messages = make([]imessage.Message, store.MaxHistoryMessages)
			case "above-cap":
				messages = make([]imessage.Message, store.MaxHistoryMessages+1)
			case "chat-id":
				messages[1].ChatID++
			case "chat-guid":
				messages[1].ChatGUID = "dm-jordan"
			case "group":
				messages[1].IsGroup = true
			case "sender":
				messages[1].Sender = "jordan"
			case "duplicate":
				messages = append(messages, messages[0])
			case "missing-guid":
				messages[1].GUID = ""
			case "oversize":
				messages[1].Text = strings.Repeat("x", store.MaxHistoryBytes)
			}
			if err := prepareChatHistory(context.Background(), cfg, db, messages); err == nil {
				t.Fatal("accepted incomplete or foreign history")
			}
			status, err := db.Status(context.Background(), configuredSource(cfg))
			if err != nil && !errors.Is(err, store.ErrUninitialized) {
				t.Fatal(err)
			}
			if status.Queued != 0 {
				t.Fatalf("history enqueued: %+v", status)
			}
		})
	}
}

func TestPrepareChatHistoryRetriesAfterImportFailure(t *testing.T) {
	ctx := context.Background()
	cfg, db, messages := dmHistoryFixture(t)
	messages[1].Text = strings.Repeat("x", store.MaxHistoryBytes)
	if err := prepareChatHistory(ctx, cfg, db, messages); err == nil {
		t.Fatal("accepted oversize")
	}
	messages[1].Text = "retry-private-fact"
	if err := prepareChatHistory(ctx, cfg, db, messages); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	source := configuredSource(cfg)
	if _, err := db.Accept(ctx, source, store.Event{ID: 11, GUID: "live", Text: "question", Sender: cfg.Owner, CreatedAt: now}, "turn"); err != nil {
		t.Fatal(err)
	}
	job, _, err := db.ClaimNext(ctx, source, now)
	if err != nil || job == nil {
		t.Fatalf("claim: %+v %v", job, err)
	}
	prompt, err := db.RequestContext(ctx, source, job, now)
	if err != nil || !strings.Contains(prompt, "retry-private-fact") {
		t.Fatalf("retry did not archive: %s %v", prompt, err)
	}
}

func TestPrepareChatHistoryGroupOnlyChecksAnchor(t *testing.T) {
	cfg, db, messages := dmHistoryFixture(t)
	cfg.Group = true
	cfg.ChatGUID = "any;+;family"
	cfg.AllowedSenders = []string{"alex", "jordan"}
	for i := range messages {
		messages[i].IsGroup = true
		messages[i].ChatGUID = cfg.ChatGUID
	}
	messages[1].Sender = "another group participant"
	messages[1].Text = strings.Repeat("x", store.MaxHistoryBytes+1)
	if err := prepareChatHistory(context.Background(), cfg, db, messages); err != nil {
		t.Fatal(err)
	}
	status, err := db.Status(context.Background(), configuredSource(cfg))
	if err != nil || status.Cursor != 10 || status.Queued != 0 {
		t.Fatalf("group: %+v %v", status, err)
	}
}
