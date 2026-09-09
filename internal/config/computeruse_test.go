package config

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/teslashibe/codex"
)

func TestComputerUseRequiresSharedMCPOnEveryChat(t *testing.T) {
	cfg := Config{
		Transport:   "local",
		Owner:       "owner@example.test",
		ChatID:      1,
		ChatGUID:    "iMessage;-;owner@example.test",
		Source:      "test",
		StatePath:   filepath.Join(t.TempDir(), "state.db"),
		WorkDir:     t.TempDir(),
		ComputerUse: true,
	}
	if _, err := loadTestConfig(t, cfg); err == nil || !strings.Contains(err.Error(), "cua_repl") {
		t.Fatalf("missing cua_repl accepted: %v", err)
	}
	cfg.MCPServers = map[string]codex.MCPServer{ComputerUseMCPServer: {Command: "/reviewed/cua"}}
	loaded, err := loadTestConfig(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.ComputerUse || loaded.MCPServers[ComputerUseMCPServer].Command != "/reviewed/cua" {
		t.Fatal("computer_use or cua_repl dropped on load")
	}
	projected := loaded.ForAgent(loaded.Agents[0])
	if !projected.ComputerUse || projected.MCPServers[ComputerUseMCPServer].Command != "/reviewed/cua" {
		t.Fatal("computer_use not inherited by chat projection")
	}
}

func TestComputerUseKeepsShadowingPluginDisabled(t *testing.T) {
	cfg := Config{
		Transport:   "local",
		Owner:       "owner@example.test",
		ChatID:      1,
		ChatGUID:    "iMessage;-;owner@example.test",
		Source:      "test",
		StatePath:   filepath.Join(t.TempDir(), "state.db"),
		WorkDir:     t.TempDir(),
		ComputerUse: true,
		MCPServers:  map[string]codex.MCPServer{ComputerUseMCPServer: {Command: "/reviewed/cua"}},
		InteractiveConfig: &codex.ReviewedInteractiveConfig{
			CodexHome:      "/existing/home",
			WorkDir:        "/existing/work",
			Binary:         "/pinned/codex",
			Version:        "0.153.1",
			EnabledPlugins: []string{"computer-use@openai-bundled", ShadowingComputerUsePlugin},
		},
	}
	if _, err := loadTestConfig(t, cfg); err == nil || !strings.Contains(err.Error(), "not to enable "+ShadowingComputerUsePlugin) {
		t.Fatalf("enabled shadowing plugin accepted: %v", err)
	}
	cfg.InteractiveConfig.EnabledPlugins = []string{"computer-use@openai-bundled"}
	if _, err := loadTestConfig(t, cfg); err == nil || !strings.Contains(err.Error(), "to disable "+ShadowingComputerUsePlugin) {
		t.Fatalf("unlisted shadowing plugin accepted: %v", err)
	}
	cfg.InteractiveConfig.DisabledPlugins = []string{"visualize@openai-bundled", ShadowingComputerUsePlugin}
	if _, err := loadTestConfig(t, cfg); err != nil {
		t.Fatal(err)
	}
}

func TestComputerUseStaysOnEveryProjectedChat(t *testing.T) {
	coding := t.TempDir()
	cfg := multiAgentConfig(t)
	cfg.ComputerUse = true
	cfg.MCPServers = map[string]codex.MCPServer{ComputerUseMCPServer: {Command: "/reviewed/cua"}, "node_repl": {Command: "/reviewed/node"}}
	cfg.Agents[0].WorkDir = coding
	cfg.InteractiveConfig = &codex.ReviewedInteractiveConfig{CodexHome: "/existing/home", WorkDir: cfg.WorkDir, Binary: "/pinned/codex", Version: "0.153.1", DisabledPlugins: []string{ShadowingComputerUsePlugin}}
	loaded, err := loadTestConfig(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	codingChat := loaded.ForAgent(loaded.Agents[0])
	family := loaded.ForAgent(loaded.Agents[2])
	if !codingChat.ComputerUse || !family.ComputerUse {
		t.Fatal("computer_use is not service-wide")
	}
	if codingChat.MCPServers[ComputerUseMCPServer].Command != "/reviewed/cua" || family.MCPServers[ComputerUseMCPServer].Command != "/reviewed/cua" {
		t.Fatal("cua_repl is not on every chat")
	}
	if codingChat.InteractiveConfig == nil || family.InteractiveConfig == nil {
		t.Fatal("coding DM dropped the shared interactive contract")
	}
}
