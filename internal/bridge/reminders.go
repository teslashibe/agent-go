package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/teslashibe/agent-go/internal/reminders"
	"github.com/teslashibe/agent-go/internal/store"
)

// ActionSchema carries final presentation only; mutations use turn-bound tools.
const ActionSchema = `{"type":"object","properties":{"reply":{"type":"string"}},"required":["reply"],"additionalProperties":false}`

func decodeAction(text string) (store.Action, error) {
	var a store.Action
	if !utf8.ValidString(text) || len(text) > 64<<10 {
		return a, errors.New("invalid structured output")
	}
	// Token-level key validation also rejects duplicate keys and null values.
	decoder := json.NewDecoder(strings.NewReader(text))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return a, errors.New("expected action object")
	}
	values := map[string]string{}
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return a, err
		}
		key, ok := keyToken.(string)
		if !ok {
			return a, errors.New("invalid key")
		}
		if _, exists := values[key]; exists {
			return a, errors.New("duplicate action field")
		}
		valueToken, err := decoder.Token()
		if err != nil {
			return a, err
		}
		value, ok := valueToken.(string)
		if !ok {
			return a, errors.New("action fields must be strings")
		}
		values[key] = value
	}
	if len(values) != 1 {
		return a, errors.New("missing action fields")
	}
	allowed := map[string]bool{"reply": true}
	for key := range values {
		if !allowed[key] {
			return a, fmt.Errorf("unexpected action field %q", key)
		}
	}
	for _, key := range []string{"reply"} {
		if _, ok := values[key]; !ok {
			return a, errors.New("unexpected action field")
		}
	}
	// A strict ordinary decode verifies the closing brace and rejects trailing JSON.
	if !json.Valid([]byte(text)) {
		return a, errors.New("invalid action JSON")
	}
	if err := json.Unmarshal([]byte(text), &a); err != nil {
		return a, err
	}
	a.Action = "none"
	return a, nil
}

// ProcessReminder is an independent dispatcher lane; it never waits for a model
// turn. The shared Messenger implementation serializes actual transport sends.
func (b *Bridge) ProcessReminder(ctx context.Context) (bool, error) {
	now := time.Now()
	reminder, err := b.store.ClaimDue(ctx, b.config.Source, now)
	if err != nil || reminder == nil {
		return false, err
	}
	loc, err := reminders.LoadTimeZone(reminder.CreatedZone)
	if err != nil {
		return true, errors.Join(ErrUncertain, err, b.store.FinishReminder(context.WithoutCancel(ctx), b.config.Source, reminder.ID, false))
	}
	late := ""
	if now.Sub(reminder.DueUTC) > time.Minute {
		late = " (late; originally due at the time shown)"
	}
	text := fmt.Sprintf("%s: reminder #%d — due %s (%s)%s: %s", reminder.UserID, reminder.ID, reminder.DueUTC.In(loc).Format("2006-01-02 15:04:05 MST"), reminder.CreatedZone, late, reminder.Text)
	if reminder.CreatedBy != "" && reminder.CreatedBy != reminder.UserID {
		text += " (requested by " + reminder.CreatedBy + ")"
	}
	sendCtx, cancel := context.WithTimeout(ctx, time.Minute)
	err = b.messenger.Send(sendCtx, b.config.Source.ChatID, text)
	if err == nil {
		err = sendCtx.Err()
	}
	cancel()
	persistCtx, done := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer done()
	if err != nil {
		return true, errors.Join(ErrUncertain, err, b.store.FinishReminder(persistCtx, b.config.Source, reminder.ID, false))
	}
	if err = b.store.FinishReminder(persistCtx, b.config.Source, reminder.ID, true); err != nil {
		return true, errors.Join(ErrUncertain, err, b.store.FinishReminder(persistCtx, b.config.Source, reminder.ID, false))
	}
	return true, nil
}
