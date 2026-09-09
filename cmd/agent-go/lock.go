//go:build darwin || linux

package main

import (
	"fmt"
	"os"
	"syscall"
)

// lockState prevents a second process from dispatching the same persisted work.
// The lock lives beside the database, outside the agent's working directory.
func lockState(path string) (*os.File, error) {
	file, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open process lock: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		return nil, fmt.Errorf("state is locked by another process: %w", err)
	}
	return file, nil
}
