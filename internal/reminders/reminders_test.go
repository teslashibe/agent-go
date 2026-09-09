package reminders

import (
	"strings"
	"testing"
	"time"
)

func TestReminderWallTimes(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		wall, zone string
		valid      bool
	}{
		{"2026-03-08T02:30:00", "America/Los_Angeles", false},
		{"2026-11-01T01:30:00", "America/Los_Angeles", false},
		{"2026-03-08T03:30:00", "America/Los_Angeles", true},
		{"2026-11-01T02:30:00", "America/Los_Angeles", true},
		{"2026-11-01T08:30:00", "UTC", true},
		{"2026-11-01T01:30:00", "PST", false},
		{"2026-11-01T01:30:00", "-08:00", false},
		{"2026-11-01T01:30:00", "Etc/GMT+8", false},
		{"2026-02-30T10:00:00", "UTC", false},
		{"2026-02-01T10:00", "UTC", false},
		{"2026-02-01T10:00:00.1", "UTC", false},
		{"2025-12-31T10:00:00", "UTC", false},
		{"2028-01-01T10:00:00", "UTC", false},
		{"2026-04-05T01:45:00", "Australia/Lord_Howe", false},
		{"2026-10-04T02:15:00", "Australia/Lord_Howe", false},
	} {
		if _, err := ParseReminderTime(tc.wall, tc.zone, now); (err == nil) != tc.valid {
			t.Errorf("%+v: %v", tc, err)
		}
	}
}

func TestValidateReminderID(t *testing.T) {
	for _, id := range []string{"", "0", "01", "+1", "-1", "1.0", "9223372036854775808"} {
		if err := ValidateReminderID(id); err == nil {
			t.Errorf("accepted %q", id)
		}
	}
	if err := ValidateReminderID("1"); err != nil {
		t.Fatal(err)
	}
}

func TestReminderText(t *testing.T) {
	for _, text := range []string{"", "  ", "bad\x00text", "bad\rtext", "\xff", strings.Repeat("x", 501)} {
		if _, err := ReminderText(text); err == nil {
			t.Errorf("accepted %q", text)
		}
	}
	for _, text := range []string{" reminder ", "line\nbreak", strings.Repeat("日", 500)} {
		got, err := ReminderText(text)
		if err != nil || got != strings.TrimSpace(text) {
			t.Errorf("%q: %q, %v", text, got, err)
		}
	}
}
