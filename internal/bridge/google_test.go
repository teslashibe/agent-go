package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	google "github.com/teslashibe/google-go"
	googlemcp "github.com/teslashibe/google-go/mcp"
	"github.com/teslashibe/mcptool"
)

type fakeGoogle struct {
	calls []string
	fail  bool
}

func (f *fakeGoogle) accounts() []google.AccountInfo {
	return []google.AccountInfo{{Alias: "personal", Email: "personal@example.com", Authenticated: true}, {Alias: "work", Email: "secret@example.com", Authenticated: true}, {Alias: "family", Authenticated: true}}
}
func (f *fakeGoogle) invoke(_ context.Context, tool mcptool.Tool, raw json.RawMessage) (any, error) {
	var args map[string]any
	_ = json.Unmarshal(raw, &args)
	f.calls = append(f.calls, tool.Name+":"+args["account"].(string))
	if f.fail {
		return nil, errors.New("secret@example.com token /private/credentials")
	}
	return map[string]any{"items": []string{args["account"].(string)}}, nil
}

func TestGoogleScopeRejectsBypassBeforeDispatch(t *testing.T) {
	f := &fakeGoogle{}
	s, err := newGoogleScope(f, []string{"personal"})
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{}`, `{"account":"personal"}`, `null`, `{"account":""}`, `{"account":"work"}`, `{"account":"personal@example.com"}`, `{"account":"PERSONAL"}`, `{"account":" personal"}`, `{"account":"personal","Account":"work"}`, `{"account":"personal","source":"family"}`, `{"account":"personal","chat_id":1}`} {
		if _, err := s.call(context.Background(), "google_gmail_search", json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	for _, tool := range (googlemcp.Provider{}).Tools() {
		if googleReadNames[tool.Name] || googleUnifiedNames[tool.Name] != "" || tool.Name == "google_gmail_list_accounts" {
			continue
		}
		if _, err := s.call(context.Background(), tool.Name, json.RawMessage(`{"account":"personal"}`)); err == nil {
			t.Fatalf("enabled %s", tool.Name)
		}
	}
	if len(f.calls) != 0 {
		t.Fatal(f.calls)
	}
}

func TestGoogleScopeListsAndQueriesOnlyApprovedAccounts(t *testing.T) {
	f := &fakeGoogle{}
	aliases := []string{"personal", "family"}
	s, err := newGoogleScope(f, aliases)
	if err != nil {
		t.Fatal(err)
	}
	aliases[0] = "work"
	got, err := s.call(context.Background(), "google_gmail_list_accounts", json.RawMessage(`{}`))
	if err != nil || strings.Contains(got, "secret") || strings.Contains(got, "work") || !strings.Contains(got, "personal") {
		t.Fatalf("%s %v", got, err)
	}
	for name := range googleUnifiedNames {
		raw := `{}`
		if name == "google_gmail_unified_search" {
			raw = `{"query":"in:inbox"}`
		}
		if _, err := s.call(context.Background(), name, json.RawMessage(raw)); err != nil {
			t.Fatal(name, err)
		}
	}
	if len(f.calls) != 8 {
		t.Fatal(f.calls)
	}
	for _, call := range f.calls {
		if strings.Contains(call, "work") || strings.Contains(call, "unified") {
			t.Fatal(call)
		}
	}
	got, err = s.call(context.Background(), "google_gmail_search", json.RawMessage(`{"account":"personal","query":"x"}`))
	if err != nil || !strings.Contains(got, "personal") {
		t.Fatalf("%s %v", got, err)
	}
	f.fail = true
	_, err = s.call(context.Background(), "google_drive_read", json.RawMessage(`{"account":"personal","file_id":"x"}`))
	if err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "private") {
		t.Fatal(err)
	}
}

func TestGoogleNoGrantNoToolsAndIndependentInventories(t *testing.T) {
	f := &fakeGoogle{}
	empty, _ := newGoogleScope(f, nil)
	if len(empty.tools()) != 0 {
		t.Fatal(empty.tools())
	}
	if _, err := empty.call(context.Background(), "google_gmail_list_accounts", json.RawMessage(`{}`)); err == nil {
		t.Fatal("empty grants exposed accounts")
	}
	personal, _ := newGoogleScope(f, []string{"personal"})
	work, _ := newGoogleScope(f, []string{"work"})
	for _, s := range []*googleScope{personal, work, personal} {
		data, _ := json.Marshal(s.tools())
		other := "secret@example.com"
		if s.aliases[0] == "work" {
			other = "personal"
		}
		if strings.Contains(string(data), other) {
			t.Fatal(string(data))
		}
	}
	if _, err := newGoogleScope(f, []string{"unknown"}); err == nil {
		t.Fatal("unknown grant accepted")
	}
}

func TestGoogleTurnClosureAndCancellation(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		f := &fakeGoogle{}
		scope, _ := newGoogleScope(f, []string{"personal"})
		turn := &notesTurn{ctx: ctx, bridge: &Bridge{google: scope}}
		if cancelled {
			cancel()
		} else {
			_ = turn.close()
		}
		request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"google_gmail_search","arguments":{"account":"personal","query":"x"}}}`))
		response := httptest.NewRecorder()
		turn.ServeHTTP(response, request)
		cancel()
		if response.Code != http.StatusGone || len(f.calls) != 0 {
			t.Fatalf("%d %v", response.Code, f.calls)
		}
	}
}

func TestGoogleConfigMustAlreadyExist(t *testing.T) {
	path := t.TempDir() + "/missing.json"
	if _, err := NewGoogleClient(path); err == nil {
		t.Fatal("missing config accepted")
	}
}

func TestGoogleHTTPWithNotesDisabled(t *testing.T) {
	f := &fakeGoogle{}
	scope, _ := newGoogleScope(f, []string{"personal"})
	turn := &notesTurn{ctx: context.Background(), bridge: &Bridge{google: scope}}
	if HandlerHasNotes(turn) || !HandlerHasGoogle(turn) {
		t.Fatal("incorrect tool capabilities")
	}
	for _, body := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"google_gmail_search","arguments":{"account":"personal","query":"x"}}}`,
	} {
		response := httptest.NewRecorder()
		turn.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body)))
		if response.Code != http.StatusOK || !json.Valid(response.Body.Bytes()) || strings.Contains(response.Body.String(), `"isError":true`) {
			t.Fatal(response.Body.String())
		}
		if strings.Contains(body, "tools/list") && (!strings.Contains(response.Body.String(), "google_gmail_search") || strings.Contains(response.Body.String(), "google_gmail_send") || strings.Contains(response.Body.String(), "work")) {
			t.Fatal(response.Body.String())
		}
	}
	if len(f.calls) != 1 || f.calls[0] != "google_gmail_search:personal" {
		t.Fatal(f.calls)
	}
}
