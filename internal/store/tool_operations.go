package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// ClaimToolOperation durably claims one caller-stable operation before any
// external effect. Existing claims are never permission to execute again.
// Arguments must be canonical JSON produced by the typed tool adapter.
func (s *Store) ClaimToolOperation(ctx context.Context, source Source, jobID int64, operationID, arguments string) (claimed bool, result string, err error) {
	if source.Validate() != nil || jobID <= 0 || operationID == "" || arguments == "" {
		return false, "", errors.New("invalid tool operation")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, "", err
	}
	defer tx.Rollback()
	var state string
	if err = tx.QueryRowContext(ctx, `SELECT j.state FROM jobs j JOIN sources s ON s.source=j.source WHERE j.id=? AND j.source=? AND s.paused=0 AND s.source_invalid=0`, jobID, source.key()).Scan(&state); err != nil {
		return false, "", err
	}
	if state != "running" {
		return false, "", errors.New("tool job is not running")
	}
	var storedArguments string
	err = tx.QueryRowContext(ctx, `SELECT arguments,state,result FROM tool_operations WHERE job_id=? AND operation_id=?`, jobID, operationID).Scan(&storedArguments, &state, &result)
	if err == nil {
		if storedArguments != arguments {
			return false, "", errors.New("operation ID reused with different arguments")
		}
		if state != "completed" {
			return false, "", ErrUncertain
		}
		return false, result, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, "", err
	}
	// A reaction is one native attempt per authenticated job, regardless of the
	// model's operation ID. Legacy acknowledgement evidence also reserves it.
	// This check shares the claim transaction, so a new ID cannot bypass it.
	var operation struct{ Name string }
	if json.Unmarshal([]byte(arguments), &operation) == nil && operation.Name == "react" {
		var attempted bool
		if err = tx.QueryRowContext(ctx, `SELECT
   EXISTS(SELECT 1 FROM jobs WHERE id=? AND ack_state!='pending' AND ack_reaction NOT IN ('','none'))
   OR EXISTS(SELECT 1 FROM tool_operations WHERE job_id=? AND CASE WHEN json_valid(arguments) THEN json_extract(arguments,'$.Name')='react' ELSE 0 END)`, jobID, jobID).Scan(&attempted); err != nil {
			return false, "", err
		}
		if attempted {
			return false, "", errors.New("tapback already attempted for this job; no new request sent")
		}
	}
	var unresolved int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM tool_operations o JOIN jobs j ON j.id=o.job_id WHERE j.source=? AND o.state IN ('dispatching','unknown') AND o.resolution=''`, source.key()).Scan(&unresolved); err != nil {
		return false, "", err
	}
	if unresolved != 0 {
		return false, "", ErrUncertain
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO tool_operations(job_id,operation_id,arguments,state,result) VALUES(?,?,?,'dispatching','')`, jobID, operationID, arguments); err != nil {
		return false, "", err
	}
	return true, "", tx.Commit()
}

// CompleteToolOperation persists a verified result. An execution error or lost
// completion leaves the claim unresolved; it must never be blindly retried.
func (s *Store) CompleteToolOperation(ctx context.Context, source Source, jobID int64, operationID, result string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE tool_operations SET state='completed',result=? WHERE job_id=? AND operation_id=? AND state='dispatching' AND EXISTS(SELECT 1 FROM jobs WHERE id=? AND source=? AND state='running')`, result, jobID, operationID, jobID, source.key())
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrUncertain
	}
	return nil
}

func (s *Store) RecordToolProgress(ctx context.Context, source Source, jobID int64, operationID string, index int, result string) error {
	res, err := s.db.ExecContext(ctx, `INSERT INTO tool_progress(job_id,operation_id,item_index,result) SELECT o.job_id,o.operation_id,?,? FROM tool_operations o JOIN jobs j ON j.id=o.job_id WHERE o.job_id=? AND o.operation_id=? AND o.state='dispatching' AND j.source=? AND j.state='running'`, index, result, jobID, operationID, source.key())
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrUncertain
	}
	return nil
}

// ToolProgress reads persisted outcomes for this exact source and job, including
// successful early steps of an incomplete operation. It performs no recovery.
func (s *Store) ToolProgress(ctx context.Context, source Source, jobID int64) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT p.result FROM tool_progress p JOIN jobs j ON j.id=p.job_id WHERE j.source=? AND j.id=? ORDER BY p.rowid`, source.key(), jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var results []string
	for rows.Next() {
		var result string
		if err := rows.Scan(&result); err != nil {
			return nil, err
		}
		results = append(results, result)
	}
	return results, rows.Err()
}

func (s *Store) migrateToolOperations() error {
	// A separate resolution marker preserves the original uncertainty and avoids
	// rebuilding the operation/progress foreign-key graph on existing databases.
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS tool_operation_reviews (
 id INTEGER PRIMARY KEY, source TEXT NOT NULL, job_id INTEGER NOT NULL,
 operation_id TEXT NOT NULL, arguments TEXT NOT NULL, original_state TEXT NOT NULL,
 result TEXT NOT NULL, progress TEXT NOT NULL, detail TEXT NOT NULL,
 created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);`)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`CREATE TABLE IF NOT EXISTS tool_operations (
 job_id INTEGER NOT NULL REFERENCES jobs(id), operation_id TEXT NOT NULL,
 arguments TEXT NOT NULL, state TEXT NOT NULL CHECK(state IN ('dispatching','completed','unknown')),
 result TEXT NOT NULL DEFAULT '', PRIMARY KEY(job_id,operation_id)
);`)
	if err != nil {
		return err
	}
	var count int
	if err = s.db.QueryRow(`SELECT count(*) FROM pragma_table_info('tool_operations') WHERE name='resolution'`).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		if _, err = s.db.Exec(`ALTER TABLE tool_operations ADD COLUMN resolution TEXT NOT NULL DEFAULT '' CHECK(resolution IN ('','abandoned'))`); err != nil {
			return err
		}
	}
	_, err = s.db.Exec(`BEGIN IMMEDIATE;
CREATE TABLE IF NOT EXISTS native_note_claims (
 job_id INTEGER PRIMARY KEY REFERENCES jobs(id), operation_id TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS tool_progress (
 job_id INTEGER NOT NULL, operation_id TEXT NOT NULL, item_index INTEGER NOT NULL,
 result TEXT NOT NULL, PRIMARY KEY(job_id,operation_id,item_index),
 FOREIGN KEY(job_id,operation_id) REFERENCES tool_operations(job_id,operation_id)
);
UPDATE sources SET paused=1 WHERE source IN (SELECT j.source FROM jobs j JOIN tool_operations o ON o.job_id=j.id WHERE o.state IN ('dispatching','unknown') AND o.resolution='');
UPDATE tool_operations SET state='unknown' WHERE state='dispatching';
COMMIT;`)
	return err
}
