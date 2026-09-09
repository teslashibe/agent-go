package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/teslashibe/agent-go/internal/store"
	"github.com/teslashibe/codex"
)

// Config describes authorized chats and one Codex workspace.
// Source identifies this exact Mac/account/database generation. Change it and
// start with a new state database after restoring or replacing chat.db.
type Config struct {
	Google     *Google                    `json:"google,omitempty"`
	MCPServers map[string]codex.MCPServer `json:"mcp_servers,omitempty"`
	// ExecutionPolicy controls Codex command sandboxing for every chat.
	// Empty preserves the historical YOLO behavior.
	ExecutionPolicy ExecutionPolicy `json:"execution_policy,omitempty"`
	// ComputerUse is a service-wide opt-in. Every chat inherits the same
	// cua_repl MCP server and, when InteractiveConfig is set, the same
	// computer-use plugins. A coding WorkDir is extra workspace access, not
	// a different tool set.
	ComputerUse bool `json:"computer_use,omitempty"`
	// DocumentAttachments requests local attachment metadata and extracts
	// bounded text from documents attached to authenticated inbound messages.
	DocumentAttachments bool `json:"document_attachments,omitempty"`
	// Explicit operator opt-in. The native client validates this reviewed
	// contract before launch; absent means the existing exec path unchanged.
	InteractiveConfig *codex.ReviewedInteractiveConfig `json:"interactive_config,omitempty"`

	Agents          []Agent         `json:"agents,omitempty"`
	NotesHelper     string          `json:"notes_helper,omitempty"`
	OwnerNotes      bool            `json:"owner_notes,omitempty"`
	Profiles        []store.Profile `json:"profiles,omitempty"`
	Transport       string          `json:"transport"`
	Host            string          `json:"host"`
	ImsgPath        string          `json:"imsg_path"`
	Owner           string          `json:"owner,omitempty"` // Legacy direct-chat fallback only.
	AllowedSenders  []string        `json:"allowed_senders,omitempty"`
	Group           bool            `json:"group,omitempty"`
	ChatID          int64           `json:"chat_id,omitempty"`
	ChatGUID        string          `json:"chat_guid,omitempty"`
	Source          string          `json:"source"`
	StatePath       string          `json:"state_path"`
	WorkDir         string          `json:"work_dir"`
	CodexPath       string          `json:"codex_path"`
	Model           string          `json:"model"`
	ReasoningEffort string          `json:"reasoning_effort"`
	ServiceTier     string          `json:"service_tier"`
}

type ExecutionPolicy string

const (
	ExecutionYOLO           ExecutionPolicy = "yolo"
	ExecutionReadOnly       ExecutionPolicy = "read-only"
	ExecutionWorkspaceWrite ExecutionPolicy = "workspace-write"
)

// CodexPolicy maps the validated service policy to the runner policy.
func (p ExecutionPolicy) CodexPolicy() codex.ExecutionPolicy {
	switch p {
	case ExecutionReadOnly:
		return codex.ExecutionReadOnly
	case ExecutionWorkspaceWrite:
		return codex.ExecutionWorkspaceWrite
	default:
		return codex.ExecutionYOLO
	}
}

// Agent binds one durable Codex conversation to one exact iMessage chat.
type Agent struct {
	// OwnerNotes grants the sole DM sender access to the Mac account's
	// unprotected Notes. Configure only for the account owner, never a group.
	OwnerNotes     bool            `json:"owner_notes,omitempty"`
	Name           string          `json:"name"`
	Owner          string          `json:"owner,omitempty"` // Legacy direct-chat fallback only.
	AllowedSenders []string        `json:"allowed_senders,omitempty"`
	Group          bool            `json:"group"`
	ChatID         int64           `json:"chat_id"`
	ChatGUID       string          `json:"chat_guid"`
	Profiles       []store.Profile `json:"profiles,omitempty"`
	// WorkDir overrides the service workspace for this chat. Set it only on
	// a coding DM; family chats must inherit the scratch work_dir. Use a
	// parent of agent-stack checkouts (agent-go, notes, imessage, codex, or
	// agent-*). New private agent-* packages may be added as siblings.
	// This is extra workspace access on the same interactive contract.
	WorkDir string `json:"work_dir,omitempty"`
}

var agentName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

// ForAgent projects chat-local fields without changing global service settings.
// MCPServers, ComputerUse, DocumentAttachments, and InteractiveConfig stay
// service-wide so every chat uses the same configured capabilities.
func (c Config) ForAgent(agent Agent) Config {
	c.Owner = agent.Owner
	c.AllowedSenders = slices.Clone(agent.AllowedSenders)
	c.Group = agent.Group
	c.ChatID = agent.ChatID
	c.ChatGUID = agent.ChatGUID
	c.Profiles = slices.Clone(agent.Profiles)
	c.OwnerNotes = agent.OwnerNotes
	if agent.WorkDir != "" {
		c.WorkDir = agent.WorkDir
	}
	return c
}

// Agent selects the sole configured agent, or an explicitly named agent.
func (c Config) Agent(name string) (Agent, error) {
	if len(c.Agents) == 0 {
		return Agent{}, errors.New("no agents configured")
	}
	if name == "" {
		if len(c.Agents) == 1 {
			return c.Agents[0], nil
		}
		return Agent{}, errors.New("agent name is required when multiple agents are configured")
	}
	for _, agent := range c.Agents {
		if agent.Name == name {
			return agent, nil
		}
	}
	return Agent{}, fmt.Errorf("unknown agent %q", name)
}

func Load(path string) (Config, error) {
	var cfg Config
	file, err := os.Open(path)
	if err != nil {
		return cfg, fmt.Errorf("open config: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err != nil {
		return cfg, fmt.Errorf("read config: %w", err)
	}
	if len(data) > 64<<10 {
		return cfg, errors.New("config exceeds 64 KiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("decode config: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return cfg, errors.New("config must contain one JSON object")
	}
	if cfg.Transport == "" {
		cfg.Transport = "ssh"
	}
	switch cfg.ExecutionPolicy {
	case "", ExecutionYOLO, ExecutionReadOnly, ExecutionWorkspaceWrite:
	default:
		return cfg, errors.New("execution_policy must be yolo, read-only, or workspace-write")
	}
	switch cfg.Transport {
	case "ssh":
		if cfg.Host == "" {
			return cfg, errors.New("host is required for ssh transport")
		}
	case "local":
	default:
		return cfg, errors.New("transport must be local or ssh")
	}
	if cfg.DocumentAttachments && cfg.Transport != "local" {
		return cfg, errors.New("document_attachments requires local transport")
	}
	if cfg.NotesHelper != "" && (cfg.Transport != "local" || !filepath.IsAbs(cfg.NotesHelper)) {
		return cfg, errors.New("notes_helper requires an absolute local native helper path")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return cfg, fmt.Errorf("decode config fields: %w", err)
	}
	hasAgents := false
	for field := range fields {
		hasAgents = hasAgents || strings.EqualFold(field, "agents")
	}
	if hasAgents {
		for field := range fields {
			switch strings.ToLower(field) {
			case "owner", "allowed_senders", "group", "chat_id", "chat_guid", "profiles", "owner_notes":
				return cfg, errors.New("agents cannot be combined with top-level chat or profile fields")
			}
		}
		if len(cfg.Agents) == 0 {
			return cfg, errors.New("agents must contain at least one agent")
		}
	} else {
		cfg.Agents = []Agent{{Name: "default", Owner: cfg.Owner, AllowedSenders: cfg.AllowedSenders, Group: cfg.Group, ChatID: cfg.ChatID, ChatGUID: cfg.ChatGUID, Profiles: cfg.Profiles, OwnerNotes: cfg.OwnerNotes}}
	}
	if cfg.Source == "" {
		return cfg, errors.New("source is required")
	}
	names := make(map[string]bool, len(cfg.Agents))
	chats := make(map[int64]bool, len(cfg.Agents))
	guids := make(map[string]bool, len(cfg.Agents))
	for i := range cfg.Agents {
		agent := &cfg.Agents[i]
		if !agentName.MatchString(agent.Name) {
			return cfg, errors.New("agent names must be 1-64 ASCII letters, digits, underscores or hyphens, starting with a letter or digit")
		}
		if names[agent.Name] {
			return cfg, fmt.Errorf("duplicate agent name %q", agent.Name)
		}
		if chats[agent.ChatID] {
			return cfg, fmt.Errorf("duplicate agent chat_id %d", agent.ChatID)
		}
		if guids[agent.ChatGUID] {
			return cfg, fmt.Errorf("duplicate agent chat_guid %q", agent.ChatGUID)
		}
		names[agent.Name], chats[agent.ChatID], guids[agent.ChatGUID] = true, true, true
		if agent.Owner != "" {
			if agent.Group || agent.AllowedSenders != nil {
				return cfg, errors.New("owner is only a direct-chat fallback; use allowed_senders alone")
			}
			agent.AllowedSenders = []string{agent.Owner}
		}
		if len(agent.AllowedSenders) == 0 || agent.ChatID <= 0 || agent.ChatGUID == "" {
			return cfg, errors.New("each agent requires allowed_senders, positive chat_id and chat_guid")
		}
		for _, sender := range agent.AllowedSenders {
			if strings.TrimSpace(sender) != sender || sender == "" || strings.ContainsAny(sender, "\r\n") {
				return cfg, errors.New("allowed_senders must contain nonempty exact identities without surrounding whitespace or newlines")
			}
		}
		slices.Sort(agent.AllowedSenders)
		agent.AllowedSenders = slices.Compact(agent.AllowedSenders)
		if !agent.Group && len(agent.AllowedSenders) != 1 {
			return cfg, fmt.Errorf("agent %q: direct chats require exactly one allowed sender", agent.Name)
		}
		if agent.OwnerNotes && (agent.Group || cfg.NotesHelper == "") {
			return cfg, errors.New("owner_notes requires a single-person DM and a native Notes helper")
		}
		if !ChatGUIDValid(agent.Group, agent.ChatGUID) {
			return cfg, errors.New("chat_guid must match group: any;+; for groups, iMessage;-; or any;-; for direct chats")
		}
		if err := store.ValidateProfiles(store.Source{AllowedSenders: agent.AllowedSenders, Group: agent.Group}, agent.Profiles); err != nil {
			return cfg, fmt.Errorf("agent %q: %w", agent.Name, err)
		}
		for j := range agent.Profiles {
			if agent.Profiles[j].TimeZone == "" {
				agent.Profiles[j].TimeZone = store.DefaultTimeZone
			}
		}
		if agent.WorkDir != "" {
			if err := validateWorkspace(cfg.StatePath, agent.WorkDir); err != nil {
				return cfg, fmt.Errorf("agent %q: %w", agent.Name, err)
			}
		}
	}
	if err := cfg.validateGoogle(); err != nil {
		return cfg, err
	}
	if err := cfg.validateComputerUse(); err != nil {
		return cfg, err
	}
	if len(cfg.Agents) == 1 {
		cfg = cfg.ForAgent(cfg.Agents[0])
	}
	if cfg.ImsgPath == "" {
		cfg.ImsgPath = "/opt/homebrew/bin/imsg"
	}
	if cfg.Transport == "local" && !filepath.IsAbs(cfg.ImsgPath) {
		return cfg, errors.New("imsg_path must be absolute for local transport")
	}
	if cfg.CodexPath == "" {
		cfg.CodexPath = "codex"
	}
	if err := validateWorkspace(cfg.StatePath, cfg.WorkDir); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// ChatGUIDValid reports whether guid matches the configured chat kind.
// Direct chats may be iMessage;-; or any;-;; groups are any;+;.
func ChatGUIDValid(group bool, guid string) bool {
	prefix := "iMessage;-;"
	if group {
		prefix = "any;+;"
	} else if strings.HasPrefix(guid, "any;-;") {
		prefix = "any;-;"
	}
	return strings.HasPrefix(guid, prefix) && len(guid) > len(prefix)
}
