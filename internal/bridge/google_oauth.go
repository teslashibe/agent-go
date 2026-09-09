package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/teslashibe/agent-go/internal/config"
	google "github.com/teslashibe/google-go"
	"golang.org/x/oauth2"
	googleoauth "golang.org/x/oauth2/google"
)

const googleCallbackPath = "/oauth2callback"

var connectScopes = []string{
	"https://www.googleapis.com/auth/gmail.readonly",
	"https://www.googleapis.com/auth/calendar.readonly",
	"https://www.googleapis.com/auth/drive.readonly",
}

type googlePending struct {
	alias, verifier string
	expires         time.Time
}

type googleOAuth struct {
	mu      sync.Mutex
	client  *GoogleClient
	config  *oauth2.Config
	slots   map[string]google.AccountConfig
	pending map[string]googlePending
	now     func() time.Time
	// HTTP transport is injectable for offline protocol tests, never model input.
	httpClient *http.Client
}

// StartGoogleOAuth owns a separate loopback listener, never the MCP/admin server.
// Configuration and slots are immutable until connection restart.
func (c *GoogleClient) StartGoogleOAuth(ctx context.Context, settings config.GoogleOAuth) (func(), error) {
	o, err := c.newOAuth(settings.PublicBaseURL)
	if err != nil {
		return nil, err
	}
	host, _, err := net.SplitHostPort(settings.ListenAddr)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return nil, errors.New("Google OAuth listener must be loopback-only")
	}
	ln, err := net.Listen("tcp", settings.ListenAddr)
	if err != nil {
		return nil, errors.New("Google OAuth listener unavailable")
	}
	ctx, cancel := context.WithCancel(ctx)
	c.oauth = o // Set before bridges/workers start.
	srv := &http.Server{Handler: o, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 40 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192, BaseContext: func(net.Listener) context.Context { return ctx }}
	doneServe := make(chan struct{})
	go func() { defer close(doneServe); _ = srv.Serve(ln) }()
	return func() {
		cancel()
		shutdownCtx, done := context.WithTimeout(context.Background(), 35*time.Second)
		defer done()
		_ = srv.Shutdown(shutdownCtx)
		_ = srv.Close()
		<-doneServe
	}, nil
}

func (c *GoogleClient) newOAuth(base string) (*googleOAuth, error) {
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("Google OAuth requires a public HTTPS origin")
	}
	dir := filepath.Dir(c.path)
	for _, path := range []string{dir, c.path, filepath.Join(dir, "oauth-keys.json")} {
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
			return nil, errors.New("Google OAuth config directory and files must be private (0700/0600), not symlinks")
		}
	}
	keys := filepath.Join(dir, "oauth-keys.json")
	data, err := os.ReadFile(keys)
	if err != nil {
		return nil, errors.New("Google OAuth keys unavailable")
	}
	var kind struct {
		Web       json.RawMessage `json:"web"`
		Installed json.RawMessage `json:"installed"`
	}
	if json.Unmarshal(data, &kind) != nil || len(kind.Web) == 0 || len(kind.Installed) != 0 {
		return nil, errors.New("Google OAuth requires Web application credentials")
	}
	cfg, err := google.LoadOAuth2Config(keys, connectScopes)
	if err != nil {
		return nil, errors.New("Google OAuth keys invalid")
	}
	// Only Google's endpoints; never trust endpoints supplied through tool input.
	cfg.Endpoint = googleoauth.Endpoint
	cfg.RedirectURL = strings.TrimSuffix(base, "/") + googleCallbackPath
	data, err = os.ReadFile(c.path)
	var slots google.Config
	if err != nil || json.Unmarshal(data, &slots) != nil {
		return nil, errors.New("Google OAuth slots unavailable")
	}
	for alias, slot := range slots.Accounts {
		address, err := mail.ParseAddress(slot.Email)
		if err != nil || address.Address != slot.Email || slot.Email == "" {
			return nil, errors.New("Google OAuth slots require exact account emails")
		}
		// Fixed upstream default layout avoids shared/custom token destinations.
		if slot.TokenPath != "" && slot.TokenPath != filepath.Join("accounts", alias, "token.json") {
			return nil, errors.New("Google OAuth slots must use default token paths")
		}
		if alias == "" || strings.ContainsAny(alias, "/\\.") {
			return nil, errors.New("Google OAuth slot alias invalid")
		}
	}
	return &googleOAuth{client: c, config: cfg, slots: slots.Accounts, pending: map[string]googlePending{}, now: time.Now, httpClient: &http.Client{Timeout: 25 * time.Second}}, nil
}

func (b *Bridge) EnableGoogleConnect() error {
	if b.config.Source.Group || b.config.Source.ChatID <= 0 || b.config.Source.ChatGUID == "" || len(b.config.Source.AllowedSenders) != 1 || b.google == nil {
		return errors.New("Google connection requires an authorized private DM")
	}
	c, ok := b.google.client.(*GoogleClient)
	if !ok || c.oauth == nil {
		return errors.New("Google OAuth not configured")
	}
	b.google.connect = c.oauth
	return nil
}

func (o *googleOAuth) link(alias string) (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, ok := o.slots[alias]; !ok {
		return "", errors.New("Google slot unavailable")
	}
	if _, err := os.Lstat(o.tokenPath(alias)); !errors.Is(err, os.ErrNotExist) {
		return "", errors.New("Google slot already connected; operator reset required")
	}
	for state, pending := range o.pending {
		if !o.now().Before(pending.expires) {
			delete(o.pending, state)
		}
	}
	if len(o.pending) >= 128 {
		return "", errors.New("Too many pending Google connections; retry later")
	}
	state, verifier := oauth2.GenerateVerifier(), oauth2.GenerateVerifier()
	o.pending[state] = googlePending{alias: alias, verifier: verifier, expires: o.now().Add(10 * time.Minute)}
	return o.config.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.SetAuthURLParam("prompt", "consent select_account"), oauth2.SetAuthURLParam("login_hint", o.slots[alias].Email), oauth2.SetAuthURLParam("include_granted_scopes", "false"), oauth2.S256ChallengeOption(verifier)), nil
}

func (o *googleOAuth) tokenPath(alias string) string {
	return filepath.Join(filepath.Dir(o.client.path), "accounts", alias, "token.json")
}

func (o *googleOAuth) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.URL.Path != googleCallbackPath {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(q["state"]) != 1 || len(q["code"]) > 1 || len(q["error"]) > 1 {
		http.Error(w, "Invalid Google callback", 400)
		return
	}
	o.mu.Lock()
	pending, ok := o.pending[q.Get("state")]
	if ok {
		delete(o.pending, q.Get("state"))
	} // Atomic consumption before exchange, including denial/error.
	o.mu.Unlock()
	if !ok || !o.now().Before(pending.expires) {
		http.Error(w, "Google link expired or already used. Request a new link in your private chat.", 400)
		return
	}
	if q.Get("error") != "" || q.Get("code") == "" {
		http.Error(w, "Google connection not completed. Request a new link in your private chat.", 400)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	ctx = context.WithValue(ctx, oauth2.HTTPClient, o.httpClient)
	token, err := o.config.Exchange(ctx, q.Get("code"), oauth2.VerifierOption(pending.verifier))
	if err == nil {
		// Google may report previously granted scopes despite this request. Never
		// persist a reported broader or incomplete grant for this read-only flow.
		if granted, ok := token.Extra("scope").(string); ok {
			scopes := strings.Fields(granted)
			if len(scopes) != len(connectScopes) {
				err = errors.New("unexpected Google scopes")
			}
			for _, scope := range connectScopes {
				if !slices.Contains(scopes, scope) {
					err = errors.New("unexpected Google scopes")
				}
			}
		}
	}
	if err == nil && token.RefreshToken != "" && token.AccessToken != "" {
		err = o.verifyAndSave(ctx, pending.alias, token)
	} else {
		err = errors.New("Google authorization failed")
	}
	if err != nil {
		http.Error(w, "Google connection failed. No account was replaced. Ask your operator to check the slot, then request a new link.", 400)
		return
	}
	fmt.Fprint(w, "Google account connected. You can close this tab and return to your private chat; reads are ready now.")
}

func (o *googleOAuth) verifyAndSave(ctx context.Context, alias string, token *oauth2.Token) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://gmail.googleapis.com/gmail/v1/users/me/profile", nil)
	if err != nil {
		return err
	}
	resp, err := o.config.Client(ctx, token).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var profile struct {
		Email string `json:"emailAddress"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 64<<10)).Decode(&profile) != nil || !strings.EqualFold(profile.Email, o.slots[alias].Email) {
		return errors.New("Google account email mismatch")
	}
	// Serialize persistence/reload against all reads, including upstream refresh writes.
	o.client.mu.Lock()
	defer o.client.mu.Unlock()
	// Fail closed if the operator edited slots during this flow.
	data, err := os.ReadFile(o.client.path)
	var current google.Config
	if err != nil || json.Unmarshal(data, &current) != nil || current.Accounts[alias] != o.slots[alias] {
		return errors.New("Google slot changed")
	}
	path := o.tokenPath(alias)
	for _, dir := range []string{filepath.Dir(filepath.Dir(path)), filepath.Dir(path)} {
		if err := os.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
			return errors.New("Google token directory must be private and not a symlink")
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".oauth-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	err = json.NewEncoder(tmp).Encode(token)
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	// Hard link publishes atomically and refuses ANY existing destination, including symlinks.
	if err := os.Link(tmp.Name(), path); err != nil {
		return err
	}
	manager, err := google.NewManager(o.client.path)
	if err == nil {
		connected := false
		for _, account := range manager.ListAccounts() {
			if account.Alias == alias && account.Authenticated {
				connected = true
			}
		}
		if !connected {
			err = errors.New("Google account activation failed")
		}
	}
	if err != nil {
		_ = os.Remove(path)
		return err
	}
	o.client.manager = manager
	return nil
}

// Only a bridge's already-projected aliases can reach link generation.
func (s *googleScope) connectAccount(raw json.RawMessage) (string, error) {
	var fields map[string]json.RawMessage
	var alias string
	if s.connect == nil || json.Unmarshal(raw, &fields) != nil || len(fields) != 1 || json.Unmarshal(fields["account"], &alias) != nil || !slices.Contains(s.aliases, alias) {
		return "", errors.New("Google connection not authorized for this chat")
	}
	link, err := s.connect.link(alias)
	if err != nil {
		return "", err
	}
	data, _ := json.Marshal(map[string]string{"url": link, "instructions": "Open this Google link on your phone within 10 minutes. Use the configured account; do not forward the link. Completion appears in your browser."})
	return string(data), nil
}
