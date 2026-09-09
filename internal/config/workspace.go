package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

const teslashibeModulePrefix = "module github.com/teslashibe/"

var (
	agentStackPackages = []string{"agent-go", "notes", "imessage", "codex"}
	agentPackageName   = regexp.MustCompile(`^agent-[a-z0-9]+(?:-[a-z0-9]+)*$`)
)

// AllowedAgentPackage reports whether dir is an agent-stack checkout whose
// go.mod path is github.com/teslashibe/<dir>. New packages must be agent-*.
func AllowedAgentPackage(dir, module string) bool {
	if dir == "" || strings.ContainsAny(dir, `/\`) || module != "github.com/teslashibe/"+dir {
		return false
	}
	return slices.Contains(agentStackPackages, dir) || agentPackageName.MatchString(dir)
}

// CodingModules lists agent-stack checkouts only for a non-group chat that
// set its own work_dir. Inherited service workspaces stay empty.
func CodingModules(group, explicit bool, dir string) []string {
	if group || !explicit {
		return nil
	}
	return WorkspaceModules(dir)
}

// WorkspaceModules lists sibling agent-stack checkouts under dir.
func WorkspaceModules(dir string) []string {
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var found []string
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || strings.HasPrefix(name, ".") {
			continue
		}
		module, ok := modulePath(filepath.Join(dir, name, "go.mod"))
		if !ok || !AllowedAgentPackage(name, module) {
			continue
		}
		found = append(found, name)
	}
	slices.Sort(found)
	return found
}

func modulePath(path string) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 || len(data) > 8<<10 {
		return "", false
	}
	line, _, _ := strings.Cut(string(data), "\n")
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, teslashibeModulePrefix) {
		return "", false
	}
	module := strings.TrimSpace(strings.TrimPrefix(line, "module "))
	if module == "" || strings.ContainsAny(module, " \t") {
		return "", false
	}
	return module, true
}

func validateWorkspace(statePath, workDir string) error {
	if !filepath.IsAbs(statePath) || !filepath.IsAbs(workDir) {
		return fmt.Errorf("state_path and work_dir must be absolute")
	}
	relative, err := filepath.Rel(workDir, statePath)
	if err != nil {
		return fmt.Errorf("compare state and workspace paths: %w", err)
	}
	if relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("state_path must be outside the agent workspace")
	}
	info, err := os.Stat(workDir)
	if err != nil {
		return fmt.Errorf("inspect work_dir: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("work_dir must be a directory")
	}
	return nil
}
