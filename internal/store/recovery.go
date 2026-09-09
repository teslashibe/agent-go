package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ResolveInterruptedAttempt preserves an unfulfilled request without replaying it.
// The caller must hold the daemon lock and review external effects first.
func (s *Store) ResolveInterruptedAttempt(ctx context.Context, source Source, jobID int64, reason string) error {
	return s.recoverAttempt(ctx, source, jobID, reason, nil, false)
}

// RetrySchemaRejectedAttempt only requeues a turn whose native transcript proves
// that the API rejected its schema before any assistant output or tool call.
func (s *Store) RetrySchemaRejectedAttempt(ctx context.Context, source Source, jobID int64, reason string, transcript []byte) error {
	if len(transcript) == 0 {
		return ErrUncertain
	}
	return s.recoverAttempt(ctx, source, jobID, reason, transcript, false)
}

func (s *Store) RetryBindingRejectedAttempt(ctx context.Context, source Source, jobID int64, reason string) error {
	return s.recoverAttempt(ctx, source, jobID, reason, nil, true)
}

// These exact errors are emitted by pinned codex v0.7.3 before app-server
// starts. Do not accept arbitrary configuration errors or prefix matches.
// The operator must restore the reviewed source before requesting a retry.
const bindingRejection = "codex: interactive configuration blocked: unreviewed or duplicate per-run MCP binding"
const unexpectedSourceRejection = "codex: interactive configuration blocked: unexpected configuration source"

// interruptedTurn is emitted by pinned teslashibe/codex v0.7.0 when a turn notification
// arrives with status other than completed. The real status is not preserved.
const interruptedTurn = "codex: interactive run: turn failed or interrupted"

func interruptedFailure(failure string) bool {
	switch failure {
	case "codex: context canceled",
		"codex: context deadline exceeded",
		"codex: interactive run: context canceled",
		"codex: interactive run: context deadline exceeded",
		interruptedTurn:
		return true
	default:
		return false
	}
}

func (s *Store) recoverAttempt(ctx context.Context, source Source, jobID int64, reason string, transcript []byte, prelaunch bool) error {
	if jobID <= 0 || strings.TrimSpace(reason) == "" || len(reason) > 4096 {
		return errors.New("explicit job ID and audit reason required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var paused, invalid bool
	if err = tx.QueryRowContext(ctx, `SELECT paused,source_invalid FROM sources WHERE source=?`, source.key()).Scan(&paused, &invalid); err != nil {
		return err
	}
	if !paused || invalid {
		return ErrUncertain
	}
	var state, failure, prompt, session string
	if err = tx.QueryRowContext(ctx, `SELECT j.state,j.error,j.prompt,s.session FROM jobs j JOIN sources s ON s.source=j.source WHERE j.id=? AND j.source=?`, jobID, source.key()).Scan(&state, &failure, &prompt, &session); err != nil {
		return err
	}
	if state != "unknown" {
		return ErrUncertain
	}
	if prelaunch {
		if failure != bindingRejection && failure != unexpectedSourceRejection {
			return ErrUncertain
		}
		var effects int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM tool_operations WHERE job_id IN (SELECT id FROM jobs WHERE source=?) AND (job_id=? OR (state IN ('dispatching','unknown') AND resolution=''))`, source.key(), jobID).Scan(&effects); err != nil {
			return err
		}
		if effects != 0 {
			return ErrUncertain
		}
	} else if transcript != nil {
		if !strings.Contains(failure, "invalid_json_schema") || !strings.Contains(failure, "text.format.schema") || !strings.Contains(failure, "400") {
			return ErrUncertain
		}
		if err := verifySchemaRejection(transcript, session, prompt); err != nil {
			return err
		}
	} else if !interruptedFailure(failure) {
		return ErrUncertain
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM tool_operations WHERE job_id IN (SELECT id FROM jobs WHERE source=?) AND state IN ('dispatching','unknown') AND resolution=''`, source.key()).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return ErrUncertain
	}
	for _, q := range []string{
		`SELECT count(*) FROM jobs WHERE source=? AND id!=? AND state IN ('unknown','running')`,
		`SELECT count(*) FROM replies WHERE job_id IN (SELECT id FROM jobs WHERE source=?) AND (job_id=? OR state!='submitted')`,
		`SELECT count(*) FROM reminders WHERE source=? AND (? > 0) AND status IN ('unknown','dispatching')`,
	} {
		if err = tx.QueryRowContext(ctx, q, source.key(), jobID).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return ErrUncertain
		}
	}
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='note_actions'`).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM note_actions WHERE source=? AND (job_id=? OR state IN ('started','created_unshared','sharing_unverified','unknown'))`, source.key(), jobID).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return ErrUncertain
		}
	}
	audit := "OUTSTANDING REQUEST; interrupted attempt resolved without replay; original error: " + failure + "; operator review: " + reason
	state = "failed"
	if transcript != nil {
		state = "queued"
		audit = fmt.Sprintf("SCHEMA REJECTION RETRY; transcript sha256=%x; original error: %s; operator review: %s", sha256.Sum256(transcript), failure, reason)
	}
	if prelaunch {
		state = "queued"
		audit = "PRELAUNCH CONFIGURATION REJECTION RETRY; pinned codex v0.7.3 rejected before app-server launch; original error: " + failure + "; operator review: " + reason
	}
	if _, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS recovery_audit (id INTEGER PRIMARY KEY, job_id INTEGER NOT NULL REFERENCES jobs(id), created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP, detail TEXT NOT NULL)`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO recovery_audit(job_id,detail) VALUES (?,?)`, jobID, audit); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE jobs SET state=?,error=? WHERE id=? AND source=?`, state, audit, jobID, source.key()); err != nil {
		return err
	}
	if transcript != nil || prelaunch {
		// Renew queue eligibility while preserving the original acceptance time
		// in the append-only operator audit.
		var accepted int64
		if err = tx.QueryRowContext(ctx, `SELECT created_at FROM jobs WHERE id=?`, jobID).Scan(&accepted); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO recovery_audit(job_id,detail) VALUES (?,?)`, jobID, fmt.Sprintf("original accepted_at_ns=%d", accepted)); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE jobs SET created_at=? WHERE id=?`, time.Now().UnixNano(), jobID); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE sources SET paused=0 WHERE source=?`, source.key()); err != nil {
		return err
	}
	return tx.Commit()
}
