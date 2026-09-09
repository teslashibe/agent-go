package bridge

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/teslashibe/agent-go/internal/store"
)

func TestPrivateSessionsStartAutomaticallyPersistAndStayIsolated(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "private-sessions.sqlite")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { db.Close() }()
	sources := []store.Source{
		{Name: "same-messages-database", AllowedSenders: []string{"alex"}, ChatID: 1, ChatGUID: "dm-alex"},
		{Name: "same-messages-database", AllowedSenders: []string{"jordan"}, ChatID: 2, ChatGUID: "dm-jordan"},
		{Name: "same-messages-database", AllowedSenders: []string{"alex", "jordan"}, Group: true, ChatID: 3, ChatGUID: "family-group"},
	}
	newBridge := func(src store.Source) (*Bridge, *fakeRunner, *fakeMessenger) {
		t.Helper()
		runner := &fakeRunner{run: func(_ context.Context, _, _ string) (Result, error) {
			return Result{SessionID: "session-" + src.ChatGUID, Text: jsonAction(store.Action{Action: "none", Reply: "reply-" + src.ChatGUID})}, nil
		}}
		messenger := &fakeMessenger{}
		b, err := New(db, runner, messenger, Config{Source: src, ChunkRunes: 4000, StructuredActions: true})
		if err != nil {
			t.Fatal(err)
		}
		return b, runner, messenger
	}
	incoming := func(src store.Source, id int64, text string) Message {
		return Message{ID: id, GUID: fmt.Sprintf("%s-%d", src.ChatGUID, id), Sender: src.AllowedSenders[0], ChatID: src.ChatID, ChatGUID: src.ChatGUID, IsGroup: src.Group, Text: text, CreatedAt: time.Now()}
	}
	for _, src := range sources {
		if err := db.Initialize(ctx, src, 1, src.ChatGUID+"-baseline"); err != nil {
			t.Fatal(err)
		}
		b, runner, messenger := newBridge(src)
		intake(t, b, incoming(src, 2, "private-marker-"+src.ChatGUID), "turn")
		drain(t, b)
		if len(runner.sessions) != 1 || runner.sessions[0] != "" {
			t.Fatalf("first text must automatically create a session: %q", runner.sessions)
		}
		if len(messenger.chatIDs) != 1 || messenger.chatIDs[0] != src.ChatID {
			t.Fatalf("reply routed outside its chat: %v", messenger.chatIDs)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, src := range sources {
		b, runner, _ := newBridge(src)
		intake(t, b, incoming(src, 3, "continue our conversation"), "turn")
		drain(t, b)
		if len(runner.sessions) != 1 || runner.sessions[0] != "session-"+src.ChatGUID {
			t.Fatalf("%s did not resume its own persisted session: %q", src.ChatGUID, runner.sessions)
		}
		if src.Group && !strings.Contains(runner.prompts[0], "private-marker-"+src.ChatGUID) {
			t.Fatalf("%s lost its own conversational context", src.ChatGUID)
		}
		for _, other := range sources {
			if other.ChatGUID != src.ChatGUID && strings.Contains(runner.prompts[0], "private-marker-"+other.ChatGUID) {
				t.Fatalf("%s received %s history", src.ChatGUID, other.ChatGUID)
			}
		}
	}
	b, runner, _ := newBridge(sources[0])
	intake(t, b, incoming(sources[0], 4, "/new"), "new")
	drain(t, b)
	intake(t, b, incoming(sources[0], 5, "start again"), "turn")
	drain(t, b)
	if len(runner.sessions) != 1 || runner.sessions[0] != "" {
		t.Fatalf("/new failed to reset only its session: %q", runner.sessions)
	}
	for _, src := range sources[1:] {
		status, err := db.Status(ctx, src)
		if err != nil || status.SessionID != "session-"+src.ChatGUID {
			t.Fatalf("/new changed another chat's session: %+v %v", status, err)
		}
	}
}
