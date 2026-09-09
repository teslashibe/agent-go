package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

func codingHome(statePath, agentName string) string {
	return filepath.Join(filepath.Dir(statePath), "codex-"+agentName)
}

// isolateCodingHome gives exec-only coding its own CODEX_HOME so CLI
// project-trust writes cannot mutate the family's hashed config.toml.
// Interactive coding keeps the pinned binary and home. Auth is linked from
// the process home; config.toml is not.
func isolateCodingHome(statePath, agentName, familyHome, binary string) (string, error) {
	if agentName == "" || binary == "" {
		return "", fmt.Errorf("coding home requires agent name and codex binary")
	}
	home := codingHome(statePath, agentName)
	if err := os.MkdirAll(home, 0700); err != nil {
		return "", err
	}
	if familyHome != "" && familyHome != home {
		src := filepath.Join(familyHome, "auth.json")
		dst := filepath.Join(home, "auth.json")
		if _, err := os.Lstat(dst); err != nil && os.IsNotExist(err) {
			if _, err := os.Stat(src); err == nil {
				if err := os.Symlink(src, dst); err != nil {
					return "", err
				}
			}
		}
	}
	wrapper := filepath.Join(home, "codex-wrapper")
	body := "#!/bin/sh\nexport CODEX_HOME=" + strconv.Quote(home) + "\nexec " + strconv.Quote(binary) + " \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(body), 0700); err != nil {
		return "", err
	}
	return wrapper, nil
}
