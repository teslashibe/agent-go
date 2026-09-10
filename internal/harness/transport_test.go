package harness

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRelayReturnsDelayedToolResultsAndStillBoundsFailures(t *testing.T) {
	path, closeEndpoint, err := serveEndpoint(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(75 * time.Millisecond):
			_, _ = w.Write([]byte(`{"id":1,"result":{"saved":22}}`))
		case <-r.Context().Done():
		}
	}), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer closeEndpoint()
	var output bytes.Buffer
	if err := relayMCP(context.Background(), path, strings.NewReader("{}\n"), &output, 25*time.Millisecond); err == nil {
		t.Fatal("short deadline did not reject delayed work")
	}
	output.Reset()
	if err := relayMCP(context.Background(), path, strings.NewReader("{}\n"), &output, time.Second); err != nil || !strings.Contains(output.String(), `"saved":22`) {
		t.Fatal(output.String(), err)
	}
}

// Run separately on the Mini to exercise the real former two-minute boundary.
// There are no Notes, model, or message effects in this transport fixture.
func TestLongHarnessCall(t *testing.T) {
	if os.Getenv("AGENT_LONG_TOOL_TEST") != "1" {
		t.Skip("opt-in slow transport regression")
	}
	path, closeEndpoint, err := ServeEndpoint(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(125 * time.Second):
			_, _ = w.Write([]byte(`{"id":1,"result":{"saved":30}}`))
		case <-r.Context().Done():
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer closeEndpoint()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	var output bytes.Buffer
	if err := RelayMCP(ctx, path, strings.NewReader("{}\n"), &output); err != nil || !strings.Contains(output.String(), `"saved":30`) {
		t.Fatal(output.String(), err)
	}
}

func TestEndpointRoundtripAndCleanup(t *testing.T) {
	var requests []string
	path, closeEndpoint, err := ServeEndpoint(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/mcp" || r.Header.Get("Content-Type") != "application/json" {
			t.Error("relay request contract changed")
		}
		body, _ := io.ReadAll(r.Body)
		requests = append(requests, string(body))
		if len(requests) == 2 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		_, _ = w.Write([]byte("  " + string(body) + "\n"))
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer closeEndpoint()
	// Moved from the opt-in model smoke fixture: transport privacy needs no model.
	for name, mode := range map[string]os.FileMode{filepath.Dir(path): 0700, path: 0600} {
		info, err := os.Stat(name)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatalf("private endpoint: %v %v", info, err)
		}
	}
	input := "{\"id\":1}\n{\"method\":\"notifications/initialized\"}\n{\"id\":2}\n"
	var output bytes.Buffer
	if err := RelayMCP(context.Background(), path, strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	if output.String() != "{\"id\":1}\n{\"id\":2}\n" || strings.Join(requests, "\n")+"\n" != input {
		t.Fatalf("roundtrip: %q, requests: %q", output.String(), requests)
	}
	closeEndpoint()
	if _, err := os.Stat(filepath.Dir(path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("endpoint directory retained: %v", err)
	}
	if err := RelayMCP(context.Background(), path, strings.NewReader("{}\n"), io.Discard); err == nil {
		t.Fatal("closed endpoint remains usable")
	}
}

func TestRelayFailures(t *testing.T) {
	path, closeEndpoint, err := ServeEndpoint(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusGone)
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer closeEndpoint()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tc := range []struct {
		name  string
		ctx   context.Context
		path  string
		input string
	}{
		{"relative path", context.Background(), "relative.sock", "{}\n"},
		{"unavailable", context.Background(), path, "{}\n"},
		{"cancelled", ctx, path, "{}\n"},
		{"request limit", context.Background(), path, strings.Repeat("x", 256<<10) + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := RelayMCP(tc.ctx, tc.path, strings.NewReader(tc.input), io.Discard); err == nil {
				t.Fatal("expected relay failure")
			}
		})
	}
}

func TestEndpointStartupFailure(t *testing.T) {
	dir := t.TempDir()
	// Exceed the Unix socket path limit after MkdirTemp succeeds.
	long := filepath.Join(dir, strings.Repeat("x", 100))
	if err := os.Mkdir(long, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", long)
	if _, closeEndpoint, err := ServeEndpoint(http.NotFoundHandler()); err == nil {
		closeEndpoint()
		t.Fatal("expected socket startup failure")
	}
	entries, err := os.ReadDir(long)
	if err != nil || len(entries) != 0 {
		t.Fatalf("startup leaked private directory: %v %v", entries, err)
	}
}
