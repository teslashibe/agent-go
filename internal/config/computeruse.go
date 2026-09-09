package config

import (
	"errors"
	"strings"
)

// ComputerUseMCPServer is the explicit unified-computer-use launcher whose js
// tool drives Chrome and the desktop from the exec/code-mode surface. It is
// the only computer-use registration; Codex Desktop's bundled computer-use
// client writes notify hooks into ~/.codex/config.toml, an unreviewed source,
// and needs the Desktop app's runtime service, so it is not used here.
const ComputerUseMCPServer = "cua_repl"

// ShadowingComputerUsePlugin redefines cua_repl with omit_tools_from
// code_mode/deferred. Enabling it hides js from ALL_TOOLS, so the model
// reports it has no browser. Interactive chats must keep it disabled.
const ShadowingComputerUsePlugin = "unified-computer-use@openai-bundled"

func (c Config) validateComputerUse() error {
	if !c.ComputerUse {
		return nil
	}
	server, ok := c.MCPServers[ComputerUseMCPServer]
	if !ok || strings.TrimSpace(server.Command) == "" {
		return errors.New("computer_use requires mcp_servers.cua_repl with a command")
	}
	if c.InteractiveConfig == nil {
		return nil
	}
	for _, id := range c.InteractiveConfig.EnabledPlugins {
		if id == ShadowingComputerUsePlugin {
			return errors.New("computer_use requires interactive_config not to enable " + ShadowingComputerUsePlugin)
		}
	}
	for _, id := range c.InteractiveConfig.DisabledPlugins {
		if id == ShadowingComputerUsePlugin {
			return nil
		}
	}
	return errors.New("computer_use requires interactive_config to disable " + ShadowingComputerUsePlugin)
}
