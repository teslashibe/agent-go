package bridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teslashibe/agent-go/internal/harness"
	"github.com/teslashibe/agent-go/internal/store"
	"github.com/teslashibe/codex"
	"github.com/teslashibe/notes"
)

type nativeFixtureNotes struct {
	fakeNotes
	mu      sync.Mutex
	items   []notes.ChecklistItem
	catalog []notes.Note
	events  []string
}

func (f *nativeFixtureNotes) List(context.Context) ([]notes.Note, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, "list")
	return append([]notes.Note(nil), f.catalog...), nil
}

func (f *nativeFixtureNotes) Get(_ context.Context, id string) (notes.Note, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, "get:"+id)
	for _, note := range f.catalog {
		if note.ID == id {
			return note, nil
		}
	}
	return notes.Note{}, errors.New("unknown fixture note ID")
}

func (f *nativeFixtureNotes) Checklist(ctx context.Context, id string) ([]notes.ChecklistItem, error) {
	if _, err := f.Get(ctx, id); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, "checklist:"+id)
	return append([]notes.ChecklistItem(nil), f.items...), nil
}
func (f *nativeFixtureNotes) AddChecklistItem(ctx context.Context, id string, text string) ([]notes.ChecklistItem, error) {
	if _, err := f.Get(ctx, id); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, "add:"+id+":"+text)
	f.adds++
	f.items = append(f.items, notes.ChecklistItem{Text: text})
	return append([]notes.ChecklistItem(nil), f.items...), nil
}

// Explicit deployment gate: real Astra and actual native transport, but all
// Notes and chat effects remain in fake backends. Fixture decisions are routed
// through normal authenticated intake and are restricted to the fixture tools.
func TestRealInteractiveNotesApprovedFixture(t *testing.T) {
	configPath := os.Getenv("AGENT_NATIVE_FIXTURE_CONFIG")
	if configPath == "" {
		t.Skip("explicit native fixture opt-in required")
	}
	var app struct {
		Model   string                     `json:"model"`
		Binary  string                     `json:"codex_path"`
		WorkDir string                     `json:"work_dir"`
		Servers map[string]codex.MCPServer `json:"mcp_servers"`
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &app); err != nil {
		t.Fatal(err)
	}
	if app.Model != "gpt-6-astra" {
		t.Fatal("fixture requires production Astra model")
	}
	if proxy := os.Getenv("AGENT_NATIVE_FIXTURE_PROXY"); proxy != "" {
		app.Binary = proxy
	}
	home := os.Getenv("CODEX_HOME")
	expected := "e82c3560652629ef79a733e4b33830608863e820ec39b1e27524a47835f31966"
	config, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(config)
	if hex.EncodeToString(sum[:]) != expected {
		t.Fatal("reviewed mini config changed")
	}
	helper := os.Getenv("AGENT_NATIVE_FIXTURE_HELPER")
	if helper == "" {
		t.Fatal("missing Notes relay binary")
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	sources := map[string]string{}
	for _, path := range codex.InteractiveConfigSources(home, app.WorkDir, userHome) {
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			sources[path] = ""
			continue
		}
		// This preference file was inspected separately for absence of managed
		// config/requirements payloads. Pin only the explicitly supplied fingerprint.
		if path == filepath.Join(userHome, "Library/Preferences/com.openai.codex.plist") && os.Getenv("AGENT_NATIVE_FIXTURE_PREF_SHA") != "" {
			sources[path] = os.Getenv("AGENT_NATIVE_FIXTURE_PREF_SHA")
			continue
		}
		t.Fatalf("unreviewed source: %s", path)
	}
	digest, err := codex.MCPServersSHA256(app.Servers)
	if err != nil {
		t.Fatal(err)
	}
	plugins := []string{"codex-app-tools@openai-bundled", "sites@openai-bundled", "browser@openai-bundled", "unified-computer-use@openai-bundled", "visualize@openai-bundled", "computer-use@openai-bundled", "documents@openai-primary-runtime", "pdf@openai-primary-runtime", "spreadsheets@openai-primary-runtime", "presentations@openai-primary-runtime", "template-creator@openai-primary-runtime", "chrome@openai-bundled"}
	client := &codex.Client{ExecutionPolicy: codex.ExecutionYOLO, Binary: app.Binary, WorkDir: app.WorkDir, Model: app.Model, ReasoningEffort: "low", Timeout: 90 * time.Second, OutputSchema: []byte(ActionSchema), MCPServers: app.Servers, Instructions: "This is an isolated fake Notes evaluation. Use ONLY harness tools, never shell/browser/desktop tools. Respond in the supplied output schema."}
	client.InteractiveConfig = &codex.ReviewedInteractiveConfig{CodexHome: home, WorkDir: app.WorkDir, Binary: app.Binary, Version: "0.153.1", ConfigSHA256: expected, MCPServersSHA256: digest, DisabledPlugins: plugins, Sources: sources, Bindings: map[string]codex.ReviewedMCPBinding{"harness": {Server: codex.MCPServer{Command: helper, Args: []string{"notes-mcp"}}, DynamicEnvKeys: []string{"AGENT_NOTES_SOCKET"}}}}
	b, s, _, _ := familyNotesBridge(t)
	fixture := &nativeFixtureNotes{catalog: []notes.Note{{ID: "x-coredata://00000000-0000-4000-8000-000000000001/ICNote/p1", Name: "Shopping List", Shared: true}}, items: []notes.ChecklistItem{{Text: "Milk"}}}
	b.notes = fixture
	messenger := &approvalMessenger{sent: make(chan string, 10)}
	b.messenger = messenger
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	if err := s.ConfigureNotes(ctx, b.config.Source, []store.NoteScope{{ID: "x-coredata://00000000-0000-4000-8000-000000000001/ICNote/p1", Title: "Shopping List", State: "shared"}}); err != nil {
		t.Fatal(err)
	}
	browserOnly := os.Getenv("AGENT_NATIVE_FIXTURE_BROWSER") == "1"
	prompt := "Read the fake Shopping List, then add Pita breads and Walnuts as two separate checklist items. Tell me what you added."
	if browserOnly {
		client.Instructions = "Use the existing configured browser tools to open https://example.com in a NEW tab, read the page title and first heading, then close ONLY the tab you created. Leave all pre-existing tabs and windows untouched. Do not use Notes or shell/network alternatives. Return the required JSON with action none and a concise verified reply. Do not invent browser success."
		prompt = "Verify browser use: open https://example.com in a new browser tab, report its title and first heading, and close only your created tab."
	}
	b.runner = &approvalTestRunner{run: func(ctx context.Context, approval codex.ApprovalHandler, h http.Handler) (Result, error) {
		if approval != nil {
			return Result{}, errors.New("YOLO must not register an iMessage approval callback")
		}
		socket, closeSocket, err := harness.ServeEndpoint(h)
		if err != nil {
			return Result{}, err
		}
		defer closeSocket()
		fixturePrompt := prompt
		if !browserOnly {
			fixturePrompt = NotesInstruction + "\n" + prompt
		}
		out, err := client.RunInteractive(ctx, "", fixturePrompt, nil, codex.MCPBinding{Name: "harness", Env: map[string]string{"AGENT_NOTES_SOCKET": socket}})
		t.Logf("Astra fixture final: %s; error: %v", out.Text, err)
		return Result{SessionID: out.SessionID, Text: out.Text}, err
	}}
	intake(t, b, noteMessage(b, 900, prompt), "turn")
	done := make(chan error, 1)
	go func() { _, err := b.ProcessNext(ctx); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	for {
		select {
		case text := <-messenger.sent:
			if strings.Contains(text, "/approve") || strings.Contains(text, "/deny") || strings.Contains(text, "/cancel") {
				t.Fatalf("approval command leaked: %q", text)
			}
		default:
			items, _ := fixture.Checklist(ctx, fixture.catalog[0].ID)
			t.Logf("ordered Notes fixture events: %v", fixture.events)
			t.Logf("verified fake Notes items: %+v", items)
			if browserOnly {
				if len(items) != 1 {
					t.Fatal("browser diagnostic changed Notes")
				}
				return
			}
			if len(items) != 3 {
				t.Fatalf("expected unattended read and two additions, got %+v", items)
			}
			found := map[string]int{}
			for _, item := range items {
				found[strings.ToLower(item.Text)]++
			}
			if found["milk"] != 1 || found["pita breads"] != 1 || found["walnuts"] != 1 {
				t.Fatalf("wrong fixture items: %+v", items)
			}
			after, err := os.ReadFile(filepath.Join(home, "config.toml"))
			if err != nil {
				t.Fatal(err)
			}
			current := sha256.Sum256(after)
			if current != sum {
				t.Fatal("runtime config changed")
			}
			return
		}
	}
}
