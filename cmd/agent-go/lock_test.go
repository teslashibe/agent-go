//go:build darwin || linux

package main

import (
	"path/filepath"
	"testing"
)

func TestLockState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	first, err := lockState(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if second, err := lockState(path); err == nil {
		second.Close()
		t.Fatal("second lock succeeded")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := lockState(path)
	if err != nil {
		t.Fatal(err)
	}
	next.Close()
}
