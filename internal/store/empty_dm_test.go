package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestEmptyDMFirstMessageIsLiveAndBecomesTrustedAnchor(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "empty.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	source := Source{Name: "mac", Sender: "owner", ChatID: 42, ChatGUID: "dm"}
	if err := db.InitializeEmptyDM(ctx, source); err != nil {
		t.Fatal(err)
	}
	if err := db.CheckHistory(ctx, source, nil); err != nil {
		t.Fatal(err)
	}
	first := Anchor{ID: 100, GUID: "first-text"}
	if err := db.CheckHistory(ctx, source, []Anchor{first}); err != nil {
		t.Fatal(err)
	}
	if cursor, err := db.Cursor(ctx, source); err != nil || cursor != 0 {
		t.Fatalf("first text was skipped as baseline: %d %v", cursor, err)
	}
	if _, err := db.BootstrapDMHistory(ctx, source, []HistoryMessage{{ID: first.ID, GUID: first.GUID, ChatID: source.ChatID, ChatGUID: source.ChatGUID, Sender: source.Sender, Text: "hello"}}); err != nil {
		t.Fatal(err)
	}
	receipt, err := db.Accept(ctx, source, Event{ID: first.ID, GUID: first.GUID, Sender: source.Sender, Text: "hello", CreatedAt: time.Now()}, "turn")
	if err != nil || receipt.Disposition != "turn" || receipt.Duplicate {
		t.Fatalf("first text must enqueue once: %+v %v", receipt, err)
	}
	job, _, err := db.ClaimNext(ctx, source, time.Now())
	if err != nil || job == nil || job.SessionID != "" {
		t.Fatalf("first text must start a new session: %+v %v", job, err)
	}
	if err := db.CheckHistory(ctx, source, []Anchor{first}); err != nil {
		t.Fatal(err)
	}
	if err := db.InitializeEmptyDM(ctx, source); err != nil {
		t.Fatal(err)
	}
	if err := db.CheckHistory(ctx, source, nil); !errors.Is(err, ErrUncertain) {
		t.Fatalf("disappeared history must fail closed after intake: %v", err)
	}
}

func TestEmptyDMBootstrapRejectsGroup(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "group.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.InitializeEmptyDM(context.Background(), Source{Name: "mac", AllowedSenders: []string{"owner"}, Group: true, ChatID: 42, ChatGUID: "group"}); err == nil {
		t.Fatal("group must retain its existing history-anchor requirement")
	}
}
