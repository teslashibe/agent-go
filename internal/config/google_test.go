package config

import "testing"

func TestGoogleOAuthPrivateExactOptIn(t *testing.T) {
	dm := Agent{Name: "dm", ChatID: 1, ChatGUID: "iMessage;-;person"}
	group := Agent{Name: "group", ChatID: 2, ChatGUID: "any;+;group", Group: true}
	cfg := Config{Agents: []Agent{dm, group}, Google: &Google{ConfigPath: "/private/config.json", AccountChats: map[string][]string{"personal": {"dm"}}, OAuth: &GoogleOAuth{PublicBaseURL: "https://phone.example", ListenAddr: "127.0.0.1:8766", Chats: []string{"dm"}}}}
	if err := cfg.validateGoogle(); err != nil {
		t.Fatal(err)
	}
	if !cfg.ForAgent(dm).GoogleConnectAllowed() || cfg.ForAgent(group).GoogleConnectAllowed() {
		t.Fatal("wrong opt-in projection")
	}
	other := dm
	other.ChatGUID = "other"
	if cfg.ForAgent(other).GoogleConnectAllowed() {
		t.Fatal("cross-chat opt-in")
	}
	cfg.Google.AccountChats["personal"] = []string{"dm", "group"}
	if cfg.validateGoogle() == nil {
		t.Fatal("shared OAuth slot accepted")
	}
	cfg.Google.AccountChats["personal"] = []string{"dm"}
	for _, chats := range [][]string{{"group"}, {"missing"}, {"dm", "dm"}} {
		cfg.Google.OAuth.Chats = chats
		if cfg.validateGoogle() == nil {
			t.Fatal(chats)
		}
	}
	cfg.Google.OAuth.Chats = []string{"dm"}
	for _, addr := range []string{"0.0.0.0:8766", "localhost:8766", "127.0.0.1:0", "[::]:8766"} {
		cfg.Google.OAuth.ListenAddr = addr
		if cfg.validateGoogle() == nil {
			t.Fatal(addr)
		}
	}
}

func TestGoogleExactChatProjection(t *testing.T) {
	dm := Agent{Name: "dm", ChatID: 1, ChatGUID: "iMessage;-;owner", Group: false}
	group := Agent{Name: "family", ChatID: 2, ChatGUID: "any;+;family", Group: true}
	cfg := Config{Agents: []Agent{dm, group}, Google: &Google{ConfigPath: "/operator/google/config.json", AccountChats: map[string][]string{"personal": {"dm"}, "family": {"dm", "family"}, "work": {}}}}
	if err := cfg.validateGoogle(); err != nil {
		t.Fatal(err)
	}
	if got := cfg.ForAgent(dm).GoogleAliases(); len(got) != 2 || got[0] != "family" || got[1] != "personal" {
		t.Fatal(got)
	}
	if got := cfg.ForAgent(group).GoogleAliases(); len(got) != 1 || got[0] != "family" {
		t.Fatal(got)
	}
	for _, a := range []Agent{{Name: "dm", ChatID: 1, ChatGUID: "iMessage;-;other"}, {Name: "dm", ChatID: 1, ChatGUID: dm.ChatGUID, Group: true}, {Name: "dm", ChatID: 9, ChatGUID: dm.ChatGUID}} {
		if got := cfg.ForAgent(a).GoogleAliases(); len(got) != 0 {
			t.Fatal(got)
		}
	}
	cfg.Google = nil
	if len(cfg.ForAgent(dm).GoogleAliases()) != 0 {
		t.Fatal("nil policy grants access")
	}
}

func TestGoogleRejectsInvalidGrantConfig(t *testing.T) {
	for _, grants := range []map[string][]string{{"*": {"dm"}}, {"Work": {"dm"}}, {"x@example.com": {"dm"}}, {"work": {"missing"}}, {"work": {"dm", "dm"}}} {
		cfg := Config{Agents: []Agent{{Name: "dm"}}, Google: &Google{ConfigPath: "/config.json", AccountChats: grants}}
		if cfg.validateGoogle() == nil {
			t.Fatal(grants)
		}
	}
	if (Config{Google: &Google{ConfigPath: "relative"}}).validateGoogle() == nil {
		t.Fatal("relative path accepted")
	}
}
