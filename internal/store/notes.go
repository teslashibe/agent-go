package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode"
)

// NoteScope is persisted per exact authenticated source identity.
// Shared family notes are discovered from Notes and refreshed; personal
// unshared notes are never admitted.
type NoteScope struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	State string `json:"state"`
}

func (s *Store) ConfigureNotes(ctx context.Context, source Source, notes []NoteScope) error {
	if err := source.Validate(); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS note_scopes(source TEXT NOT NULL REFERENCES sources(source),id TEXT NOT NULL,title TEXT NOT NULL,state TEXT NOT NULL,PRIMARY KEY(source,id));
 CREATE TABLE IF NOT EXISTS note_actions(job_id INTEGER PRIMARY KEY REFERENCES jobs(id),source TEXT NOT NULL,action TEXT NOT NULL,note_id TEXT NOT NULL DEFAULT '',state TEXT NOT NULL,error TEXT NOT NULL DEFAULT '');
 CREATE TABLE IF NOT EXISTS note_confirmations (
  source TEXT NOT NULL REFERENCES sources(source),sender TEXT NOT NULL,kind TEXT NOT NULL,
  note_id TEXT NOT NULL,title TEXT NOT NULL,modified_at INTEGER NOT NULL,created_at INTEGER NOT NULL,
  PRIMARY KEY(source,sender)
 );`)
	if err != nil {
		return err
	}
	for _, note := range notes {
		if note.ID == "" || note.Title == "" {
			return errors.New("invalid note scope")
		}
		state := note.State
		if state == "" {
			state = "authorized"
		}
		if _, err = s.db.ExecContext(ctx, `INSERT INTO note_scopes(source,id,title,state) VALUES(?,?,?,?) ON CONFLICT(source,id) DO NOTHING`, source.key(), note.ID, note.Title, state); err != nil {
			return err
		}
	}
	return nil
}

// SyncAuthorizedNotes replaces the live authorized set with the shared notes
// just observed in Notes. In-flight created-but-unshared rows are kept.
func (s *Store) SyncAuthorizedNotes(ctx context.Context, source Source, notes []NoteScope) error {
	if err := s.ConfigureNotes(ctx, source, nil); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	seen := map[string]struct{}{}
	for _, note := range notes {
		if note.ID == "" || note.Title == "" {
			return errors.New("invalid note scope")
		}
		seen[note.ID] = struct{}{}
		if _, err = tx.ExecContext(ctx, `INSERT INTO note_scopes(source,id,title,state) VALUES(?,?,?,'authorized') ON CONFLICT(source,id) DO UPDATE SET title=excluded.title,state='authorized' WHERE note_scopes.state NOT IN ('created_unshared','sharing_unverified')`, source.key(), note.ID, note.Title); err != nil {
			return err
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,state FROM note_scopes WHERE source=?`, source.key())
	if err != nil {
		return err
	}
	defer rows.Close()
	var stale []string
	for rows.Next() {
		var id, state string
		if err = rows.Scan(&id, &state); err != nil {
			return err
		}
		if _, ok := seen[id]; ok || state == "created_unshared" || state == "sharing_unverified" || state == "deleted" {
			continue
		}
		stale = append(stale, id)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	for _, id := range stale {
		if _, err = tx.ExecContext(ctx, `UPDATE note_scopes SET state='deleted' WHERE source=? AND id=?`, source.key(), id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// NormalizeNoteName is deliberately conservative: punctuation remains
// significant, while Unicode case, leading/trailing space, and space runs do not.
func NormalizeNoteName(name string) string {
	fields := strings.Fields(name)
	for i, field := range fields {
		fields[i] = strings.Map(func(r rune) rune {
			lowest := r
			for next := unicode.SimpleFold(r); next != r; next = unicode.SimpleFold(next) {
				if folded := unicode.ToLower(next); folded < lowest {
					lowest = folded
				}
			}
			return unicode.ToLower(lowest)
		}, field)
	}
	return strings.Join(fields, " ")
}

func (s *Store) ResolveNote(ctx context.Context, source Source, name string) (NoteScope, []string, error) {
	scope, err := s.Notes(ctx, source)
	if err != nil {
		return NoteScope{}, nil, err
	}
	want := NormalizeNoteName(name)
	var matches []NoteScope
	for _, note := range scope {
		if (note.State == "authorized" || note.State == "shared_verified") && NormalizeNoteName(note.Title) == want {
			matches = append(matches, note)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil, nil
	}
	names := make([]string, 0, len(scope))
	for _, note := range scope {
		if note.State == "authorized" || note.State == "shared_verified" {
			names = append(names, note.Title)
		}
	}
	slices.Sort(names)
	return NoteScope{}, slices.Compact(names), nil
}
func (s *Store) Notes(ctx context.Context, source Source) ([]NoteScope, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,title,state FROM note_scopes WHERE source=? ORDER BY title,id`, source.key())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []NoteScope{}
	for rows.Next() {
		var n NoteScope
		if err := rows.Scan(&n.ID, &n.Title, &n.State); err != nil {
			return nil, err
		}
		result = append(result, n)
	}
	return result, rows.Err()
}
func (s *Store) StartNoteAction(ctx context.Context, source Source, jobID int64, action, noteID string) error {
	result, err := s.db.ExecContext(ctx, `INSERT INTO note_actions(job_id,source,action,note_id,state) SELECT id,source,?,?,'started' FROM jobs WHERE id=? AND source=? AND state='running'`, action, noteID, jobID, source.key())
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("note action has no running authenticated job")
	}
	return nil
}

// StartNativeNoteAction is only for a currently claimed authenticated operation.
// Failed actions require a different operation and a durably terminal predecessor.
func (s *Store) StartNativeNoteAction(ctx context.Context, source Source, jobID int64, operationID, action, noteID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var authorized int
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM tool_operations o JOIN jobs j ON j.id=o.job_id JOIN sources s ON s.source=j.source WHERE j.id=? AND j.source=? AND j.state='running' AND s.paused=0 AND o.operation_id=? AND o.state='dispatching' AND o.resolution=''`, jobID, source.key(), operationID).Scan(&authorized)
	if err != nil {
		return err
	}
	if authorized != 1 {
		return ErrUncertain
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO note_actions(job_id,source,action,note_id,state) VALUES(?,?,?,?,'started') ON CONFLICT(job_id) DO UPDATE SET action=excluded.action,note_id=excluded.note_id,state='started',error='' WHERE note_actions.source=excluded.source AND EXISTS(SELECT 1 FROM native_note_claims n JOIN tool_operations o ON o.job_id=n.job_id AND o.operation_id=n.operation_id WHERE n.job_id=excluded.job_id AND ((note_actions.state='completed' AND (n.operation_id=? OR o.state='completed')) OR (note_actions.state='failed' AND n.operation_id<>? AND o.state='completed')))`, jobID, source.key(), action, noteID, operationID, operationID)
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
	if _, err = tx.ExecContext(ctx, `INSERT INTO native_note_claims(job_id,operation_id) VALUES(?,?) ON CONFLICT(job_id) DO UPDATE SET operation_id=excluded.operation_id`, jobID, operationID); err != nil {
		return err
	}
	return tx.Commit()
}

// RecordCreatedNote persists the external ID before sharing is attempted. It must
// never be rolled back together with a failed external collaboration operation.
func (s *Store) RecordCreatedNote(ctx context.Context, source Source, jobID int64, id, title string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO note_scopes(source,id,title,state) VALUES(?,?,?,'created_unshared')`, source.key(), id, title); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE note_actions SET note_id=?,state='created_unshared' WHERE job_id=? AND source=?`, id, jobID, source.key()); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) FinishNoteAction(ctx context.Context, source Source, jobID int64, state, detail string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE note_actions SET state=?,error=? WHERE job_id=? AND source=?`, state, detail, jobID, source.key())
	return err
}
func (s *Store) MarkNoteSharing(ctx context.Context, source Source, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE note_scopes SET state='sharing_unverified' WHERE source=? AND id=?`, source.key(), id)
	return err
}

func (s *Store) SetNoteShared(ctx context.Context, source Source, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE note_scopes SET state='shared_verified' WHERE source=? AND id=?`, source.key(), id)
	return err
}

// AuthorizePrivateNote completes creation in an explicitly owner-authorized DM.
// Group sources cannot promote an unshared note through this path.
func (s *Store) AuthorizePrivateNote(ctx context.Context, source Source, id string) error {
	if source.Validate() != nil || source.Group || len(source.AllowedSenders) != 1 {
		return errors.New("private Notes require an owner DM")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE note_scopes SET state='authorized' WHERE source=? AND id=? AND state='created_unshared'`, source.key(), id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n != 1 {
		return ErrUncertain
	}
	return err
}

type NoteConfirmation struct {
	Sender     string
	Kind       string
	NoteID     string
	Title      string
	ModifiedAt time.Time
	CreatedAt  time.Time
}

func (s *Store) JobSender(ctx context.Context, source Source, jobID int64) (string, error) {
	var sender string
	err := s.db.QueryRowContext(ctx, `SELECT sender FROM jobs WHERE id=? AND source=? AND state='running'`, jobID, source.key()).Scan(&sender)
	return sender, err
}

// SaveUncertainNoteResult retains the observed result without releasing the
// existing started claim, which continues to block automatic mutation replay.
func (s *Store) SaveUncertainNoteResult(ctx context.Context, source Source, jobID int64, operationID, result string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE tool_operations SET result=? WHERE job_id=? AND operation_id=? AND state='dispatching' AND EXISTS(SELECT 1 FROM jobs WHERE id=? AND source=?)`, result, jobID, operationID, jobID, source.key())
	return err
}
func (s *Store) UncertainNoteResult(ctx context.Context, source Source, jobID int64, operationID string) string {
	var result string
	_ = s.db.QueryRowContext(ctx, `SELECT o.result FROM tool_operations o JOIN jobs j ON j.id=o.job_id WHERE j.source=? AND o.job_id=? AND o.operation_id=? AND o.state IN ('dispatching','unknown')`, source.key(), jobID, operationID).Scan(&result)
	return result
}

// NoteByID validates an exact identity within the current source's live scope.
func (s *Store) NoteByID(ctx context.Context, source Source, id string) (NoteScope, error) {
	var note NoteScope
	err := s.db.QueryRowContext(ctx, `SELECT id,title,state FROM note_scopes WHERE source=? AND id=? AND state IN ('authorized','shared_verified')`, source.key(), id).Scan(&note.ID, &note.Title, &note.State)
	return note, err
}

func (s *Store) SetNoteConfirmation(ctx context.Context, source Source, c NoteConfirmation) error {
	if !source.AllowsSender(c.Sender) || c.NoteID == "" || c.Title == "" || c.ModifiedAt.IsZero() {
		return errors.New("invalid note confirmation")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO note_confirmations(source,sender,kind,note_id,title,modified_at,created_at)
 VALUES(?,?,?,?,?,?,?) ON CONFLICT(source,sender) DO UPDATE SET kind=excluded.kind,note_id=excluded.note_id,title=excluded.title,modified_at=excluded.modified_at,created_at=excluded.created_at`,
		source.key(), c.Sender, c.Kind, c.NoteID, c.Title, c.ModifiedAt.UnixNano(), c.CreatedAt.UnixNano())
	return err
}

func (s *Store) NoteConfirmation(ctx context.Context, source Source, sender string, now time.Time) (NoteConfirmation, error) {
	var c NoteConfirmation
	var modified, created int64
	err := s.db.QueryRowContext(ctx, `SELECT kind,note_id,title,modified_at,created_at FROM note_confirmations WHERE source=? AND sender=?`,
		source.key(), sender).Scan(&c.Kind, &c.NoteID, &c.Title, &modified, &created)
	if err != nil {
		return c, err
	}
	c.Sender, c.ModifiedAt, c.CreatedAt = sender, time.Unix(0, modified), time.Unix(0, created)
	if now.Sub(c.CreatedAt) > 10*time.Minute || now.Before(c.CreatedAt) {
		_ = s.ClearNoteConfirmation(ctx, source, sender)
		return NoteConfirmation{}, sql.ErrNoRows
	}
	return c, nil
}

func (s *Store) ClearNoteConfirmation(ctx context.Context, source Source, sender string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM note_confirmations WHERE source=? AND sender=?`, source.key(), sender)
	if err != nil && strings.Contains(err.Error(), "no such table") {
		return nil
	}
	return err
}

func (s *Store) MarkNoteDeleted(ctx context.Context, source Source, id string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE note_scopes SET state='deleted' WHERE source=? AND id=? AND state!='deleted'`, source.key(), id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("authorized note scope changed")
	}
	return nil
}
