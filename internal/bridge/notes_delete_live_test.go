package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/teslashibe/agent-go/internal/store"
	"github.com/teslashibe/notes"
)

// Explicit live opt-in: creates three disposable private notes, deletes two
// through the batch confirmation path, verifies the third survived, then deletes
// that fixture in a separate confirmed request. No messages or invitations are
// sent. The store and requester are fixtures; Apple Notes effects are real.
// On failure, retain the private evidence and remaining notes without retrying.
func TestLiveBatchNoteDeletion(t *testing.T) {
	helper := os.Getenv("AGENT_BATCH_DELETE_LIVE_HELPER")
	if helper == "" {
		t.Skip("explicit disposable Notes batch fixture opt-in required")
	}
	evidence := os.Getenv("AGENT_BATCH_DELETE_LIVE_EVIDENCE")
	if !filepath.IsAbs(helper) || !filepath.IsAbs(evidence) {
		t.Fatal("absolute helper and private evidence file required")
	}
	file, err := os.OpenFile(evidence, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	encoder := json.NewEncoder(file)
	record := func(value any) {
		t.Helper()
		if err := encoder.Encode(value); err != nil {
			t.Fatal(err)
		}
		if err := file.Sync(); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	client := notes.Client{NativeExecutable: helper, Timeout: 30 * time.Second}
	b, s, _, _ := familyNotesBridge(t)
	b.config.Source = store.Source{Name: "native-batch-fixture", ChatID: 1, ChatGUID: "iMessage;-;fixture", AllowedSenders: []string{"owner@example.test"}}
	if err = s.Initialize(ctx, b.config.Source, 0, ""); err != nil {
		t.Fatal(err)
	}
	if err = b.EnableOwnerNotes(ctx, client); err != nil {
		t.Fatal(err)
	}
	prefix := fmt.Sprintf("Agent batch deletion fixture %d", time.Now().UnixNano())
	var created []notes.Note
	for i := range 3 {
		title := fmt.Sprintf("%s %d", prefix, i+1)
		record(map[string]any{"phase": "creating", "title": title})
		n, createErr := client.Create(ctx, title, "Disposable automated batch deletion fixture.")
		if createErr != nil {
			record(map[string]any{"phase": "creation_failed", "error": createErr.Error()})
			t.Fatal("fixture creation failed; inspect private evidence before recovery")
		}
		created = append(created, n)
		record(map[string]any{"phase": "created", "note": n})
	}
	ids := []string{created[0].ID, created[1].ID}
	sender := b.config.Source.AllowedSenders[0]
	request := deletionTurn(t, b, s, sender)
	preview := deletionCall(t, request, "delete_notes", "review", ids...)
	record(preview)
	if preview.Status != "confirmation_required" {
		t.Fatal("batch review failed; inspect private evidence")
	}
	for _, n := range created {
		if _, err = client.Get(ctx, n.ID); err != nil {
			t.Fatal("fixture was not active before confirmation")
		}
	}
	finishDeletionTurn(t, s, request)
	confirm := deletionTurn(t, b, s, sender)
	result := deletionCall(t, confirm, "confirm_delete_notes", "confirm", ids...)
	record(result)
	if result.IsError || len(result.Notes) != 2 {
		t.Fatal("batch deletion incomplete; inspect private evidence without retrying")
	}
	finishDeletionTurn(t, s, confirm)
	active, err := client.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if slices.ContainsFunc(active, func(n notes.Note) bool { return n.ID == id }) {
			t.Fatal("selected fixture still active")
		}
	}
	if _, err = client.Get(ctx, created[2].ID); err != nil {
		t.Fatal("unselected control note did not survive")
	}
	record(map[string]any{"phase": "verified", "selected_inactive": true, "unselected_control_active": true})
	cleanupRequest := deletionTurn(t, b, s, sender)
	preview = deletionCall(t, cleanupRequest, "delete_notes", "review-control", created[2].ID)
	record(preview)
	if preview.Status != "confirmation_required" {
		t.Fatal("control cleanup review failed")
	}
	finishDeletionTurn(t, s, cleanupRequest)
	cleanupConfirm := deletionTurn(t, b, s, sender)
	result = deletionCall(t, cleanupConfirm, "confirm_delete_notes", "confirm-control", created[2].ID)
	record(result)
	if result.IsError {
		t.Fatal("control cleanup incomplete; retain private evidence")
	}
	record(map[string]any{"phase": "completed", "fixture_notes_moved_to_recently_deleted": 3})
}
