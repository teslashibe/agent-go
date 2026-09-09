package main

import (
	"errors"
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"
)

var savedSessionID = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// requireInteractiveSession never replaces a saved session with a new one.
// A missing or unreadable local session requires explicit recovery.
func requireInteractiveSession(home, session string) error {
	if session == "" || home == "" {
		return nil
	}
	if !savedSessionID.MatchString(session) {
		return errors.New("invalid saved Codex session; original session preserved")
	}
	found := false
	err := filepath.WalkDir(filepath.Join(home, "sessions"), func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() && strings.HasSuffix(strings.ToLower(d.Name()), "-"+strings.ToLower(session)+".jsonl") {
			found = true
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		return errors.New("cannot inspect saved Codex sessions; original session preserved; review the configured home before recovery")
	}
	if !found {
		return errors.New("saved Codex session is missing from the configured home; original session preserved; explicit recovery required")
	}
	return nil
}
