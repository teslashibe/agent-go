package config

import (
	"errors"
	"net"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
)

// Google grants exact account aliases to named, authenticated chat bindings.
// Missing accounts/chats have no access. Credentials are operator managed.
type Google struct {
	ConfigPath   string              `json:"config_path"`
	AccountChats map[string][]string `json:"account_chats"`
	OAuth        *GoogleOAuth        `json:"oauth,omitempty"`
}

// GoogleOAuth explicitly opts named private chats into connecting existing slots.
type GoogleOAuth struct {
	PublicBaseURL string   `json:"public_base_url"`
	ListenAddr    string   `json:"listen_addr"`
	Chats         []string `json:"chats"`
}

var googleAlias = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

func (c Config) validateGoogle() error {
	if c.Google == nil {
		return nil
	}
	if !filepath.IsAbs(c.Google.ConfigPath) {
		return errors.New("google.config_path must be absolute")
	}
	names := map[string]bool{}
	for _, a := range c.Agents {
		names[a.Name] = true
	}
	for alias, chats := range c.Google.AccountChats {
		if !googleAlias.MatchString(alias) {
			return errors.New("Google account keys must be canonical lowercase aliases, not emails or wildcards")
		}
		seen := map[string]bool{}
		for _, chat := range chats {
			if !names[chat] || seen[chat] {
				return errors.New("Google account chat grants must name distinct configured agents")
			}
			seen[chat] = true
		}
	}
	if o := c.Google.OAuth; o != nil {
		u, err := url.Parse(o.PublicBaseURL)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
			return errors.New("google.oauth.public_base_url must be an HTTPS origin")
		}
		host, port, err := net.SplitHostPort(o.ListenAddr)
		p, _ := strconv.Atoi(port)
		if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() || p < 1 || p > 65535 {
			return errors.New("google.oauth.listen_addr must use a loopback IP and fixed port")
		}
		seen := map[string]bool{}
		for _, name := range o.Chats {
			if !names[name] || seen[name] {
				return errors.New("Google OAuth chats must be distinct configured private chats")
			}
			seen[name] = true
			for _, chats := range c.Google.AccountChats {
				if slices.Contains(chats, name) && len(chats) != 1 {
					return errors.New("Google OAuth slots must be granted only to their exact private chat")
				}
			}
			for _, a := range c.Agents {
				if a.Name == name && a.Group {
					return errors.New("Google OAuth is private-DM only")
				}
			}
		}
	}
	return nil
}

// GoogleConnectAllowed projects opt-in from the exact chat binding.
func (c Config) GoogleConnectAllowed() bool {
	if c.Google == nil || c.Google.OAuth == nil || c.Group {
		return false
	}
	for _, a := range c.Agents {
		if a.ChatID == c.ChatID && a.ChatGUID == c.ChatGUID && a.Group == c.Group && slices.Contains(c.Google.OAuth.Chats, a.Name) {
			return true
		}
	}
	return false
}

// GoogleAliases resolves grants from the exact operator-configured chat binding,
// never a sender, model argument, account default, or a group/DM heuristic.
func (c Config) GoogleAliases() []string {
	var aliases []string
	if c.Google == nil {
		return aliases
	}
	for _, a := range c.Agents {
		if a.ChatID != c.ChatID || a.ChatGUID != c.ChatGUID || a.Group != c.Group {
			continue
		}
		for alias, chats := range c.Google.AccountChats {
			if slices.Contains(chats, a.Name) {
				aliases = append(aliases, alias)
			}
		}
	}
	slices.Sort(aliases)
	return slices.Compact(aliases)
}

// HasGoogleGrants avoids credential initialization when all grants are absent.
func (c Config) HasGoogleGrants() bool {
	if c.Google == nil {
		return false
	}
	for _, chats := range c.Google.AccountChats {
		if len(chats) > 0 {
			return true
		}
	}
	return false
}
