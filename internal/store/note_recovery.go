package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
)

// NoteAttemptReview fingerprints the exact evidence to be reviewed externally.
// It exposes no request, note content, recipient, or session data.
type NoteAttemptReview struct {
	SHA256 string `json:"sha256"`
}

const noteReviewSQL = `SELECT json_object(
 'source',j.source,'job',j.id,'guid',j.guid,'prompt',j.prompt,
 'job_state',j.state,'error',j.error,'ack_state',j.ack_state,'ack_reaction',j.ack_reaction,'ack_outcome',ACK_OUTCOME,
 'session',s.session,'generation',s.generation,'cursor',s.cursor,
 'operation',o.operation_id,'arguments',o.arguments,'state',o.state,'result',o.result,'resolution',o.resolution,
 'note_action',json_object('action',a.action,'note_id',a.note_id,'state',a.state,'error',a.error),
 'job_operations',(SELECT json_group_array(json_object('operation',b.operation_id,'arguments',b.arguments,'state',b.state,'result',b.result,'resolution',b.resolution,
   'progress',(SELECT json_group_array(json_object('index',q.item_index,'result',q.result)) FROM (SELECT item_index,result FROM tool_progress WHERE job_id=j.id AND operation_id=b.operation_id ORDER BY item_index) q)))
   FROM (SELECT operation_id,arguments,state,result,resolution FROM tool_operations WHERE job_id=j.id ORDER BY operation_id) b),
 'progress',(SELECT COALESCE(json_group_array(json_object('index',p.item_index,'result',p.result)),'[]')
             FROM (SELECT item_index,result FROM tool_progress WHERE job_id=j.id AND operation_id=o.operation_id ORDER BY item_index) p))
 FROM jobs j JOIN sources s ON s.source=j.source
 JOIN tool_operations o ON o.job_id=j.id
 JOIN native_note_claims n ON n.job_id=j.id AND n.operation_id=o.operation_id
 JOIN note_actions a ON a.job_id=j.id AND a.source=j.source
 WHERE j.id=? AND j.source=? AND o.operation_id=?
 AND j.state='unknown' AND s.paused=1 AND s.source_invalid=0
 AND o.state IN ('dispatching','unknown') AND o.resolution=''
 AND a.state IN ('started','created_unshared','sharing_unverified','unknown')
 AND json_extract(o.arguments,'$.Name')=a.action`

func noteAttemptReview(ctx context.Context, tx *sql.Tx, source Source, jobID int64, operationID string) (NoteAttemptReview, error) {
	if source.Validate() != nil || jobID <= 0 || strings.TrimSpace(operationID) == "" || len(operationID) > 128 {
		return NoteAttemptReview{}, errors.New("exact source, job and operation required")
	}
	// Older live databases have no observed-outcome column. Review must not run
	// migrations or infer an observation from legacy acknowledgement state.
	var hasOutcome bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pragma_table_info('jobs') WHERE name='ack_outcome')`).Scan(&hasOutcome); err != nil {
		return NoteAttemptReview{}, err
	}
	outcome := "'column absent'"
	if hasOutcome {
		outcome = "j.ack_outcome"
	}
	query := strings.Replace(noteReviewSQL, "ACK_OUTCOME", outcome, 1)
	var evidence string
	if err := tx.QueryRowContext(ctx, query, jobID, source.key(), operationID).Scan(&evidence); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return NoteAttemptReview{}, ErrUncertain
		}
		return NoteAttemptReview{}, err
	}
	digest := sha256.Sum256([]byte(evidence))
	return NoteAttemptReview{SHA256: hex.EncodeToString(digest[:])}, nil
}

// ReviewNoteAttempt does not change state. The caller holds the daemon lock and
// reviews the corresponding private operation, progress and external effects.
func (s *Store) ReviewNoteAttempt(ctx context.Context, source Source, jobID int64, operationID string) (NoteAttemptReview, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return NoteAttemptReview{}, err
	}
	defer tx.Rollback()
	return noteAttemptReview(ctx, tx, source, jobID, operationID)
}

// ResolveReviewedNoteAttempt abandons only this reviewed attempt without replay.
// It preserves the original request, error, sessions, note state and effect evidence.
// The request remains outstanding; this is not proof the Notes action succeeded.
func (s *Store) ResolveReviewedNoteAttempt(ctx context.Context, source Source, jobID int64, operationID, expectedSHA256, reason string, abandonUnknownAck bool) error {
	if len(expectedSHA256) != 64 || strings.TrimSpace(reason) == "" || len(reason) > 4096 {
		return errors.New("review fingerprint and explicit external-effect review reason required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	review, err := noteAttemptReview(ctx, tx, source, jobID, operationID)
	if err != nil {
		return err
	}
	if review.SHA256 != expectedSHA256 {
		return ErrUncertain
	}
	var ackState string
	if err := tx.QueryRowContext(ctx, `SELECT ack_state FROM jobs WHERE id=? AND source=?`, jobID, source.key()).Scan(&ackState); err != nil {
		return err
	}
	if ackState == "dispatching" || (ackState == "unknown") != abandonUnknownAck {
		return ErrUncertain
	}
	var busy bool
	for _, check := range []struct {
		query string
		args  []any
	}{
		{`SELECT EXISTS(SELECT 1 FROM jobs WHERE source=? AND id!=? AND (state IN ('running','unknown') OR ack_state='dispatching'))`, []any{source.key(), jobID}},
		{`SELECT EXISTS(SELECT 1 FROM replies r JOIN jobs j ON j.id=r.job_id WHERE j.source=? AND r.state!='submitted')`, []any{source.key()}},
		{`SELECT EXISTS(SELECT 1 FROM approvals WHERE source=? AND state IN ('waiting','decided'))`, []any{source.key()}},
		{`SELECT EXISTS(SELECT 1 FROM reminders WHERE source=? AND status IN ('dispatching','unknown'))`, []any{source.key()}},
		{`SELECT EXISTS(SELECT 1 FROM tool_operations o JOIN jobs j ON j.id=o.job_id WHERE j.source=? AND o.state IN ('dispatching','unknown') AND o.resolution='' AND NOT(o.job_id=? AND o.operation_id=?))`, []any{source.key(), jobID, operationID}},
	} {
		if err := tx.QueryRowContext(ctx, check.query, check.args...).Scan(&busy); err != nil {
			return err
		}
		if busy {
			return ErrUncertain
		}
	}
	detail := "OUTSTANDING REQUEST; reviewed Notes attempt abandoned without replay; evidence sha256=" + expectedSHA256 + "; operator review: " + reason
	if abandonUnknownAck {
		detail += "; reviewed acknowledgement remains unknown and is abandoned without retry; delivery not verified"
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO tool_operation_reviews(source,job_id,operation_id,arguments,original_state,result,progress,detail)
 SELECT j.source,o.job_id,o.operation_id,o.arguments,o.state,o.result,
 (SELECT COALESCE(json_group_array(json_object('index',p.item_index,'result',p.result)),'[]') FROM tool_progress p WHERE p.job_id=o.job_id AND p.operation_id=o.operation_id),?
 FROM tool_operations o JOIN jobs j ON j.id=o.job_id WHERE o.job_id=? AND o.operation_id=?`, detail, jobID, operationID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS recovery_audit (id INTEGER PRIMARY KEY, job_id INTEGER NOT NULL REFERENCES jobs(id), created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP, detail TEXT NOT NULL)`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO recovery_audit(job_id,detail) VALUES(?,?)`, jobID, detail); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tool_operations SET resolution='abandoned' WHERE job_id=? AND operation_id=?`, jobID, operationID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE jobs SET state='failed' WHERE id=? AND source=?`, jobID, source.key()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sources SET paused=0 WHERE source=?`, source.key()); err != nil {
		return err
	}
	return tx.Commit()
}
