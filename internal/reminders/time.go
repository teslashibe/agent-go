package reminders

import (
	"errors"
	"strings"
	"time"
)

const DefaultTimeZone = "America/Los_Angeles"

// LoadTimeZone excludes abbreviations, offsets, local-machine defaults and paths.
func LoadTimeZone(zone string) (*time.Location, error) {
	if zone != "UTC" && (!strings.Contains(zone, "/") || strings.HasPrefix(zone, "/") || strings.Contains(zone, "..") || strings.HasPrefix(zone, "Etc/GMT")) {
		return nil, errors.New("use an IANA timezone such as America/Los_Angeles (or UTC), not an abbreviation or offset")
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return nil, errors.New("unknown IANA timezone")
	}
	return loc, nil
}

const WallTimeLayout = "2006-01-02T15:04:05"

// ParseReminderTime rejects normalized spring gaps and both sides of autumn folds.
func ParseReminderTime(wall, zone string, now time.Time) (time.Time, error) {
	loc, err := LoadTimeZone(zone)
	if err != nil {
		return time.Time{}, err
	}
	naive, err := time.Parse(WallTimeLayout, wall)
	if err != nil || naive.Format(WallTimeLayout) != wall {
		return time.Time{}, errors.New("specify an exact local date and time (YYYY-MM-DDTHH:mm:ss)")
	}
	offsets := map[int]bool{}
	for h := -48; h <= 48; h++ {
		_, offset := naive.Add(time.Duration(h) * time.Hour).In(loc).Zone()
		offsets[offset] = true
	}
	var candidates []time.Time
	for offset := range offsets {
		candidate := naive.Add(-time.Duration(offset) * time.Second)
		if candidate.In(loc).Format(WallTimeLayout) == wall {
			candidates = append(candidates, candidate)
		}
	}
	if len(candidates) == 0 {
		return time.Time{}, errors.New("that local time does not exist because of a clock change; choose another time")
	}
	if len(candidates) != 1 {
		return time.Time{}, errors.New("that local time is ambiguous because of a clock change; choose an unambiguous time or specify the exact UTC time")
	}
	due := candidates[0].UTC()
	if !due.After(now) {
		return time.Time{}, errors.New("that time is in the past; choose a future date and time")
	}
	if due.After(now.Add(366 * 24 * time.Hour)) {
		return time.Time{}, errors.New("reminders can be scheduled at most 366 days ahead")
	}
	return due, nil
}
