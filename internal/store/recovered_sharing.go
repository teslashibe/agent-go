package store

import (
	"context"
	"database/sql"
	"errors"
)

// Only an explicitly abandoned creation attempt may be rechecked. Discovery
// alone must never promote an in-flight or unresolved sharing operation.
const recoveredSharingPredicate = `s.source=? AND s.id=? AND s.state='sharing_unverified'
 AND EXISTS(SELECT 1 FROM note_actions a JOIN jobs j ON j.id=a.job_id
 JOIN native_note_claims n ON n.job_id=a.job_id
 JOIN tool_operations o ON o.job_id=n.job_id AND o.operation_id=n.operation_id
 WHERE a.source=s.source AND a.note_id=s.id AND a.action='create_shared_note'
 AND j.state='failed' AND o.resolution='abandoned')
 AND NOT EXISTS(SELECT 1 FROM note_actions a JOIN jobs j ON j.id=a.job_id
 LEFT JOIN native_note_claims n ON n.job_id=a.job_id
 LEFT JOIN tool_operations o ON o.job_id=n.job_id AND o.operation_id=n.operation_id
 WHERE a.source=s.source AND a.note_id=s.id AND
 (j.state IN ('queued','running','unknown') OR (o.state IN ('dispatching','unknown') AND o.resolution='')))`

func (s *Store) CanVerifyRecoveredSharing(ctx context.Context, source Source, id string) (bool, error) {
	if source.Validate() != nil || !source.Group {
		return false, nil
	}
	var found int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM note_scopes s WHERE `+recoveredSharingPredicate, source.key(), id).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return found == 1, err
}

// ConfirmRecoveredSharing follows fresh native verification of the configured
// participants. It changes only the current scope, preserving original errors,
// creation progress and the abandoned operation's uncertain outcome.
func (s *Store) ConfirmRecoveredSharing(ctx context.Context, source Source, id string, jobID int64, operationID string) error {
	if source.Validate() != nil || !source.Group || operationID == "" {
		return ErrUncertain
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var claimed int
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM jobs j JOIN sources s ON s.source=j.source
 JOIN tool_operations o ON o.job_id=j.id WHERE j.id=? AND j.source=? AND j.state='running'
 AND s.paused=0 AND s.source_invalid=0 AND o.operation_id=? AND o.state='dispatching' AND o.resolution=''`, jobID, source.key(), operationID).Scan(&claimed)
	if err != nil {
		return err
	}
	if claimed != 1 {
		return ErrUncertain
	}
	result, err := tx.ExecContext(ctx, `UPDATE note_scopes AS s SET state='shared_verified' WHERE `+recoveredSharingPredicate, source.key(), id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrUncertain
	}
	// This table already exists after the required reviewed recovery.
	_, err = tx.ExecContext(ctx, `INSERT INTO recovery_audit(job_id,detail) VALUES(?,?)`, jobID, "Fresh native verification of configured participants authorized recovered note "+id+"; prior attempt evidence preserved")
	if err != nil {
		return err
	}
	return tx.Commit()
}
