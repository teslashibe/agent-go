package bridge

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teslashibe/agent-go/internal/store"
	"golang.org/x/oauth2"
)

type oauthTransport func(*http.Request) (*http.Response, error)

func (f oauthTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func oauthFixture(t *testing.T) (*GoogleClient, *googleOAuth) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{
		"config.json":     `{"accounts":{"personal":{"email":"person@example.com"},"work":{"email":"work@example.com"}}}`,
		"oauth-keys.json": `{"web":{"client_id":"test","client_secret":"secret","redirect_uris":["https://phone.example/oauth2callback"]}}`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	c, err := NewGoogleClient(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	o, err := c.newOAuth("https://phone.example")
	if err != nil {
		t.Fatal(err)
	}
	c.oauth = o
	return c, o
}

func oauthState(t *testing.T, o *googleOAuth) string {
	t.Helper()
	link, err := o.link("personal")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(link)
	if u.Host != "accounts.google.com" || u.Query().Get("code_challenge_method") != "S256" || u.Query().Get("redirect_uri") != "https://phone.example/oauth2callback" || strings.Contains(u.Query().Get("scope"), "modify") || u.Query().Get("include_granted_scopes") != "false" {
		t.Fatal(link)
	}
	return u.Query().Get("state")
}
func callback(o *googleOAuth, query string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	o.ServeHTTP(w, httptest.NewRequest("GET", "/oauth2callback?"+query, nil))
	return w
}

func TestGoogleOAuthExpiryReplayAndDenial(t *testing.T) {
	_, o := oauthFixture(t)
	now := time.Now()
	o.now = func() time.Time { return now }
	state := oauthState(t, o)
	now = now.Add(11 * time.Minute)
	if w := callback(o, "state="+state+"&code=secret"); w.Code != 400 || !strings.Contains(w.Body.String(), "expired") {
		t.Fatal(w)
	}
	state = oauthState(t, o)
	if w := callback(o, "state="+state+"&error=secret_denial"); w.Code != 400 || strings.Contains(w.Body.String(), "secret_denial") {
		t.Fatal(w)
	}
	if w := callback(o, "state="+state+"&code=secret"); w.Code != 400 || !strings.Contains(w.Body.String(), "already used") {
		t.Fatal(w)
	}
	if w := callback(o, "state=unknown&code=secret"); w.Code != 400 {
		t.Fatal(w)
	}
}

func TestGoogleOAuthExactScopeAndPrivateDM(t *testing.T) {
	c, o := oauthFixture(t)
	scope, _ := newGoogleScope(c, []string{"personal"})
	for _, raw := range []string{`{"account":"personal"}`, `{"account":"work"}`} {
		if _, err := scope.connectAccount(json.RawMessage(raw)); err == nil {
			t.Fatal("opt-in missing")
		}
	}
	scope.connect = o
	for _, raw := range []string{`{"account":"work"}`, `{"account":"personal","chat_id":2}`, `{"account":"person@example.com"}`, `null`} {
		if _, err := scope.connectAccount(json.RawMessage(raw)); err == nil {
			t.Fatal(raw)
		}
	}
	if _, err := scope.call(context.Background(), "google_connect_account", json.RawMessage(`{"account":"personal"}`)); err != nil {
		t.Fatal(err)
	}
	b := &Bridge{google: scope, config: Config{Source: store.Source{Group: true, ChatID: 1, ChatGUID: "group", AllowedSenders: []string{"person"}}}}
	if b.EnableGoogleConnect() == nil {
		t.Fatal("group accepted")
	}
	b.config.Source.Group = false
	if err := b.EnableGoogleConnect(); err != nil {
		t.Fatal(err)
	}
}

func TestGoogleOAuthProtocolPersistenceReload(t *testing.T) {
	for _, mode := range []string{"success", "mismatch", "exchange_error", "no_refresh", "broad_scopes"} {
		t.Run(mode, func(t *testing.T) {
			c, o := oauthFixture(t)
			state := oauthState(t, o)
			pending := o.pending[state]
			calls := 0
			o.httpClient.Transport = oauthTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				body := `{"emailAddress":"person@example.com"}`
				status := 200
				if r.URL.Host == "oauth2.googleapis.com" {
					if err := r.ParseForm(); err != nil {
						t.Fatal(err)
					}
					if r.Form.Get("code_verifier") != pending.verifier || r.Form.Get("code") != "authorization-code" {
						t.Fatal("missing PKCE exchange")
					}
					body = `{"access_token":"access-secret","refresh_token":"refresh-secret","token_type":"Bearer","expires_in":3600}`
					if mode == "broad_scopes" {
						body = `{"access_token":"access-secret","refresh_token":"refresh-secret","token_type":"Bearer","scope":"https://mail.google.com/"}`
					}
					if mode == "exchange_error" {
						status = 400
						body = `{"error":"secret-provider-error"}`
					}
					if mode == "no_refresh" {
						body = `{"access_token":"access-secret","token_type":"Bearer"}`
					}
				} else {
					if r.URL.Host != "gmail.googleapis.com" || r.Header.Get("Authorization") != "Bearer access-secret" {
						t.Fatal("invalid identity request")
					}
					if mode == "mismatch" {
						body = `{"emailAddress":"other@example.com"}`
					}
				}
				return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			})
			w := callback(o, "state="+state+"&code=authorization-code")
			if strings.Contains(w.Body.String(), "secret") || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal(w)
			}
			if mode != "success" {
				if w.Code != 400 {
					t.Fatal(w)
				}
				if _, err := os.Stat(o.tokenPath("personal")); !os.IsNotExist(err) {
					t.Fatal("failed flow persisted token")
				}
				return
			}
			if w.Code != 200 {
				t.Fatal(w)
			}
			info, err := os.Stat(o.tokenPath("personal"))
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatal(info, err)
			}
			data, _ := os.ReadFile(o.tokenPath("personal"))
			var token oauth2.Token
			if json.Unmarshal(data, &token) != nil || token.RefreshToken != "refresh-secret" {
				t.Fatal("token format invalid")
			}
			if !c.accounts()[0].Authenticated {
				t.Fatal("account not hot reloaded")
			}
			var wg sync.WaitGroup
			for range 10 {
				wg.Add(1)
				go func() { defer wg.Done(); _ = c.accounts() }()
			}
			wg.Wait()
			if _, err := o.link("personal"); err == nil {
				t.Fatal("overwrite permitted")
			}
			count := calls
			if w := callback(o, "state="+state+"&code=authorization-code"); w.Code != 400 || calls != count {
				t.Fatal("replay exchanged code")
			}
			// Same-account parallel links cannot replace the first successful token.
			if err := o.verifyAndSave(context.WithValue(context.Background(), oauth2.HTTPClient, o.httpClient), "personal", &token); err == nil {
				t.Fatal("overwrite succeeded")
			}
		})
	}
}

func TestGoogleOAuthRejectsUnsafeConfiguration(t *testing.T) {
	c, _ := oauthFixture(t)
	for _, base := range []string{"http://phone.example", "https://user@phone.example", "https://phone.example/path", "https://phone.example?secret=x"} {
		if _, err := c.newOAuth(base); err == nil {
			t.Fatal(base)
		}
	}
	if err := os.Chmod(c.path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := c.newOAuth("https://phone.example"); err == nil {
		t.Fatal("public config accepted")
	}
}
