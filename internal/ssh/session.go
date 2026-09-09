// Package ssh starts a remote imsg process using the system OpenSSH client.
package ssh

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"sync"
	"time"
)

var hostPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.@-]*$`)
var pathPattern = regexp.MustCompile(`^/[a-zA-Z0-9_./-]+$`)

// Session owns the remote process and its input/output pipes.
type Session struct {
	io.ReadCloser
	input  io.WriteCloser
	cmd    *exec.Cmd
	cancel context.CancelFunc
	once   sync.Once
	err    error
}

// Open starts imsg rpc on a configured SSH host. SSH configuration supplies the
// identity and network route; host keys must already be trusted.
func Open(ctx context.Context, host, binary string) (*Session, error) {
	if !hostPattern.MatchString(host) {
		return nil, errors.New("invalid SSH host alias")
	}
	if !pathPattern.MatchString(binary) {
		return nil, errors.New("imsg path must be an absolute path without shell metacharacters")
	}
	ctx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(ctx, "ssh", "-T", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "ConnectTimeout=10", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=2", host, binary, "rpc")
	cmd.WaitDelay = 2 * time.Second
	input, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("open SSH stdin: %w", err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		input.Close()
		return nil, fmt.Errorf("open SSH stdout: %w", err)
	}
	// Remote diagnostics may contain message data. Keep them out of application logs.
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		cancel()
		input.Close()
		output.Close()
		return nil, fmt.Errorf("start SSH: %w", err)
	}
	return &Session{ReadCloser: output, input: input, cmd: cmd, cancel: cancel}, nil
}

func (s *Session) Write(p []byte) (int, error) { return s.input.Write(p) }

// Close interrupts blocked I/O and reaps the local SSH process. Killing SSH
// does not establish whether a remote send completed; callers must reconcile it.
func (s *Session) Close() error {
	s.once.Do(func() {
		s.cancel()
		s.input.Close()
		s.ReadCloser.Close()
		s.err = s.cmd.Wait()
	})
	return s.err
}
