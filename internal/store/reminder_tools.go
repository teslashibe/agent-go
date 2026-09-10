package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/teslashibe/agent-go/internal/reminders"
)

// ReminderToolResult is the durable structured outcome, persisted with the mutation.
type ReminderToolResult struct {
	Status             string       `json:"status"`
	Message            string       `json:"message"`
	IsError            bool         `json:"is_error"`
	Changed            bool         `json:"changed"`
	Timezone           string       `json:"timezone"`
	TimezoneConfigured bool         `json:"timezone_configured"`
	Reminder           *Reminder    `json:"reminder,omitempty"`
	Reminders          []Reminder   `json:"reminders"`
	Pending            *PendingInfo `json:"pending"`
}

type PendingInfo struct {
	Text        string    `json:"text"`
	RecipientID string    `json:"recipient_id,omitempty"`
	Question    string    `json:"question"`
	CreatedAt   time.Time `json:"created_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}

func reminderPendingInfo(ctx context.Context, tx *sql.Tx, source Source, sender string) (*PendingInfo, error) {
	p := &PendingInfo{}
	var created int64
	err := tx.QueryRowContext(ctx, `SELECT prompt,question,created_at,recipient_id FROM reminder_clarifications WHERE source=? AND sender=?`, source.key(), sender).Scan(&p.Text, &p.Question, &created, &p.RecipientID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p.CreatedAt = time.Unix(0, created).UTC()
	p.ExpiresAt = p.CreatedAt.Add(24 * time.Hour)
	return p, nil
}

// ReminderTool executes the local mutation and saves its receipt in ONE
// transaction. Unlike external effects, rollback is sufficient for safe retry.
func (s *Store) ReminderTool(ctx context.Context, source Source, jobID int64, name string, args reminders.Args, now time.Time) (string, error) {
	if err := reminders.Validate(name, args); err != nil {
		return "", err
	}
	canonical, _ := json.Marshal(struct {
		Name string
		Args reminders.Args
	}{name, args})
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var sender, prompt, id, zone string
	if !source.Group || source.Validate() != nil {
		return "", errors.New("reminder tools are disabled for this channel")
	}
	err = tx.QueryRowContext(ctx, `SELECT j.sender,j.prompt,p.user_id,p.time_zone FROM jobs j JOIN sources s ON s.source=j.source JOIN profiles p ON p.source=j.source AND p.sender=j.sender AND p.active=1 WHERE j.id=? AND j.source=? AND j.state='running' AND s.paused=0 AND s.source_invalid=0`, jobID, source.key()).Scan(&sender, &prompt, &id, &zone)
	if err != nil {
		return "", err
	}
	if !source.AllowsSender(sender) {
		return "", errors.New("requester is not authorized")
	}
	var stored, state, result string
	err = tx.QueryRowContext(ctx, `SELECT arguments,state,result FROM tool_operations WHERE job_id=? AND operation_id=?`, jobID, args.OperationID).Scan(&stored, &state, &result)
	if err == nil {
		if stored != string(canonical) {
			return "", errors.New("operation ID reused with different arguments")
		}
		if state != "completed" {
			return "", ErrUncertain
		}
		return result, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	err = nil
	var unresolved bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tool_operations o JOIN jobs j ON j.id=o.job_id WHERE j.source=? AND o.state IN ('dispatching','unknown') AND o.resolution='')`, source.key()).Scan(&unresolved); err != nil {
		return "", err
	}
	if unresolved {
		return "", ErrUncertain
	}
	recipient := args.RecipientID
	if name == "create_reminder" || name == "set_pending_reminder" {
		pending, err := reminderPendingInfo(ctx, tx, source, sender)
		if err != nil {
			return "", err
		}
		if pending != nil && now.Before(pending.ExpiresAt) && pending.RecipientID != "" {
			if recipient != "" && recipient != pending.RecipientID {
				return "", errors.New("clear the pending reminder before changing its recipient")
			}
			recipient = pending.RecipientID
		}
		if recipient != "" {
			if err := validateReminderRecipient(ctx, tx, source, recipient); err != nil {
				return "", err
			}
		}
	}
	outcome := ReminderToolResult{Status: "completed", Timezone: zone, TimezoneConfigured: zone != ""}
	switch name {
	case "get_timezone":
		outcome.Message = zone
	case "set_pending_reminder":
		previous, readErr := reminderPendingInfo(ctx, tx, source, sender)
		if readErr != nil {
			return "", readErr
		}
		// Keep the original request and expiry on clarification follow-ups.
		_, err = tx.ExecContext(ctx, `INSERT INTO reminder_clarifications(source,sender,prompt,created_at,question,recipient_id) VALUES(?,?,?,?,?,?) ON CONFLICT(source,sender) DO UPDATE SET prompt=CASE WHEN created_at<=? THEN excluded.prompt ELSE prompt END, created_at=CASE WHEN created_at<=? THEN excluded.created_at ELSE created_at END, question=excluded.question,recipient_id=excluded.recipient_id`, source.key(), sender, boundedText(prompt, 4000), now.UnixNano(), args.Question, recipient, now.Add(-24*time.Hour).UnixNano(), now.Add(-24*time.Hour).UnixNano())
		if err == nil {
			outcome.Pending, err = reminderPendingInfo(ctx, tx, source, sender)
		}
		outcome.Changed = previous == nil || (outcome.Pending != nil && *previous != *outcome.Pending)
		outcome.Message = "Pending reminder request saved; clarification: " + args.Question
	case "clear_pending_reminder":
		var result sql.Result
		result, err = tx.ExecContext(ctx, `DELETE FROM reminder_clarifications WHERE source=? AND sender=?`, source.key(), sender)
		if err == nil {
			var n int64
			n, err = result.RowsAffected()
			outcome.Changed = n > 0
		}
		outcome.Message = "Pending reminder clarification cleared. Scheduled reminders are unchanged."
		if !outcome.Changed {
			outcome.Message = "No pending reminder clarification existed. Scheduled reminders are unchanged."
		}
	default:
		outcome, err = executeAction(ctx, tx, source, id, zone, Action{Action: name, Text: args.Text, LocalTime: args.LocalTime, Timezone: args.Timezone, ReminderID: args.ReminderID, RecipientID: recipient}, now)
		if err == nil && name == "create_reminder" && !outcome.IsError {
			_, err = tx.ExecContext(ctx, `DELETE FROM reminder_clarifications WHERE source=? AND sender=?`, source.key(), sender)
		}
	}
	if err != nil {
		return "", err
	}
	if outcome.IsError {
		outcome.Status = "not_applied"
	}
	encoded, err := json.Marshal(outcome)
	if err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO tool_operations(job_id,operation_id,arguments,state,result) VALUES(?,?,?,'completed',?)`, jobID, args.OperationID, string(canonical), string(encoded)); err != nil {
		return "", err
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	return string(encoded), nil
}
