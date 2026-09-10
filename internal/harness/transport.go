package harness

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// ToolCallTimeout leaves the ten-minute application turn time to report and
// persist an interrupted native batch. Transport deadlines must outlive it.
const ToolCallTimeout = 8 * time.Minute

// ServeEndpoint uses a private per-turn Unix socket. No source, job, sender
// or client credentials cross the process boundary.
func ServeEndpoint(handler http.Handler) (string, func(), error) {
	return serveEndpoint(handler, ToolCallTimeout+20*time.Second)
}

func serveEndpoint(handler http.Handler, timeout time.Duration) (string, func(), error) {
	dir, err := os.MkdirTemp("", "agent-harness-")
	if err != nil {
		return "", nil, err
	}
	path := filepath.Join(dir, "mcp.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		os.RemoveAll(dir)
		return "", nil, err
	}
	if err = os.Chmod(path, 0600); err != nil {
		listener.Close()
		os.RemoveAll(dir)
		return "", nil, err
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: time.Minute, WriteTimeout: timeout}
	go func() { _ = server.Serve(listener) }()
	return path, func() { _ = server.Close(); _ = os.RemoveAll(dir) }, nil
}

// RelayMCP adapts native MCP JSONL stdio to the parent-owned endpoint.
// It never opens durable state or accesses application clients directly.
func RelayMCP(ctx context.Context, path string, input io.Reader, output io.Writer) error {
	return relayMCP(ctx, path, input, output, ToolCallTimeout+15*time.Second)
}

func relayMCP(ctx context.Context, path string, input io.Reader, output io.Writer, timeout time.Duration) error {
	if !filepath.IsAbs(path) {
		return errors.New("MCP endpoint must be absolute")
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: timeout}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), 256<<10)
	for scanner.Scan() {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://localhost/mcp", bytes.NewReader(scanner.Bytes()))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		response, err := client.Do(req)
		if err != nil {
			return err
		}
		body, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
		response.Body.Close()
		if err != nil {
			return err
		}
		if response.StatusCode == http.StatusAccepted {
			continue
		}
		if response.StatusCode != http.StatusOK {
			return errors.New("MCP turn endpoint unavailable")
		}
		if _, err = output.Write(append(bytes.TrimSpace(body), '\n')); err != nil {
			return err
		}
	}
	return scanner.Err()
}
