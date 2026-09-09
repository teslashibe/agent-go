package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/teslashibe/agent-go/internal/harness"
	"github.com/teslashibe/agent-go/internal/store"
	"github.com/teslashibe/codex"
	"github.com/teslashibe/notes"
)

type smokeNotes struct{ nativeFixtureNotes }

type realSmokeRunner struct {
	binary      string
	codexBinary string
	model       string
	t           *testing.T
}

func (r realSmokeRunner) Run(context.Context, string, string) (Result, error) {
	r.t.Fatal("unexpected legacy runner")
	return Result{}, nil
}
func (r realSmokeRunner) RunWithNotes(ctx context.Context, session, prompt string, h http.Handler) (Result, error) {
	path, closeEndpoint, err := harness.ServeEndpoint(h)
	if err != nil {
		return Result{}, err
	}
	defer closeEndpoint()
	client := codex.Client{ExecutionPolicy: codex.ExecutionYOLO, Binary: r.codexBinary, Model: r.model, WorkDir: r.t.TempDir(), Timeout: 3 * time.Minute, OutputSchema: []byte(ActionSchema), Instructions: "You are a personal assistant in a fake-backend evaluation. Use harness tools. Final response must follow the supplied output schema.", MCPServers: map[string]codex.MCPServer{"harness": {Command: r.binary, Args: []string{"notes-mcp"}, Env: map[string]string{"AGENT_NOTES_SOCKET": path}}}}
	result, err := client.Run(ctx, session, prompt)
	r.t.Logf("REAL CODEX session=%s response=%s error=%v", result.SessionID, result.Text, err)
	return Result{SessionID: result.SessionID, Text: result.Text}, err
}

// Explicit opt-in only. The backend and incoming turn are isolated test
// fixtures; this never opens the live Notes client or messaging transport.
func newRealSmokeRunner(t *testing.T) realSmokeRunner {
	binary := os.Getenv("AGENT_CODEX_SMOKE_BINARY")
	if binary == "" {
		t.Skip("set AGENT_CODEX_SMOKE_BINARY to an agent-go binary for real model eval")
	}
	cli := os.Getenv("AGENT_CODEX_SMOKE_CODEX")
	model := os.Getenv("AGENT_CODEX_SMOKE_MODEL")
	if !filepath.IsAbs(binary) || !filepath.IsAbs(cli) || !filepath.IsAbs(os.Getenv("CODEX_HOME")) || model != "gpt-6-astra" {
		t.Fatal("fixture requires absolute reviewed binaries, explicit CODEX_HOME and the configured gpt-6-astra model")
	}
	// The adapter ignores user config/rules and enables only the fake harness MCP.
	// Disable native command, delegation, app and desktop tools independently of
	// prompt instructions. Authentication remains in the supplied CODEX_HOME;
	// no auth/session files are copied or reset. Native YOLO matches production
	// and permits fake MCP effects without approvals. The code-mode orchestrator
	// remains enabled because it dispatches MCP calls; shell execution is disabled.
	// This is not OS isolation.
	proxy := filepath.Join(t.TempDir(), "fixture-codex")
	quoted := "'" + strings.ReplaceAll(cli, "'", "'\\''") + "'"
	script := "#!/bin/sh\nexec " + quoted + " --disable shell_tool --disable unified_exec --disable multi_agent --disable multi_agent_v2 --disable apps --disable plugins --disable computer_use --disable in_app_browser --disable in_app_chat --disable in_app_local_automation --disable remote_plugin --disable image_generation --disable view_image --disable skill_search --disable skill_mcp_dependency_install -c 'web_search=\"disabled\"' \"$@\"\n"
	if err := os.WriteFile(proxy, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return realSmokeRunner{binary: binary, codexBinary: proxy, model: model, t: t}
}

func TestRealCodexNotesSmoke(t *testing.T) {
	runner := newRealSmokeRunner(t)
	for _, tc := range []struct {
		name, prompt string
		adds         int
	}{
		{"read", "What items do I need to get for ABGT700?", 0},
		{"batch", "Add pizzas, pita breads, and walnuts to Shopping List in Notes.", 3},
		{"ambiguity", "Add hatch chili mustard chips to Del Polo, pizzas, and pita breads plus walnuts to our shopping list in notes. I am unsure whether Del Polo is a separate destination or part of the item name; clarify before making changes.", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, s, _, messenger := familyNotesBridge(t)
			client := &smokeNotes{nativeFixtureNotes: nativeFixtureNotes{
				catalog: []notes.Note{
					{ID: "shopping", Name: "Shopping List", Shared: true},
					{ID: "abgt", Name: "ABGT700", Shared: true, Body: "<div>Bring earplugs, photo ID and a portable charger.</div>"},
				},
				items: []notes.ChecklistItem{{Text: "Earplugs"}, {Text: "Photo ID"}, {Text: "Portable charger"}},
			}}
			b.notes = client
			runner.t = t
			b.runner = runner
			if err := s.ConfigureNotes(context.Background(), b.config.Source, []store.NoteScope{{ID: "shopping", Title: "Shopping List", State: "shared"}, {ID: "abgt", Title: "ABGT700", State: "shared"}}); err != nil {
				t.Fatal(err)
			}
			intake(t, b, noteMessage(b, 700, tc.prompt), "turn")
			_, err := b.ProcessNext(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			_, err = b.ProcessNext(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("ordered Notes fixture events: %v", client.events)
			for _, event := range client.events {
				if strings.HasPrefix(event, "add:") && !strings.HasPrefix(event, "add:shopping:") {
					t.Fatalf("write targeted wrong note ID: %s", event)
				}
			}
			if client.adds != tc.adds {
				t.Fatalf("adds=%d want=%d", client.adds, tc.adds)
			}
			if len(messenger.texts) != 1 {
				t.Fatalf("responses=%v", messenger.texts)
			}
			reply := messenger.texts[0]
			t.Logf("HUMAN RESPONSE: %s", reply)
			if strings.Contains(reply, "<div>") || json.Valid([]byte(reply)) {
				t.Fatalf("raw response: %s", reply)
			}
			if tc.name == "read" && (!strings.Contains(strings.ToLower(reply), "earplugs") || !strings.Contains(strings.ToLower(reply), "charger")) {
				t.Fatalf("missing relevant fixture results: %s", reply)
			}
		})
	}
}
