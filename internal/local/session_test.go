package local

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Run the test executable as imsg; no shell, model, or Messages database needed.
func init() {
	if os.Getenv("AGENT_TEST_LOCAL") != "1" || len(os.Args) != 2 || os.Args[1] != "rpc" {
		return
	}
	if _, err := io.CopyN(os.Stdout, os.Stdin, 4); err != nil {
		os.Exit(2)
	}
	// Stop consuming input so the parent can exercise blocked reads and writes.
	for {
		time.Sleep(time.Hour)
	}
}

func TestSessionShutdown(t *testing.T) {
	for _, action := range []string{"cancel", "close"} {
		t.Run(action, func(t *testing.T) {
			t.Setenv("AGENT_TEST_LOCAL", "1")
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			// Local paths may include spaces and shell metacharacters.
			binary := filepath.Join(t.TempDir(), "imsg ; literal")
			if err := os.Symlink(executable, binary); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			session, err := Open(ctx, binary)
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			if _, err := session.Write([]byte("ping")); err != nil {
				t.Fatal(err)
			}
			var reply [4]byte
			if _, err := io.ReadFull(session, reply[:]); err != nil || string(reply[:]) != "ping" {
				t.Fatalf("round trip: %q, %v", reply, err)
			}
			reads := make(chan error, 1)
			writes := make(chan error, 1)
			go func() { _, err := session.Read(make([]byte, 1)); reads <- err }()
			go func() { _, err := session.Write(make([]byte, 8<<20)); writes <- err }()
			closed := make(chan struct{})
			if action == "cancel" {
				cancel()
			} else {
				go func() { session.Close(); close(closed) }()
			}
			for _, done := range []chan error{reads, writes} {
				select {
				case err := <-done:
					if err == nil {
						t.Fatal("I/O succeeded after shutdown")
					}
				case <-time.After(5 * time.Second):
					t.Fatal("shutdown left I/O blocked")
				}
			}
			if action == "close" {
				select {
				case <-closed:
				case <-time.After(5 * time.Second):
					t.Fatal("Close hung")
				}
			}
			var wg sync.WaitGroup
			for range 4 {
				wg.Go(func() { session.Close() })
			}
			wg.Wait()
			if session.cmd.ProcessState == nil {
				t.Fatal("process was not reaped")
			}
		})
	}
}

func TestOpenFailure(t *testing.T) {
	for _, binary := range []string{"", "imsg", filepath.Join(t.TempDir(), "missing")} {
		if _, err := Open(context.Background(), binary); err == nil {
			t.Fatalf("accepted %q", binary)
		}
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Open(ctx, binary); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled start: %v", err)
	}
}
