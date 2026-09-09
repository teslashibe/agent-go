package bridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/teslashibe/agent-go/internal/harness"
	"github.com/teslashibe/agent-go/internal/store"
	"github.com/teslashibe/codex"
	"github.com/teslashibe/notes"
)

// One explicit diagnostic only: fake Notes, no chat transport, read-only native
// sandbox, every native approval denied. No credentials are read or copied.
func TestRealCodexNotesDeniedDiagnostic(t *testing.T) {
	binary := os.Getenv("AGENT_NOTES_DIAGNOSTIC_PROXY")
	helper := os.Getenv("AGENT_NOTES_DIAGNOSTIC_HELPER")
	if binary == "" || helper == "" {
		t.Skip("explicit fixture diagnostic opt-in required")
	}
	model := os.Getenv("AGENT_NOTES_DIAGNOSTIC_MODEL")
	if model != "gpt-6-astra" {
		t.Fatal("diagnostic requires the configured application model gpt-6-astra; no fallback")
	}
	home := t.TempDir()
	work := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	config := []byte("model = \"" + model + "\"\n")
	if err := os.WriteFile(filepath.Join(home, "config.toml"), config, 0600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(config)
	sources := map[string]string{}
	userHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range codex.InteractiveConfigSources(home, work, userHome) {
		if path == filepath.Join(userHome, ".codex", "AGENTS.md") {
			// Reviewed explicitly: this global file is empty. Pin exactly that
			// content, not whatever happens to be present during the run.
			info, err := os.Lstat(path)
			if err != nil || !info.Mode().IsRegular() || info.Size() != 0 {
				t.Fatalf("reviewed empty AGENTS.md drifted: %v", err)
			}
			empty := sha256.Sum256(nil)
			sources[path] = hex.EncodeToString(empty[:])
			continue
		}
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("refusing unreviewed fixture source %s: %v", path, err)
		}
		sources[path] = ""
	}
	loginCtx, stopLogin := context.WithTimeout(context.Background(), 10*time.Second)
	login := exec.CommandContext(loginCtx, binary, "login", "status")
	login.Dir = work
	loginOutput, loginErr := login.CombinedOutput()
	stopLogin()
	t.Logf("isolated login status: %s; exit=%v", loginOutput, loginErr)
	if loginErr != nil {
		t.Fatalf("isolated fixture authentication unavailable; no credentials copied and app-server not launched: %v", loginErr)
	}
	b, s, _, _ := familyNotesBridge(t)
	fixture := &nativeFixtureNotes{catalog: []notes.Note{{ID: "shopping", Name: "Shopping List", Shared: true}}}
	b.notes = fixture
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := s.ConfigureNotes(ctx, b.config.Source, []store.NoteScope{{ID: "shopping", Title: "Shopping List", State: "shared"}}); err != nil {
		t.Fatal(err)
	}
	intake(t, b, noteMessage(b, 800, "Read the fake Shopping List"), "turn")
	job, _, err := s.ClaimNext(ctx, b.config.Source, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	turn := newNotesTurn(ctx, b, job)
	defer turn.close()
	socket, closeSocket, err := harness.ServeEndpoint(turn)
	if err != nil {
		t.Fatal(err)
	}
	defer closeSocket()
	servers := map[string]codex.MCPServer{}
	fingerprint, err := codex.MCPServersSHA256(servers)
	if err != nil {
		t.Fatal(err)
	}
	client := codex.Client{Binary: binary, WorkDir: work, Model: model, Timeout: 50 * time.Second, OutputSchema: []byte(ActionSchema), MCPServers: servers, Instructions: "Fixture diagnostic. Use harness only, never shell, browser, accounts, or other tools. Stop after a denied operation. Final output follows the supplied schema.\n" + NotesInstruction}
	client.InteractiveConfig = &codex.ReviewedInteractiveConfig{CodexHome: home, WorkDir: work, Binary: binary, Version: "0.153.4", ConfigSHA256: hex.EncodeToString(digest[:]), MCPServersSHA256: fingerprint, Sources: sources, Bindings: map[string]codex.ReviewedMCPBinding{"harness": {Server: codex.MCPServer{Command: helper, Args: []string{"notes-mcp"}}, DynamicEnvKeys: []string{"AGENT_NOTES_SOCKET"}}}}
	approvals := 0
	result, err := client.RunInteractive(ctx, "", "Read the fake Shopping List in harness. This is an isolated fixture. If approval is denied, stop.", func(_ context.Context, r codex.ApprovalRequest) (codex.ApprovalDecision, error) {
		approvals++
		t.Logf("DENY callback: kind=%s thread=%s turn=%s request=%s item=%s", r.Kind, r.ThreadID, r.TurnID, r.RequestID, r.ItemID)
		return codex.ApprovalDeny, nil
	}, codex.MCPBinding{Name: "harness", Env: map[string]string{"AGENT_NOTES_SOCKET": socket}})
	t.Logf("DIAGNOSTIC outcome approvals=%d session=%s error=%v", approvals, result.SessionID, err)
	if fixture.creates+fixture.adds+fixture.edits+fixture.deletes != 0 {
		t.Fatal("fixture unexpectedly mutated")
	}
	if err != nil {
		t.Fatalf("single diagnostic runtime failed: %v", err)
	}
	if approvals == 0 {
		t.Fatal("single diagnostic produced no native approval event")
	}
}
