package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/teslashibe/agent-go/internal/store"
	"github.com/teslashibe/codex"
)

func TestExecutionPolicy(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value ExecutionPolicy
		want  codex.ExecutionPolicy
		valid bool
	}{
		{name: "default", want: codex.ExecutionYOLO, valid: true},
		{name: "yolo", value: ExecutionYOLO, want: codex.ExecutionYOLO, valid: true},
		{name: "read only", value: ExecutionReadOnly, want: codex.ExecutionReadOnly, valid: true},
		{name: "workspace write", value: ExecutionWorkspaceWrite, want: codex.ExecutionWorkspaceWrite, valid: true},
		{name: "account access", value: "account-access"},
		{name: "unknown", value: "unknown"},
		{name: "wrong case", value: "YOLO"},
		{name: "surrounding whitespace", value: " yolo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{
				Transport: "local", ExecutionPolicy: tc.value,
				Owner: "owner@example.test", ChatID: 1, ChatGUID: "iMessage;-;owner@example.test",
				Source: "test", StatePath: filepath.Join(t.TempDir(), "state.db"), WorkDir: t.TempDir(),
			}
			loaded, err := loadTestConfig(t, cfg)
			if (err == nil) != tc.valid {
				t.Fatalf("Load = %+v, %v", loaded, err)
			}
			if tc.valid && loaded.ExecutionPolicy.CodexPolicy() != tc.want {
				t.Fatalf("Codex policy = %q, want %q", loaded.ExecutionPolicy.CodexPolicy(), tc.want)
			}
		})
	}
}

func TestOwnerNotesRequiresExplicitDMGrant(t *testing.T) {
	cfg := Config{Transport: "local", Source: "test", StatePath: filepath.Join(t.TempDir(), "state.db"), WorkDir: t.TempDir(), NotesHelper: "/signed/notes", Agents: []Agent{
		{Name: "owner", ChatID: 1, ChatGUID: "iMessage;-;owner@example.test", AllowedSenders: []string{"owner@example.test"}, OwnerNotes: true},
		{Name: "group", ChatID: 2, ChatGUID: "any;+;group", Group: true, AllowedSenders: []string{"owner@example.test", "other@example.test"}},
	}}
	loaded, err := loadTestConfig(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.ForAgent(loaded.Agents[0]).OwnerNotes || loaded.ForAgent(loaded.Agents[1]).OwnerNotes {
		t.Fatal("owner grant leaked between chats")
	}
	cfg.Agents[1].OwnerNotes = true
	if _, err := loadTestConfig(t, cfg); err == nil {
		t.Fatal("owner grant admitted group")
	}
	cfg.Agents[1].OwnerNotes = false
	cfg.NotesHelper = ""
	if _, err := loadTestConfig(t, cfg); err == nil {
		t.Fatal("owner Notes admitted without helper")
	}
}

func TestReviewedInteractiveConfigLoadingIsExplicit(t *testing.T) {
	cfg := Config{Transport: "local", Owner: "owner@example.test", ChatID: 1, ChatGUID: "iMessage;-;owner@example.test", Source: "test", StatePath: filepath.Join(t.TempDir(), "state.db"), WorkDir: t.TempDir(), MCPServers: map[string]codex.MCPServer{"browser": {Command: "existing-browser", Env: map[string]string{"HOME_BINDING": "unchanged"}}}}
	for _, enabled := range []bool{false, true} {
		if enabled {
			cfg.InteractiveConfig = &codex.ReviewedInteractiveConfig{CodexHome: "/existing/home", WorkDir: cfg.WorkDir, Binary: "/pinned/codex", Version: "0.153.1", Bindings: map[string]codex.ReviewedMCPBinding{"harness": {Server: codex.MCPServer{Command: "/pinned/agent-go", Args: []string{"notes-mcp"}}, DynamicEnvKeys: []string{"AGENT_NOTES_SOCKET"}}}}
		}
		loaded, err := loadTestConfig(t, cfg)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(loaded.InteractiveConfig, cfg.InteractiveConfig) || !reflect.DeepEqual(loaded.MCPServers, cfg.MCPServers) {
			t.Fatal("reviewed config or browser map changed during load")
		}
		projected := loaded.ForAgent(loaded.Agents[0])
		if !reflect.DeepEqual(projected.InteractiveConfig, cfg.InteractiveConfig) || !reflect.DeepEqual(projected.MCPServers, cfg.MCPServers) {
			t.Fatal("reviewed config lost in chat projection")
		}
	}
}

func TestProfileConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, profiles string
		valid          bool
	}{
		{"defaults", `[{"id":"alex","sender":"a@example.test"},{"id":"sam","sender":"b@example.test"}]`, true},
		{"explicit", `[{"id":"sam","sender":"b@example.test","time_zone":"Europe/London"}]`, true},
		{"duplicate id", `[{"id":"sam","sender":"a@example.test"},{"id":"sam","sender":"b@example.test"}]`, false},
		{"not allowed", `[{"id":"sam","sender":"c@example.test"}]`, false},
		{"abbreviation", `[{"id":"sam","sender":"b@example.test","time_zone":"PST"}]`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.json")
			data := `{"transport":"local","allowed_senders":["a@example.test","b@example.test"],"group":true,"chat_id":1,"chat_guid":"any;+;test","source":"test","state_path":` + strconv.Quote(filepath.Join(dir, "state.db")) + `,"work_dir":` + strconv.Quote(t.TempDir()) + `,"profiles":` + tc.profiles + `}`
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path)
			if (err == nil) != tc.valid {
				t.Fatalf("%+v %v", cfg, err)
			}
			if tc.valid && !slices.Equal(cfg.Agents[0].Profiles, cfg.Profiles) {
				t.Fatalf("legacy group profiles not preserved on default agent: %+v", cfg)
			}
			if tc.name == "defaults" && (cfg.Profiles[0].TimeZone != "America/Los_Angeles" || cfg.Profiles[1].TimeZone != "America/Los_Angeles") {
				t.Fatal(cfg.Profiles)
			}
		})
	}
}

func TestLoadTransport(t *testing.T) {
	for _, tc := range []struct {
		name, transport, host, binary string
		valid                         bool
	}{
		{"default SSH", "", "mac", "", true},
		{"explicit SSH", "ssh", "mac", "", true},
		{"SSH requires host", "ssh", "", "", false},
		{"empty host does not select local", "", "", "", false},
		{"local defaults without host", "local", "", "", true},
		{"local ignores host", "local", "mac", "/opt/homebrew/bin/imsg", true},
		{"local literal path", "local", "", "/Applications/My Tools/imsg;literal", true},
		{"local requires absolute path", "local", "", "imsg", false},
		{"unknown transport", "http", "mac", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{Transport: tc.transport, Host: tc.host, ImsgPath: tc.binary,
				Owner: "owner@example.com", ChatID: 1, ChatGUID: "iMessage;-;owner@example.com", Source: "mac-1",
				StatePath: filepath.Join(t.TempDir(), "state.db"), WorkDir: t.TempDir()}
			data, err := json.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			loaded, err := Load(path)
			if (err == nil) != tc.valid {
				t.Fatalf("Load: %+v, %v", loaded, err)
			}
			if tc.valid {
				want := tc.transport
				if want == "" {
					want = "ssh"
				}
				if loaded.Transport != want {
					t.Fatalf("transport = %q, want %q", loaded.Transport, want)
				}
			}
		})
	}
}

func TestDocumentAttachmentsRequireLocalTransport(t *testing.T) {
	cfg := Config{
		Transport: "ssh", Host: "mac", DocumentAttachments: true,
		Owner: "owner@example.com", ChatID: 1, ChatGUID: "iMessage;-;owner@example.com", Source: "mac-1",
		StatePath: filepath.Join(t.TempDir(), "state.db"), WorkDir: t.TempDir(),
	}
	if _, err := loadTestConfig(t, cfg); err == nil || !strings.Contains(err.Error(), "document_attachments requires local transport") {
		t.Fatalf("remote document attachments accepted: %v", err)
	}
	cfg.Transport = "local"
	cfg.Host = ""
	loaded, err := loadTestConfig(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.DocumentAttachments || !loaded.ForAgent(loaded.Agents[0]).DocumentAttachments {
		t.Fatal("document attachment setting was not preserved service-wide")
	}
}

func TestLoadAllowedSenders(t *testing.T) {
	for _, tc := range []struct {
		name    string
		owner   string
		senders []string
		group   bool
		guid    string
		valid   bool
	}{
		{"group", "", []string{"second@example.com", "first@example.com", "second@example.com"}, true, "any;+;configured-group", true},
		{"direct list", "", []string{"first@example.com"}, false, "iMessage;-;first@example.com", true},
		{"direct any service", "", []string{"first@example.com"}, false, "any;-;first@example.com", true},
		{"direct multiple senders", "", []string{"first@example.com", "second@example.com"}, false, "iMessage;-;first@example.com", false},
		{"direct duplicate sender", "", []string{"first@example.com", "first@example.com"}, false, "iMessage;-;first@example.com", true},
		{"legacy direct", "first@example.com", nil, false, "iMessage;-;first@example.com", true},
		{"legacy group rejected", "first@example.com", nil, true, "any;+;configured-group", false},
		{"ambiguous owner", "first@example.com", []string{"first@example.com"}, false, "iMessage;-;first@example.com", false},
		{"missing senders", "", nil, true, "any;+;configured-group", false},
		{"empty sender", "", []string{""}, true, "any;+;configured-group", false},
		{"whitespace sender", "", []string{" first@example.com"}, true, "any;+;configured-group", false},
		{"newline sender", "", []string{"first\nsecond"}, true, "any;+;configured-group", false},
		{"group flag required", "", []string{"first@example.com"}, false, "any;+;configured-group", false},
		{"direct with group flag", "", []string{"first@example.com"}, true, "iMessage;-;first@example.com", false},
		{"group identifier required", "", []string{"first@example.com"}, true, "any;+;", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{Transport: "local", Owner: tc.owner, AllowedSenders: tc.senders, Group: tc.group,
				ChatID: 1, ChatGUID: tc.guid, Source: "test-mac", StatePath: filepath.Join(t.TempDir(), "state.db"), WorkDir: t.TempDir(),
				Model: "test-model", ReasoningEffort: "high", ServiceTier: "fast"}
			data, err := json.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			loaded, err := Load(path)
			if (err == nil) != tc.valid {
				t.Fatalf("Load = %+v, %v", loaded, err)
			}
			if !tc.valid {
				return
			}
			if loaded.Model != cfg.Model || loaded.ReasoningEffort != cfg.ReasoningEffort || loaded.ServiceTier != cfg.ServiceTier || loaded.Group != tc.group {
				t.Fatalf("lost configuration: %+v", loaded)
			}
			want := []string{"first@example.com"}
			if tc.group {
				want = append(want, "second@example.com")
			}
			if !slices.Equal(loaded.AllowedSenders, want) {
				t.Fatalf("senders = %v, want %v", loaded.AllowedSenders, want)
			}
		})
	}
}

func TestLoadMultipleAgents(t *testing.T) {
	cfg := multiAgentConfig(t)
	loaded, err := loadTestConfig(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Agents) != 3 || loaded.ChatID != 0 || loaded.Profiles != nil || loaded.AllowedSenders != nil {
		t.Fatalf("unexpected global chat configuration: %+v", loaded)
	}
	if _, err := loaded.Agent(""); err == nil {
		t.Fatal("unnamed selection accepted for multiple agents")
	}
	if _, err := loaded.Agent("missing"); err == nil {
		t.Fatal("unknown agent accepted")
	}
	for i, want := range cfg.Agents {
		got, err := loaded.Agent(want.Name)
		if err != nil || got.ChatID != want.ChatID || got.ChatGUID != want.ChatGUID {
			t.Fatalf("Agent(%q) = %+v, %v", want.Name, got, err)
		}
		projected := loaded.ForAgent(got)
		wantDir := loaded.WorkDir
		if got.WorkDir != "" {
			wantDir = got.WorkDir
		}
		if projected.Owner != got.Owner || projected.Group != got.Group || projected.ChatID != got.ChatID || projected.ChatGUID != got.ChatGUID || !slices.Equal(projected.AllowedSenders, got.AllowedSenders) || !slices.Equal(projected.Profiles, got.Profiles) || projected.WorkDir != wantDir {
			t.Fatalf("lost agent fields: %+v", projected)
		}
		global := projected
		global.Owner, global.AllowedSenders, global.Group, global.ChatID, global.ChatGUID, global.Profiles, global.WorkDir = loaded.Owner, loaded.AllowedSenders, loaded.Group, loaded.ChatID, loaded.ChatGUID, loaded.Profiles, loaded.WorkDir
		if !reflect.DeepEqual(global, loaded) {
			t.Fatalf("projection changed global configuration: %+v", projected)
		}
		if !got.Group && len(projected.Profiles) != 0 {
			t.Fatalf("group profiles leaked into DM %q", got.Name)
		}
		projected.AllowedSenders[0] = "mutated"
		if loaded.Agents[i].AllowedSenders[0] == "mutated" {
			t.Fatal("projected senders share mutable backing storage")
		}
	}
	group, _ := loaded.Agent("family")
	if group.Profiles[0].TimeZone != store.DefaultTimeZone || group.Profiles[1].TimeZone != "Europe/London" {
		t.Fatalf("incorrect profile timezones: %+v", group.Profiles)
	}
	projected := loaded.ForAgent(group)
	projected.Profiles[0].ID = "mutated"
	if loaded.Agents[2].Profiles[0].ID == "mutated" {
		t.Fatal("projected profiles share mutable backing storage")
	}
	dm := projected.ForAgent(loaded.Agents[0])
	if len(dm.Profiles) != 0 {
		t.Fatal("reprojecting group configuration leaked profiles into a DM")
	}
}

func TestLoadAgentValidation(t *testing.T) {
	for name, mutate := range map[string]func(*Config){
		"duplicate name":               func(c *Config) { c.Agents[1].Name = c.Agents[0].Name },
		"duplicate chat ID":            func(c *Config) { c.Agents[1].ChatID = c.Agents[0].ChatID },
		"duplicate chat GUID":          func(c *Config) { c.Agents[1].ChatGUID = c.Agents[0].ChatGUID },
		"empty name":                   func(c *Config) { c.Agents[0].Name = "" },
		"unsafe path name":             func(c *Config) { c.Agents[0].Name = "../private" },
		"absolute path name":           func(c *Config) { c.Agents[0].Name = "/private" },
		"backslash name":               func(c *Config) { c.Agents[0].Name = `private\other` },
		"whitespace name":              func(c *Config) { c.Agents[0].Name = "private " },
		"newline name":                 func(c *Config) { c.Agents[0].Name = "private\nother" },
		"control name":                 func(c *Config) { c.Agents[0].Name = "private\x00other" },
		"long name":                    func(c *Config) { c.Agents[0].Name = strings.Repeat("a", 65) },
		"missing source":               func(c *Config) { c.Source = "" },
		"invalid chat ID":              func(c *Config) { c.Agents[0].ChatID = 0 },
		"missing chat GUID":            func(c *Config) { c.Agents[0].ChatGUID = "" },
		"mismatched chat GUID":         func(c *Config) { c.Agents[0].ChatGUID = "any;+;dm" },
		"missing senders":              func(c *Config) { c.Agents[0].Owner = "" },
		"ambiguous owner":              func(c *Config) { c.Agents[0].AllowedSenders = []string{"a@example.test"} },
		"group owner":                  func(c *Config) { c.Agents[2].Owner = "a@example.test" },
		"DM multiple senders":          func(c *Config) { c.Agents[1].AllowedSenders = []string{"a@example.test", "b@example.test"} },
		"DM profiles":                  func(c *Config) { c.Agents[0].Profiles = []store.Profile{{ID: "a", Sender: "a@example.test"}} },
		"profile from different agent": func(c *Config) { c.Agents[2].AllowedSenders = []string{"b@example.test"} },
		"duplicate profile IDs":        func(c *Config) { c.Agents[2].Profiles[1].ID = "a" },
		"duplicate profile senders":    func(c *Config) { c.Agents[2].Profiles[1].Sender = "a@example.test" },
		"unsafe profile ID":            func(c *Config) { c.Agents[2].Profiles[0].ID = "../a" },
		"invalid profile timezone":     func(c *Config) { c.Agents[2].Profiles[0].TimeZone = "PST" },
		"global profiles":              func(c *Config) { c.Profiles = c.Agents[2].Profiles },
		"global chat":                  func(c *Config) { c.ChatID = 4 },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := multiAgentConfig(t)
			mutate(&cfg)
			if _, err := loadTestConfig(t, cfg); err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
}

func TestLoadRejectsMixedAgentFieldPresence(t *testing.T) {
	for _, field := range []string{"owner", "allowed_senders", "group", "chat_id", "chat_guid", "profiles", "Profiles", "CHAT_ID"} {
		t.Run(field, func(t *testing.T) {
			data, err := json.Marshal(multiAgentConfig(t))
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]any
			if err := json.Unmarshal(data, &fields); err != nil {
				t.Fatal(err)
			}
			fields[field] = nil
			if _, err := loadTestConfig(t, fields); err == nil {
				t.Fatal("explicit empty top-level field accepted with agents")
			}
		})
	}
	for _, agents := range []any{nil, []Agent{}} {
		if _, err := loadTestConfig(t, map[string]any{"transport": "local", "agents": agents}); err == nil {
			t.Fatal("empty agents accepted")
		}
	}
}

func TestLoadSoleAndLegacyAgent(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		cfg := multiAgentConfig(t)
		cfg.Agents = cfg.Agents[:1]
		wantName := cfg.Agents[0].Name
		if legacy {
			cfg = cfg.ForAgent(cfg.Agents[0])
			cfg.Agents = nil
			wantName = "default"
		}
		loaded, err := loadTestConfig(t, cfg)
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"", wantName} {
			agent, err := loaded.Agent(name)
			if err != nil || agent.Name != wantName || agent.ChatID != loaded.ChatID || agent.ChatGUID != loaded.ChatGUID || agent.Owner != loaded.Owner || !slices.Equal(agent.AllowedSenders, loaded.AllowedSenders) {
				t.Fatalf("legacy=%v Agent(%q) = %+v, %v", legacy, name, agent, err)
			}
		}
	}
	if _, err := (Config{}).Agent(""); err == nil {
		t.Fatal("selected an agent from an empty configuration")
	}
}

func multiAgentConfig(t *testing.T) Config {
	t.Helper()
	return Config{
		Transport: "local", Host: "mac", Source: "test-mac", StatePath: filepath.Join(t.TempDir(), "state.db"), WorkDir: t.TempDir(),
		Model: "test-model", ReasoningEffort: "high", ServiceTier: "fast", NotesHelper: "/usr/local/bin/notes-helper",
		MCPServers: map[string]codex.MCPServer{"test": {}},
		Agents: []Agent{
			{Name: "private-A_1", Owner: "a@example.test", ChatID: 1, ChatGUID: "iMessage;-;a@example.test"},
			{Name: "private-b", AllowedSenders: []string{"b@example.test"}, ChatID: 2, ChatGUID: "iMessage;-;b@example.test"},
			{Name: "family", AllowedSenders: []string{"b@example.test", "a@example.test", "b@example.test"}, Group: true, ChatID: 3, ChatGUID: "any;+;family",
				Profiles: []store.Profile{{ID: "a", Sender: "a@example.test"}, {ID: "b", Sender: "b@example.test", TimeZone: "Europe/London"}}},
		},
	}
}

func loadTestConfig(t *testing.T, cfg any) (Config, error) {
	t.Helper()
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return Load(path)
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	body := `{"host":"mac","owner":"owner@example.com","chat_id":1,"chat_guid":"iMessage;-;owner@example.com","source":"mac-1","state_path":"` + filepath.Join(t.TempDir(), "state.db") + `","work_dir":"` + dir + `"}`
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Transport != "ssh" || cfg.CodexPath != "codex" || cfg.ImsgPath != "/opt/homebrew/bin/imsg" {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	for _, body := range []string{body + ` {}`, `{"unexpected":true}`, `{}`, `null`} {
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil {
			t.Errorf("accepted invalid config %q", body)
		}
	}
}

func TestWorkspaceModules(t *testing.T) {
	if got := WorkspaceModules(""); len(got) != 0 {
		t.Fatal(got)
	}
	root := t.TempDir()
	if got := WorkspaceModules(root); len(got) != 0 {
		t.Fatal(got)
	}
	if err := os.Mkdir(filepath.Join(root, "notes"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes", "go.mod"), []byte("module github.com/teslashibe/notes\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "agent-routing"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "agent-routing", "go.mod"), []byte("module github.com/teslashibe/agent-routing\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "routing"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "routing", "go.mod"), []byte("module github.com/teslashibe/routing\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "vendor-lib"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "vendor-lib", "go.mod"), []byte("module example.com/vendor-lib\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".hidden"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".hidden", "go.mod"), []byte("module github.com/teslashibe/hidden\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := WorkspaceModules(root); !slices.Equal(got, []string{"agent-routing", "notes"}) {
		t.Fatal(got)
	}
	if AllowedAgentPackage("routing", "github.com/teslashibe/routing") || !AllowedAgentPackage("notes", "github.com/teslashibe/notes") {
		t.Fatal("allowlist")
	}
	if got := CodingModules(false, false, root); len(got) != 0 {
		t.Fatal(got)
	}
	if got := CodingModules(true, true, root); len(got) != 0 {
		t.Fatal(got)
	}
	if got := CodingModules(false, true, root); !slices.Equal(got, []string{"agent-routing", "notes"}) {
		t.Fatal(got)
	}
}

func TestAgentWorkDir(t *testing.T) {
	cfg := multiAgentConfig(t)
	coding := t.TempDir()
	cfg.Agents[0].WorkDir = coding
	cfg.InteractiveConfig = &codex.ReviewedInteractiveConfig{CodexHome: "/existing/home", WorkDir: cfg.WorkDir, Binary: "/pinned/codex", Version: "0.153.1"}
	loaded, err := loadTestConfig(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	projected := loaded.ForAgent(loaded.Agents[0])
	family := loaded.ForAgent(loaded.Agents[1])
	if projected.WorkDir != coding || family.WorkDir != loaded.WorkDir {
		t.Fatalf("work_dir family=%q coding=%q", family.WorkDir, projected.WorkDir)
	}
	if projected.InteractiveConfig == nil || family.InteractiveConfig == nil || !reflect.DeepEqual(projected.InteractiveConfig, family.InteractiveConfig) {
		t.Fatal("coding DM dropped or changed the shared interactive contract")
	}
	cfg.Agents[0].WorkDir = "relative"
	if _, err := loadTestConfig(t, cfg); err == nil {
		t.Fatal("accepted relative agent work_dir")
	}
	cfg.Agents[0].WorkDir = filepath.Join(t.TempDir(), "missing")
	if _, err := loadTestConfig(t, cfg); err == nil {
		t.Fatal("accepted missing agent work_dir")
	}
	inside := t.TempDir()
	cfg.StatePath = filepath.Join(inside, "state.db")
	cfg.Agents[0].WorkDir = inside
	if _, err := loadTestConfig(t, cfg); err == nil {
		t.Fatal("accepted state inside agent work_dir")
	}
}
