package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/teslashibe/codex"
)

func mcpServersSHA256(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var cfg struct {
		MCPServers map[string]codex.MCPServer `json:"mcp_servers"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return "", err
	}
	sum, err := codex.MCPServersSHA256(cfg.MCPServers)
	if err != nil {
		return "", err
	}
	return sum, nil
}

func printMCPServersSHA256(path string) error {
	sum, err := mcpServersSHA256(path)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(os.Stdout, sum)
	return err
}
