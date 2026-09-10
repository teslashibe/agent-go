package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/teslashibe/agent-go/internal/harness"
	"github.com/teslashibe/agent-go/internal/store"
	"github.com/teslashibe/imessage"
	"github.com/teslashibe/notes"
)

type polishLiveMessenger struct {
	client *imessage.Client
	chatID int64
}

func (m polishLiveMessenger) Send(ctx context.Context, chatID int64, text string) error {
	if chatID != m.chatID {
		return errors.New("fixture chat mismatch")
	}
	_, err := m.client.Send(ctx, chatID, text)
	return err
}
func (m polishLiveMessenger) React(context.Context, int64, string, string) (bool, error) {
	return false, errors.New("native fixture has no real inbound message to react to")
}

// Explicit opt-in: real Notes and three messages to the configured group. The
// runner is scripted, not a model. Keep the fixture store, notes and evidence
// until recipient opening/edit verification and exact-ID cleanup are complete.
// Existing note IDs, when supplied, are used only for read-only link retrieval.
func TestLiveSharedNotesPolish(t *testing.T) {
	path := os.Getenv("AGENT_NOTES_POLISH_LIVE_CONFIG")
	if path == "" {
		t.Skip("explicit native Notes/group message fixture opt-in required")
	}
	var cfg struct {
		Source                         store.Source
		Helper, Imsg, Evidence, Prefix string
		ExistingNoteIDs                []string
	}
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &cfg) != nil {
		t.Fatal("invalid private fixture configuration")
	}
	if cfg.Source.Validate() != nil || !cfg.Source.Group || len(cfg.Source.AllowedSenders) != 2 || !filepath.IsAbs(cfg.Helper) || !filepath.IsAbs(cfg.Imsg) || !filepath.IsAbs(cfg.Evidence) || !strings.HasPrefix(cfg.Prefix, "Agent verification ") || len(cfg.ExistingNoteIDs) != 2 {
		t.Fatal("fixture requires exact reviewed group, two existing note IDs, paths and marker")
	}
	if err := os.Mkdir(cfg.Evidence, 0700); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(cfg.Evidence, "native.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	record := func(v any) {
		t.Helper()
		if err := json.NewEncoder(f).Encode(v); err != nil {
			t.Fatal(err)
		}
		if err := f.Sync(); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	proc := exec.CommandContext(ctx, cfg.Imsg, "rpc")
	stdout, err := proc.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := proc.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := proc.Start(); err != nil {
		t.Fatal(err)
	}
	messages := imessage.NewClient(stdout, stdin)
	defer func() { messages.Close(); _ = proc.Process.Kill(); _ = proc.Wait() }()
	chats, err := messages.Chats(ctx, 10000)
	if err != nil {
		t.Fatal("cannot verify the fixture chat")
	}
	matched := false
	for _, c := range chats {
		if c.ID == cfg.Source.ChatID && c.GUID == cfg.Source.ChatGUID && c.IsGroup {
			matched = true
		}
	}
	if !matched {
		t.Fatal("configured fixture group was not verified")
	}
	client := notes.Client{NativeExecutable: cfg.Helper, Timeout: 30 * time.Second}
	control, err := client.Create(ctx, cfg.Prefix+" control", "Unchanged disposable control.")
	record(map[string]any{"phase": "control_created", "note_id": control.ID, "success": err == nil})
	if err != nil {
		t.Fatal("control creation failed; inspect retained evidence")
	}
	before, err := client.Get(ctx, control.ID)
	if err != nil {
		t.Fatal("control read failed")
	}
	s, err := store.Open(filepath.Join(cfg.Evidence, "fixture-state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Initialize(ctx, cfg.Source, 0, ""); err != nil {
		t.Fatal(err)
	}
	var createdID string
	stage := 0
	runner := &nativeTestRunner{runNative: func(turnCtx context.Context, handler http.Handler) (Result, error) {
		endpoint, closeEndpoint, err := harness.ServeEndpoint(handler)
		if err != nil {
			return Result{}, err
		}
		defer closeEndpoint()
		call := func(name string, args map[string]any) noteOutcome {
			t.Helper()
			request, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
			var output bytes.Buffer
			if err := harness.RelayMCP(turnCtx, endpoint, bytes.NewReader(append(request, '\n')), &output); err != nil {
				record(map[string]any{"phase": "transport_failed", "tool": name})
				t.Fatal("native tool transport failed; do not retry")
			}
			out := decodeOutcome(t, output.String())
			record(map[string]any{"phase": "tool_result", "tool": name, "result": out})
			if out.IsError {
				t.Fatal("native operation failed; inspect retained evidence without retrying")
			}
			return out
		}
		reply := ""
		switch stage {
		case 0:
			items := make([]string, 22)
			for i := range items {
				items[i] = fmt.Sprintf("Fixture item %02d", i+1)
			}
			out := call("create_shared_note", map[string]any{"operation_id": "create", "title": cfg.Prefix + " checklist", "body": "Disposable shared checklist regression.", "items": items})
			createdID = out.NoteID
			if createdID == "" || out.Link == "" || len(out.Items) != 22 {
				t.Fatal("creation result is incomplete")
			}
			read := call("read_note", map[string]any{"operation_id": "read-created", "note_id": createdID})
			if len(read.Checklist) != 22 {
				t.Fatal("created checklist count differs")
			}
			reply = cfg.Prefix + ": created a disposable shared checklist with 22 items. Open the invitation below to check recipient access."
		case 1:
			call("check_note_item", map[string]any{"operation_id": "check-first", "note_id": createdID, "text": "Fixture item 01"})
			items := make([]string, 8)
			for i := range items {
				items[i] = fmt.Sprintf("Fixture item %02d", i+23)
			}
			call("add_note_items", map[string]any{"operation_id": "extend", "note_id": createdID, "items": items})
			read := call("read_note", map[string]any{"operation_id": "read-extended", "note_id": createdID})
			if len(read.Checklist) != 30 || !read.Checklist[0].Checked {
				t.Fatal("extended checklist lost count or checked state")
			}
			for i, item := range read.Checklist {
				if item.Text != fmt.Sprintf("Fixture item %02d", i+1) || item.Checked != (i == 0) {
					t.Fatal("native checklist contents differ")
				}
			}
			reply = cfg.Prefix + ": verified 30 checklist items with the first item still checked."
		case 2:
			reply = cfg.Prefix + ": here are the opening links for your two existing packing lists. Each invited participant can open the link using their Apple Account. Their contents and sharing permissions were not changed."
			for i, id := range cfg.ExistingNoteIDs {
				out := call("get_note_link", map[string]any{"operation_id": fmt.Sprintf("existing-link-%d", i), "note_id": id})
				if out.Link == "" {
					t.Fatal("existing note link absent")
				}
				reply += "\n\n" + out.Title + "\n" + out.Link
			}
		default:
			return Result{}, errors.New("unexpected fixture turn")
		}
		stage++
		return Result{SessionID: "native-polish-fixture", Text: actionText(store.Action{Action: "none", Reply: reply})}, nil
	}}
	b, err := New(s, runner, polishLiveMessenger{messages, cfg.Source.ChatID}, Config{Source: cfg.Source, StructuredActions: true, ChunkRunes: 3000})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.EnableFamilyNotes(ctx, client); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		intake(t, b, noteMessage(b, int64(900+i), cfg.Prefix), "turn")
		drain(t, b)
	}
	after, err := client.Get(ctx, control.ID)
	if err != nil || after.ID != before.ID || after.Name != before.Name || after.Plaintext != before.Plaintext || after.Shared != before.Shared {
		t.Fatal("unselected control changed")
	}
	record(map[string]any{"phase": "complete", "checklist_note_id": createdID, "control_note_id": control.ID, "checklist_counts": []int{22, 30}, "recipient_opening": "not yet verified", "cleanup": "retain fixtures until recipient verification"})
}
