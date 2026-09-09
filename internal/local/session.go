// Package local starts imsg directly in the agent's user session.
package local

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// Session owns the local process and its input/output pipes.
type Session struct {
	io.ReadCloser
	input  io.WriteCloser
	cmd    *exec.Cmd
	cancel context.CancelFunc
	once   sync.Once
	err    error
}

// Open runs binary rpc without a shell, inheriting the agent's user and
// environment. The agent must already run as the logged-in Messages user.
func Open(ctx context.Context, binary string) (*Session, error) {
	if !filepath.IsAbs(binary) {
		return nil, errors.New("local imsg path must be absolute")
	}
	info, err := os.Stat(binary)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return nil, errors.New("local imsg path must name an existing executable file")
	}
	ctx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(ctx, binary, "rpc")
	cmd.WaitDelay = 2 * time.Second
	input, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("open local imsg stdin: %w", err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		input.Close()
		return nil, fmt.Errorf("open local imsg stdout: %w", err)
	}
	// Diagnostics may contain message data or credentials; never log them.
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		cancel()
		input.Close()
		output.Close()
		return nil, fmt.Errorf("start local imsg: %w", err)
	}
	s := &Session{ReadCloser: output, input: input, cmd: cmd, cancel: cancel}
	// Also close pipes on cancellation, even if a descendant inherited them.
	context.AfterFunc(ctx, func() { s.Close() })
	return s, nil
}

func (s *Session) Write(p []byte) (int, error) { return s.input.Write(p) }

// Close interrupts blocked I/O, kills and reaps the owned process. It is safe
// to call concurrently or more than once. A killed send remains uncertain.
func (s *Session) Close() error {
	s.once.Do(func() {
		s.cancel()
		s.input.Close()
		s.ReadCloser.Close()
		s.err = s.cmd.Wait()
	})
	return s.err
}
