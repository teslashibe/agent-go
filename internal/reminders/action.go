package reminders

import (
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ValidateReminderID checks the canonical cancellation target, not ownership.
func ValidateReminderID(reminderID string) error {
	id, err := strconv.ParseInt(reminderID, 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != reminderID {
		return errors.New("invalid reminder ID")
	}
	return nil
}

func ReminderText(text string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" || !utf8.ValidString(text) || utf8.RuneCountInString(text) > 500 || strings.ContainsAny(text, "\x00\r") {
		return "", errors.New("provide reminder text of 1–500 characters.")
	}
	return text, nil
}

// Profile maps an authenticated transport sender to a stable application identity.
// Seed timezones are only used when the profile is first inserted.
type Profile struct {
	ID       string `json:"id"`
	Sender   string `json:"sender"`
	TimeZone string `json:"time_zone,omitempty"`
}
