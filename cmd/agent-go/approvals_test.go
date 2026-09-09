//go:build darwin || linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/teslashibe/agent-go/internal/bridge"
	"github.com/teslashibe/agent-go/internal/config"
	"github.com/teslashibe/codex"
)

func assertReplyOnlyAdapterInstructions(t *testing.T, instructions string) {
	t.Helper()
	if strings.Contains(strings.ToLower(instructions), "reaction") {
		t.Fatal("adapter instructs use of removed reaction field")
	}
	if !strings.Contains(instructions, "application acknowledgement outcome") || strings.Contains(instructions, "already sent") {
		t.Fatal("host acknowledgement guidance lost")
	}
	var schema struct {
		Properties           map[string]json.RawMessage
		Required             []string
		AdditionalProperties bool
	}
	if err := json.Unmarshal([]byte(bridge.ActionSchema), &schema); err != nil {
		t.Fatal(err)
	}
	if len(schema.Properties) != 1 || schema.Properties["reply"] == nil || !reflect.DeepEqual(schema.Required, []string{"reply"}) || schema.AdditionalProperties {
		t.Fatal("final response contract is not reply-only")
	}
}

// Inspect the actual append sites as well as the derived exec client above.
// The interactive adapter deliberately fails closed without reviewed deployment
// config; do not fabricate that security review merely to capture instructions.
func TestBothAdaptersAppendReplyOnlyGuidance(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"notesClient", "RunWithApprovals"} {
		seen := map[string]bool{}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name.Name != name {
				continue
			}
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				assign, ok := node.(*ast.AssignStmt)
				if !ok || assign.Tok != token.ADD_ASSIGN {
					return true
				}
				lhs, ok := assign.Lhs[0].(*ast.SelectorExpr)
				if !ok || lhs.Sel.Name != "Instructions" {
					return true
				}
				rhs, ok := assign.Rhs[0].(*ast.Ident)
				if !ok {
					t.Fatal("unreviewed inline adapter instruction append")
				}
				seen[rhs.Name] = true
				switch rhs.Name {
				case "tapbackTurnInstructions":
					assertReplyOnlyAdapterInstructions(t, tapbackTurnInstructions)
				case "progressTurnInstructions":
					if strings.Contains(strings.ToLower(progressTurnInstructions), "reaction") {
						t.Fatal("progress guidance contains removed field")
					}
				case "googleTurnInstructions":
					if strings.Contains(strings.ToLower(googleTurnInstructions), "reaction") {
						t.Fatal("google guidance contains removed field")
					}
				default:
					t.Fatalf("unreviewed adapter instruction append %s", rhs.Name)
				}
				return true
			})
		}
		if !seen["tapbackTurnInstructions"] || !seen["progressTurnInstructions"] {
			t.Fatalf("%s append sites not covered", name)
		}
	}
	if strings.Contains(strings.ToLower(bridge.NotesInstruction), "reaction") {
		t.Fatal("Notes guidance contains removed field")
	}
}

func TestInteractiveAdapterOptInAndNoFallback(t *testing.T) {
	tmp := t.TempDir()
	client := &codex.Client{Binary: "must-not-execute", WorkDir: t.TempDir()}
	t.Setenv("TMPDIR", tmp)
	defaultRunner := agentRunner{client: client}
	if _, ok := any(defaultRunner).(bridge.ApprovalRunner); ok {
		t.Fatal("default exec path now interactive")
	}
	if _, ok := any(defaultRunner).(bridge.NotesRunner); !ok {
		t.Fatal("default native Notes path lost")
	}
	adapter := interactiveAgentRunner{defaultRunner}
	for _, handler := range []http.Handler{nil, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("Notes invoked without reviewed configuration") })} {
		_, err := adapter.RunWithApprovals(context.Background(), "", "test", func(context.Context, codex.ApprovalRequest) (codex.ApprovalDecision, error) {
			t.Error("callback before configuration review")
			return codex.ApprovalDeny, nil
		}, handler)
		var blocked *codex.InteractiveConfigurationError
		if !errors.As(err, &blocked) {
			t.Fatalf("must fail config closed, never retry exec: %v", err)
		}
		entries, err := os.ReadDir(tmp)
		if err != nil || len(entries) != 0 {
			t.Fatalf("rejected startup leaked endpoint: %v %v", entries, err)
		}
	}
}

func TestNewCodexClientExecutionPolicy(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  config.ExecutionPolicy
		want codex.ExecutionPolicy
	}{
		{name: "default", want: codex.ExecutionYOLO},
		{name: "yolo", set: config.ExecutionYOLO, want: codex.ExecutionYOLO},
		{name: "read only", set: config.ExecutionReadOnly, want: codex.ExecutionReadOnly},
		{name: "workspace write", set: config.ExecutionWorkspaceWrite, want: codex.ExecutionWorkspaceWrite},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := newCodexClient(config.Config{ExecutionPolicy: tc.set})
			if client.ExecutionPolicy != tc.want {
				t.Fatalf("ExecutionPolicy = %q, want %q", client.ExecutionPolicy, tc.want)
			}
		})
	}
}

func TestNotesClientPreservesExistingMCPAndHome(t *testing.T) {
	home := os.Getenv("CODEX_HOME")
	browser := codex.MCPServer{Command: "existing-browser", Args: []string{"existing-arg"}, Env: map[string]string{"SECRET": "do-not-print"}}
	client := &codex.Client{Binary: "existing-codex", WorkDir: t.TempDir(), Instructions: "original", MCPServers: map[string]codex.MCPServer{"browser": browser}}
	runner := agentRunner{client: client}
	derived, closeEndpoint, err := runner.notesClient(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	if err != nil {
		t.Fatal(err)
	}
	defer closeEndpoint()
	if derived == client || derived.Binary != client.Binary || derived.WorkDir != client.WorkDir || !reflect.DeepEqual(derived.MCPServers["browser"], browser) {
		t.Fatal("client or browser config changed")
	}
	if len(client.MCPServers) != 1 || client.Instructions != "original" || os.Getenv("CODEX_HOME") != home {
		t.Fatal("parent config or home mutated")
	}
	if _, ok := derived.MCPServers["authorized_notes"]; ok {
		t.Fatal("obsolete endpoint registered")
	}
	assertReplyOnlyAdapterInstructions(t, derived.Instructions)
	if strings.Contains(derived.Instructions, bridge.NotesInstruction) || strings.Contains(derived.Instructions, "After tool results, use action none") {
		t.Fatal("adapter duplicated runtime Notes instructions")
	}
	notes := derived.MCPServers["harness"]
	if len(notes.Args) != 1 || notes.Args[0] != "notes-mcp" || notes.Env["AGENT_NOTES_SOCKET"] == "" {
		t.Fatal("native Notes registration missing")
	}
	closeEndpoint()
	if _, err := os.Stat(filepath.Dir(notes.Env["AGENT_NOTES_SOCKET"])); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("adapter cleanup retained endpoint: %v", err)
	}
}

func TestInteractiveAdapterDoesNotResetMissingSession(t *testing.T) {
	const session = "01a079ef-0ead-7c62-8d80-3af4015793cd"
	client := &codex.Client{Binary: "must-not-execute", InteractiveConfig: &codex.ReviewedInteractiveConfig{CodexHome: t.TempDir()}}
	adapter := interactiveAgentRunner{agentRunner{client: client}}
	for _, handler := range []http.Handler{nil, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("missing session invoked harness") })} {
		result, err := adapter.RunWithApprovals(context.Background(), session, "fixture", nil, handler)
		if err == nil || result.SessionID != session {
			t.Fatalf("missing session was reset: result=%+v error=%v", result, err)
		}
		if !strings.Contains(err.Error(), "original session preserved") {
			t.Fatal(err)
		}
	}
}
