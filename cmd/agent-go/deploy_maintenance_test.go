package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/teslashibe/codex"
)

func TestMCPServersSHA256UsesCodexCanonicalization(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"mcp_servers":{"cua_repl":{"command":"/Applications/Codex.app/cua_node","args":["server"]}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	want, err := codex.MCPServersSHA256(map[string]codex.MCPServer{
		"cua_repl": {Command: "/Applications/Codex.app/cua_node", Args: []string{"server"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := mcpServersSHA256(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
