package store

import (
	"errors"
	"fmt"
	"regexp"

	"github.com/teslashibe/agent-go/internal/reminders"
)

// Profile and time constants preserve existing configuration and fixture callers.
// The payload and wall-time rules are owned by reminders.
type Profile = reminders.Profile

const DefaultTimeZone = reminders.DefaultTimeZone
const WallTimeLayout = reminders.WallTimeLayout

var profileID = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// ValidateProfiles stays with Source: sender authorization and validation order
// must not be replaced with a domain-level policy callback.
func ValidateProfiles(source Source, profiles []Profile) error {
	ids, senders := map[string]bool{}, map[string]bool{}
	if len(profiles) > 0 && !source.Group {
		return errors.New("profiles and reminders require the configured group chat")
	}
	for _, p := range profiles {
		if !profileID.MatchString(p.ID) || ids[p.ID] {
			return errors.New("profile IDs must be unique lowercase identifiers (maximum 32 characters)")
		}
		if !source.AllowsSender(p.Sender) || senders[p.Sender] {
			return errors.New("profile senders must be unique exact allowed senders")
		}
		zone := p.TimeZone
		if zone == "" {
			zone = DefaultTimeZone
		}
		if _, err := reminders.LoadTimeZone(zone); err != nil {
			return fmt.Errorf("profile %s: %w", p.ID, err)
		}
		ids[p.ID], senders[p.Sender] = true, true
	}
	return nil
}
