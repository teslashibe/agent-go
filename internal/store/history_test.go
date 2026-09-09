package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func archiveMessage(source Source, id int64, sender, text string, outgoing bool) HistoryMessage {
	return HistoryMessage{ID: id, GUID: fmt.Sprintf("archive-%d", id), ChatID: source.ChatID, ChatGUID: source.ChatGUID, IsGroup: source.Group, Sender: sender, IsFromMe: outgoing, CreatedAt: time.Date(2026, 9, 1, 12, 0, int(id), 0, time.UTC), Text: text}
}

func TestImportHistoryFullFidelityAndNoReplay(t *testing.T) {
	ctx := context.Background()
	s, source, _ := reminderStore(t)
	long := strings.Repeat("detailed trip plan ", 500) + "END-OF-LONG-PLAN"
	messages := []HistoryMessage{
		archiveMessage(source, 1, source.AllowedSenders[0], long, false),
		archiveMessage(source, 2, source.AllowedSenders[1], "second speaker", false),
		archiveMessage(source, 3, "bot@example.test", "assistant voice transcript", true),
	}
	messages[2].Metadata = json.RawMessage(`{"attachments":[{"transcription":"available original voice transcript"}]}`)
	if _, err := s.db.Exec(`UPDATE sources SET session='old-session' WHERE source=?`, source.key()); err != nil {
		t.Fatal(err)
	}
	before, err := s.Status(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	n, err := s.ImportHistory(ctx, source, messages)
	if err != nil || n != 3 {
		t.Fatalf("import %d: %v", n, err)
	}
	after, err := s.Status(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	if after.Cursor != before.Cursor || after.SessionID != "" || after.Queued != 0 || after.UnresolvedReplies != 0 {
		t.Fatalf("unexpected import side effects: %+v", after)
	}
	for _, table := range []string{"inbox", "jobs", "replies", "reminders"} {
		var count int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s count=%d %v", table, count, err)
		}
	}
	if _, err := s.db.Exec(`UPDATE sources SET session='new-session' WHERE source=?`, source.key()); err != nil {
		t.Fatal(err)
	}
	n, err = s.ImportHistory(ctx, source, messages)
	if err != nil || n != 0 {
		t.Fatalf("repeat %d: %v", n, err)
	}
	after, _ = s.Status(ctx, source)
	if after.SessionID != "new-session" {
		t.Fatal("repeat reset session")
	}
	// Polling an imported historical request advances intake without creating work.
	receipt, err := s.Accept(ctx, source, Event{ID: 1, GUID: messages[0].GUID, Sender: messages[0].Sender, Text: long}, "turn")
	if err != nil || receipt.Disposition != "rejected" {
		t.Fatalf("replay admitted: %+v %v", receipt, err)
	}
	job := conversationJob(t, s, source, source.AllowedSenders[0], "what did we plan?")
	prompt, err := s.RequestContext(ctx, source, job, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{long, "second speaker", "assistant voice transcript", "available original voice transcript", "UNTRUSTED"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("missing full history %q", want[:min(len(want), 70)])
		}
	}
	if len(prompt) > MaxRequestBytes {
		t.Fatal("request budget exceeded")
	}
	if err := s.CompleteTurn(ctx, source, job.ID, "continued", []string{"recalled plan"}); err != nil {
		t.Fatal(err)
	}
	flushReplies(t, s, source)
	_, err = s.Accept(ctx, source, Event{ID: 11, GUID: "reset-archive", Sender: source.AllowedSenders[0], Text: "/new"}, "new")
	if err != nil {
		t.Fatal(err)
	}
	flushReplies(t, s, source)
	n, err = s.ImportHistory(ctx, source, messages)
	if err != nil || n != 0 {
		t.Fatalf("repeat after reset %d %v", n, err)
	}
	job = conversationJob(t, s, source, source.AllowedSenders[1], "clean conversation")
	prompt, err = s.RequestContext(ctx, source, job, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(prompt, "END-OF-LONG-PLAN") || strings.Contains(prompt, "assistant voice transcript") {
		t.Fatal("/new resurrected archive")
	}
	var count int
	s.db.QueryRow(`SELECT COUNT(*) FROM history_messages`).Scan(&count)
	if count != 3 {
		t.Fatal("reset deleted archive")
	}
}

func TestImportHistoryRejectsWithoutMutatingState(t *testing.T) {
	for _, kind := range []string{"outsider", "wrong-chat", "busy", "oversize", "conflict"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			s, source, _ := reminderStore(t)
			good := archiveMessage(source, 1, source.AllowedSenders[0], "original", false)
			if kind == "conflict" {
				if _, err := s.ImportHistory(ctx, source, []HistoryMessage{good}); err != nil {
					t.Fatal(err)
				}
			}
			s.db.Exec(`UPDATE sources SET session='preserve' WHERE source=?`, source.key())
			messages := []HistoryMessage{archiveMessage(source, 2, source.AllowedSenders[1], "valid-first", false), good}
			switch kind {
			case "outsider":
				messages[1].Sender = "outsider@example.test"
			case "wrong-chat":
				messages[1].ChatGUID = "other"
			case "busy":
				conversationJob(t, s, source, source.AllowedSenders[0], "busy")
			case "oversize":
				messages[1].Text = strings.Repeat("x", MaxHistoryBytes)
			case "conflict":
				messages[1].Text = "changed"
			}
			_, err := s.ImportHistory(ctx, source, messages)
			if err == nil {
				t.Fatal("unsafe import accepted")
			}
			if kind == "busy" && !errors.Is(err, ErrBusy) {
				t.Fatalf("wrong busy error %v", err)
			}
			state, _ := s.Status(ctx, source)
			if state.SessionID != "preserve" {
				t.Fatal("failed import reset session")
			}
			var count int
			s.db.QueryRow(`SELECT COUNT(*) FROM history_messages`).Scan(&count)
			want := 0
			if kind == "conflict" {
				want = 1
			}
			if count != want {
				t.Fatalf("partial import: %d", count)
			}
		})
	}
}

func TestImportOpenDoesNotRecoverBusyState(t *testing.T) {
	ctx := context.Background()
	s, source, path := reminderStore(t)
	job := conversationJob(t, s, source, source.AllowedSenders[0], "running")
	other, err := OpenForHistoryImport(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	_, err = other.ImportHistory(ctx, source, []HistoryMessage{archiveMessage(source, 1, source.AllowedSenders[0], "history", false)})
	if !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	var state string
	if err = s.db.QueryRow(`SELECT state FROM jobs WHERE id=?`, job.ID).Scan(&state); err != nil || state != "running" {
		t.Fatalf("import performed recovery: %s %v", state, err)
	}
}
