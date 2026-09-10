package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var (
	ErrUninitialized = errors.New("source is not initialized")
	ErrUncertain     = errors.New("conversation is paused pending manual recovery")
	ErrBusy          = errors.New("conversation is busy")
)

// Source binds a cursor and conversation to exact configured identities.
// Name identifies the upstream database, not a message-provided field.
type Source struct {
	Name           string
	Sender         string // Legacy direct-chat identity; cannot accompany AllowedSenders.
	ChatGUID       string
	ChatID         int64
	AllowedSenders []string `json:",omitempty"`
	Group          bool     `json:",omitempty"`
}

func (s Source) Validate() error {
	if s.Name == "" || s.ChatGUID == "" || s.ChatID <= 0 ||
		(s.Sender != "" && (s.Group || len(s.AllowedSenders) != 0)) ||
		(s.Sender == "" && len(s.AllowedSenders) == 0) {
		return errors.New("exact source, allowed senders, and chat identities are required")
	}
	senders := s.AllowedSenders
	if s.Sender != "" {
		senders = []string{s.Sender}
	}
	for _, sender := range senders {
		if sender == "" || strings.TrimSpace(sender) != sender || strings.ContainsAny(sender, "\r\n") {
			return errors.New("invalid allowed sender identity")
		}
	}
	return nil
}

func (s Source) AllowsSender(sender string) bool {
	if len(s.AllowedSenders) > 0 {
		return slices.Contains(s.AllowedSenders, sender)
	}
	return !s.Group && sender != "" && sender == s.Sender
}

func (s Source) key() string {
	s.AllowedSenders = slices.Clone(s.AllowedSenders)
	slices.Sort(s.AllowedSenders)
	s.AllowedSenders = slices.Compact(s.AllowedSenders)
	// Preserve existing single-sender direct-chat state for owner fallback and
	// its equivalent allowed_senders configuration. Groups always have a set.
	if !s.Group && s.Sender == "" && len(s.AllowedSenders) == 1 {
		s.Sender = s.AllowedSenders[0]
		s.AllowedSenders = nil
	}
	b, _ := json.Marshal(s)
	return string(b)
}

type Store struct{ db *sql.DB }

// Open is for a single process owner. Do not open the same database in another
// worker: opening performs crash recovery before returning.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	_, err = db.Exec(`PRAGMA busy_timeout = 5000;
PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;
CREATE TABLE IF NOT EXISTS sources (
 source TEXT PRIMARY KEY, cursor INTEGER NOT NULL, generation TEXT NOT NULL,
 session TEXT NOT NULL DEFAULT '', paused INTEGER NOT NULL DEFAULT 0,
 source_invalid INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS source_anchors (
 source TEXT PRIMARY KEY REFERENCES sources(source), message_id INTEGER NOT NULL,
 guid TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS inbox (
 source TEXT NOT NULL REFERENCES sources(source), guid TEXT NOT NULL,
 message_id INTEGER NOT NULL, disposition TEXT NOT NULL,
 PRIMARY KEY(source, guid)
);
CREATE TABLE IF NOT EXISTS jobs (
 id INTEGER PRIMARY KEY, source TEXT NOT NULL REFERENCES sources(source),
 guid TEXT NOT NULL, prompt TEXT NOT NULL, created_at INTEGER NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('queued','running','completed','failed','unknown')),
 error TEXT NOT NULL DEFAULT '', ack_reaction TEXT NOT NULL DEFAULT '',
 ack_state TEXT NOT NULL DEFAULT 'pending'
 CHECK(ack_state IN ('pending','dispatching','submitted','unknown')), UNIQUE(source,guid)
);
CREATE TABLE IF NOT EXISTS replies (
 id INTEGER PRIMARY KEY, job_id INTEGER NOT NULL REFERENCES jobs(id),
 ordinal INTEGER NOT NULL, text TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('pending','dispatching','submitted','unknown')),
 UNIQUE(job_id,ordinal)
);
BEGIN IMMEDIATE;
UPDATE sources SET paused=1 WHERE source IN (
 SELECT source FROM jobs WHERE state IN ('running','unknown')
 UNION SELECT j.source FROM jobs j JOIN replies r ON r.job_id=j.id
 WHERE r.state IN ('dispatching','unknown')
);
UPDATE jobs SET state='unknown', error='interrupted turn' WHERE state='running';
UPDATE replies SET state='unknown' WHERE state='dispatching';
COMMIT;`)
	if err != nil {
		db.Close()
		return nil, err
	}
	if err = s.migrateToolOperations(); err != nil {
		db.Close()
		return nil, err
	}
	if err = s.migrateReminders(); err != nil {
		db.Close()
		return nil, err
	}
	if err = s.migrateAcknowledgements(); err != nil {
		db.Close()
		return nil, err
	}
	if err = s.migrateHistory(); err != nil {
		db.Close()
		return nil, err
	}
	if err = s.migrateApprovals(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrateAcknowledgements() error {
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM pragma_table_info('jobs') WHERE name='ack_state'`).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		if _, err := s.db.Exec(`ALTER TABLE jobs ADD COLUMN ack_state TEXT NOT NULL DEFAULT 'pending'`); err != nil {
			return err
		}
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM pragma_table_info('jobs') WHERE name='ack_reaction'`).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		if _, err := s.db.Exec(`ALTER TABLE jobs ADD COLUMN ack_reaction TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM pragma_table_info('jobs') WHERE name='ack_outcome'`).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		if _, err := s.db.Exec(`ALTER TABLE jobs ADD COLUMN ack_outcome TEXT NOT NULL DEFAULT '' CHECK(ack_outcome IN ('','accepted','skipped','unknown'))`); err != nil {
			return err
		}
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM pragma_table_info('jobs') WHERE name='ack_evidence'`).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		if _, err := s.db.Exec(`ALTER TABLE jobs ADD COLUMN ack_evidence TEXT NOT NULL DEFAULT '' CHECK(ack_evidence IN ('','verified_on_sender','not_started'))`); err != nil {
			return err
		}
	}
	_, err := s.db.Exec(`BEGIN IMMEDIATE;
UPDATE jobs SET ack_outcome='unknown' WHERE ack_state='dispatching' AND ack_outcome='';
UPDATE jobs SET ack_state='unknown' WHERE ack_state='dispatching';
COMMIT;`)
	return err
}

func (s *Store) Close() error { return s.db.Close() }

// Initialize inserts a first-boot baseline, but never moves an existing cursor.
// The parent must obtain the baseline from the actual upstream source.
func (s *Store) Initialize(ctx context.Context, source Source, baseline int64, generation string) error {
	if source.Validate() != nil || baseline < 0 {
		return errors.New("invalid source identity or baseline")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO sources(source,cursor,generation) VALUES(?,?,?) ON CONFLICT(source) DO NOTHING`, source.key(), baseline, generation)
	return err
}

// Anchor is a row identity observed in upstream chat history, not a database generation.
type Anchor struct {
	ID   int64
	GUID string
}

// CheckHistory initializes only a new source, then verifies persisted identity
// against real upstream history before any worker or reply dispatcher starts.
// The caller supplies a bounded window for the configured chat. Missing evidence
// (including legacy state without an inbox cursor or initial anchor) fails closed.
func (s *Store) CheckHistory(ctx context.Context, source Source, history []Anchor) error {
	if err := source.Validate(); err != nil {
		return err
	}
	var latest Anchor
	for _, row := range history {
		if row.ID <= 0 || row.GUID == "" {
			return errors.New("invalid upstream history identity")
		}
		if row.ID > latest.ID {
			latest = row
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Never attach a newly observed anchor to existing state.
	if latest.ID > 0 {
		result, err := tx.ExecContext(ctx, `INSERT INTO sources(source,cursor,generation) VALUES(?,?,'') ON CONFLICT(source) DO NOTHING`, source.key(), latest.ID)
		if err != nil {
			return err
		}
		inserted, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if inserted == 1 {
			if _, err = tx.ExecContext(ctx, `INSERT INTO source_anchors(source,message_id,guid) VALUES(?,?,?)`, source.key(), latest.ID, latest.GUID); err != nil {
				return err
			}
		}
	}
	var cursor int64
	var paused bool
	if err = tx.QueryRowContext(ctx, `SELECT cursor,paused FROM sources WHERE source=?`, source.key()).Scan(&cursor, &paused); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrUninitialized
		}
		return err
	}
	if paused {
		return ErrUncertain
	}
	var anchor Anchor
	err = tx.QueryRowContext(ctx, `SELECT message_id,guid FROM inbox WHERE source=? AND message_id=?`, source.key(), cursor).Scan(&anchor.ID, &anchor.GUID)
	if errors.Is(err, sql.ErrNoRows) {
		err = tx.QueryRowContext(ctx, `SELECT message_id,guid FROM source_anchors WHERE source=?`, source.key()).Scan(&anchor.ID, &anchor.GUID)
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	// An explicitly bootstrapped empty DM has no previous message to verify.
	// Keep cursor zero so the first message, including one arriving while the
	// transport reconnects, is delivered as live intake rather than a baseline.
	if !source.Group && cursor == 0 && err == nil && anchor.ID == 0 && anchor.GUID == "empty-dm" {
		return tx.Commit()
	}
	matched := false
	for _, row := range history {
		if row.ID == anchor.ID {
			if row.GUID != anchor.GUID {
				matched = false
				break
			}
			matched = true
		}
	}
	if cursor > latest.ID || !matched {
		if _, err = tx.ExecContext(ctx, `UPDATE sources SET paused=1,source_invalid=1 WHERE source=?`, source.key()); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		return fmt.Errorf("%w: upstream rowID/GUID anchor missing or changed; manual source recovery required", ErrUncertain)
	}
	return tx.Commit()
}

func (s *Store) Cursor(ctx context.Context, source Source) (int64, error) {
	var cursor int64
	err := s.db.QueryRowContext(ctx, `SELECT cursor FROM sources WHERE source=?`, source.key()).Scan(&cursor)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrUninitialized
	}
	return cursor, err
}

// CheckSource validates real upstream observations. Generation may be empty if
// unavailable, but must be supplied consistently. A lower high-water mark or a
// changed generation durably pauses the source; this never resets its cursor.
func (s *Store) CheckSource(ctx context.Context, source Source, generation string, highWater int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var cursor int64
	var saved string
	var paused bool
	err = tx.QueryRowContext(ctx, `SELECT cursor,generation,paused FROM sources WHERE source=?`, source.key()).Scan(&cursor, &saved, &paused)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrUninitialized
	}
	if err != nil {
		return err
	}
	if paused {
		return ErrUncertain
	}
	if cursor > highWater || saved != generation {
		if _, err = tx.ExecContext(ctx, `UPDATE sources SET paused=1,source_invalid=1 WHERE source=?`, source.key()); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		return ErrUncertain
	}
	return tx.Commit()
}

type Event struct {
	Sender    string // Authenticated transport identity, never parsed from prompt.
	ID        int64
	GUID      string
	Text      string
	CreatedAt time.Time
}

type Receipt struct {
	Disposition string
	Duplicate   bool
}

// Duplicate reports whether this exact upstream message was already accepted.
func (s *Store) Duplicate(ctx context.Context, source Source, event Event) (bool, error) {
	if event.ID <= 0 || event.GUID == "" {
		return false, errors.New("message ID and GUID are required")
	}
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT message_id FROM inbox WHERE source=? AND guid=?`, source.key(), event.GUID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return id == event.ID, err
}

// Accept persists the inbox decision and cursor in the same transaction.
// action is one of rejected, expired, turn, status, new. At most five unfinished
// turns (including the running turn and unsent replies) are admitted.
func (s *Store) Accept(ctx context.Context, source Source, event Event, action string) (Receipt, error) {
	if event.ID <= 0 || event.GUID == "" {
		return Receipt{}, errors.New("message ID and GUID are required")
	}
	switch action {
	case "rejected", "expired", "turn", "status", "new":
	default:
		return Receipt{}, errors.New("invalid inbox action")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Receipt{}, err
	}
	defer tx.Rollback()
	var cursor int64
	var paused bool
	err = tx.QueryRowContext(ctx, `SELECT cursor,paused FROM sources WHERE source=?`, source.key()).Scan(&cursor, &paused)
	if errors.Is(err, sql.ErrNoRows) {
		return Receipt{}, ErrUninitialized
	}
	if err != nil {
		return Receipt{}, err
	}
	var priorID int64
	var priorAction string
	err = tx.QueryRowContext(ctx, `SELECT message_id,disposition FROM inbox WHERE source=? AND guid=?`, source.key(), event.GUID).Scan(&priorID, &priorAction)
	if err == nil && priorID == event.ID {
		return Receipt{Disposition: priorAction, Duplicate: true}, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Receipt{}, err
	}
	if event.ID <= cursor || err == nil {
		if _, err = tx.ExecContext(ctx, `UPDATE sources SET paused=1,source_invalid=1 WHERE source=?`, source.key()); err != nil {
			return Receipt{}, err
		}
		if err = tx.Commit(); err != nil {
			return Receipt{}, err
		}
		return Receipt{}, ErrUncertain
	}
	// An explicit archive is evidence only. Catch transport backlog here without
	// enqueueing it, while allowing the normal cursor/inbox transaction to advance.
	var archivedID int64
	err = tx.QueryRowContext(ctx, `SELECT message_id FROM history_messages WHERE source=? AND guid=?`, source.key(), event.GUID).Scan(&archivedID)
	if err == nil {
		if archivedID != event.ID {
			return Receipt{}, ErrUncertain
		}
		action = "rejected"
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Receipt{}, err
	}
	// The absence of an inbox row is expected for a new message.
	err = nil
	// Status and rejected input remain observable while the worker is paused.
	if paused && action != "status" && action != "rejected" && action != "expired" {
		return Receipt{}, ErrUncertain
	}
	if action == "turn" || action == "new" {
		var count int
		err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs j WHERE source=? AND
 (state IN ('queued','running','unknown') OR EXISTS(SELECT 1 FROM replies r WHERE r.job_id=j.id AND r.state!='submitted'))`, source.key()).Scan(&count)
		if err != nil {
			return Receipt{}, err
		}
		if action == "new" && count > 0 {
			action = "busy"
		}
		if action == "turn" && count >= 5 {
			action = "full"
		}
	}
	if action == "turn" {
		_, err = tx.ExecContext(ctx, `INSERT INTO jobs(source,guid,prompt,created_at,state,sender) VALUES(?,?,?,?,'queued',?)`, source.key(), event.GUID, event.Text, event.CreatedAt.UnixNano(), event.Sender)
	} else if action == "new" {
		// /new is an idle-only group-wide reset. The inbox marker also bounds
		// historical context so old conversation cannot reappear on the next turn.
		_, err = tx.ExecContext(ctx, `UPDATE sources SET session='' WHERE source=?`, source.key())
		if err == nil {
			_, err = tx.ExecContext(ctx, `DELETE FROM reminder_clarifications WHERE source=?`, source.key())
		}
		if err == nil {
			_, err = tx.ExecContext(ctx, `DELETE FROM dm_history_pending_context WHERE source=?`, source.key())
		}
	}
	if err != nil {
		return Receipt{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO inbox(source,guid,message_id,disposition) VALUES(?,?,?,?)`, source.key(), event.GUID, event.ID, action); err != nil {
		return Receipt{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE sources SET cursor=? WHERE source=?`, event.ID, source.key()); err != nil {
		return Receipt{}, err
	}
	if err = tx.Commit(); err != nil {
		return Receipt{}, err
	}
	return Receipt{Disposition: action}, nil
}

type Status struct {
	Cursor            int64
	SessionID         string
	Paused            bool
	Queued            int
	Running           int
	UnknownTurns      int
	UnresolvedReplies int
	PendingReminders  int
	UnknownReminders  int
}

func (s *Store) Status(ctx context.Context, source Source) (Status, error) {
	var status Status
	err := s.db.QueryRowContext(ctx, `SELECT cursor,session,paused,
 (SELECT COUNT(*) FROM jobs WHERE source=s.source AND state='queued'),
 (SELECT COUNT(*) FROM jobs WHERE source=s.source AND state='running'),
 (SELECT COUNT(*) FROM jobs WHERE source=s.source AND state='unknown'),
 (SELECT COUNT(*) FROM replies r JOIN jobs j ON j.id=r.job_id WHERE j.source=s.source AND r.state!='submitted'),
 (SELECT COUNT(*) FROM reminders WHERE source=s.source AND status='pending'),
 (SELECT COUNT(*) FROM reminders WHERE source=s.source AND status='unknown')
 FROM sources s WHERE source=?`, source.key()).Scan(&status.Cursor, &status.SessionID, &status.Paused, &status.Queued, &status.Running, &status.UnknownTurns, &status.UnresolvedReplies, &status.PendingReminders, &status.UnknownReminders)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrUninitialized
	}
	return status, err
}

type Job struct {
	Sender    string
	UserID    string
	ID        int64
	GUID      string
	Prompt    string
	SessionID string
	Ack       bool
	Reaction  string
}

type Reply struct {
	ID   int64
	Text string
}

// ClaimNext atomically expires stale queued turns and claims either the oldest
// pending reply or the next turn. Only one caller may own a source's worker.
func (s *Store) ClaimNext(ctx context.Context, source Source, now time.Time) (*Job, *Reply, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	var paused bool
	var session string
	err = tx.QueryRowContext(ctx, `SELECT paused,session FROM sources WHERE source=?`, source.key()).Scan(&paused, &session)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ErrUninitialized
	}
	if err != nil {
		return nil, nil, err
	}
	if paused {
		return nil, nil, ErrUncertain
	}
	var busy int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs j WHERE source=? AND (state='running' OR EXISTS(SELECT 1 FROM replies r WHERE r.job_id=j.id AND r.state='dispatching'))`, source.key()).Scan(&busy); err != nil {
		return nil, nil, err
	}
	if busy > 0 {
		return nil, nil, ErrBusy
	}
	reply := &Reply{}
	err = tx.QueryRowContext(ctx, `SELECT r.id,r.text FROM replies r JOIN jobs j ON j.id=r.job_id WHERE j.source=? AND r.state='pending' ORDER BY j.id,r.ordinal LIMIT 1`, source.key()).Scan(&reply.ID, &reply.Text)
	if err == nil {
		if _, err = tx.ExecContext(ctx, `UPDATE replies SET state='dispatching' WHERE id=?`, reply.ID); err != nil {
			return nil, nil, err
		}
		if err = tx.Commit(); err != nil {
			return nil, nil, err
		}
		return nil, reply, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE jobs SET state='failed',error='expired' WHERE source=? AND state='queued' AND created_at<?`, source.key(), now.Add(-15*time.Minute).UnixNano()); err != nil {
		return nil, nil, err
	}
	job := &Job{SessionID: session}
	var ackState string
	err = tx.QueryRowContext(ctx, `SELECT id,guid,prompt,sender,ack_state,ack_reaction FROM jobs WHERE source=? AND state='queued' ORDER BY id LIMIT 1`, source.key()).Scan(&job.ID, &job.GUID, &job.Prompt, &job.Sender, &ackState, &job.Reaction)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, tx.Commit()
	}
	if err != nil {
		return nil, nil, err
	}
	err = tx.QueryRowContext(ctx, `SELECT user_id FROM profiles WHERE source=? AND sender=? AND active=1`, source.key(), job.Sender).Scan(&job.UserID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, nil, err
	}
	job.Ack = ackState == "pending"
	if _, err = tx.ExecContext(ctx, `UPDATE jobs SET state='running',ack_state=CASE WHEN ack_state='pending' THEN 'dispatching' ELSE ack_state END WHERE id=?`, job.ID); err != nil {
		return nil, nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, nil, err
	}
	return job, nil, nil
}

func (s *Store) SaveAcknowledgementChoice(ctx context.Context, source Source, jobID int64, reaction string) error {
	return changed(s.db.ExecContext(ctx, `UPDATE jobs SET ack_reaction=? WHERE id=? AND source=? AND state='running' AND ack_state='dispatching' AND ack_reaction=''`, reaction, jobID, source.key()))
}

// ack_state remains the legacy scheduling/recovery marker. ack_outcome records
// observed outcomes separately, without relabeling historical submitted rows.
func (s *Store) FinishAcknowledgement(ctx context.Context, source Source, jobID int64, outcome string) error {
	return s.FinishAcknowledgementReceipt(ctx, source, jobID, outcome, "")
}

// FinishAcknowledgementReceipt persists bounded native evidence atomically with
// the existing scheduling marker. Historical outcomes are never relabeled.
func (s *Store) FinishAcknowledgementReceipt(ctx context.Context, source Source, jobID int64, outcome, evidence string) error {
	if outcome != "accepted" && outcome != "skipped" && outcome != "unknown" {
		return errors.New("invalid acknowledgement outcome")
	}
	if evidence != "" && !(evidence == "verified_on_sender" && outcome == "accepted") && !(evidence == "not_started" && outcome == "skipped") {
		return errors.New("inconsistent acknowledgement evidence")
	}
	state := "submitted"
	if outcome == "unknown" {
		state = "unknown"
	}
	return changed(s.db.ExecContext(ctx, `UPDATE jobs SET ack_state=?,ack_outcome=?,ack_evidence=? WHERE id=? AND source=? AND state='running' AND ack_state='dispatching'`, state, outcome, evidence, jobID, source.key()))
}

func (s *Store) AcknowledgementOutcome(ctx context.Context, source Source, jobID int64) (string, error) {
	var outcome string
	err := s.db.QueryRowContext(ctx, `SELECT CASE WHEN ack_evidence!='' THEN ack_evidence WHEN ack_outcome='' THEN 'unrecorded' ELSE ack_outcome END FROM jobs WHERE id=? AND source=?`, jobID, source.key()).Scan(&outcome)
	return outcome, err
}

// CompleteTurn saves the resumable session and every reply chunk atomically before
// any send is attempted. An empty session preserves the existing session.
func (s *Store) CompleteTurn(ctx context.Context, source Source, jobID int64, session string, chunks []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = completeTurn(ctx, tx, source, jobID, session, chunks); err != nil {
		return err
	}
	return tx.Commit()
}

// FailTurn records a deterministic application-level rejection and its reply.
// It is used only when no application-managed external action was started.
func (s *Store) FailTurn(ctx context.Context, source Source, jobID int64, session, reason string, chunks []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE jobs SET state='failed',error=?,ack_state=CASE WHEN ack_state='dispatching' THEN 'submitted' ELSE ack_state END WHERE id=? AND source=? AND state='running'`, reason, jobID, source.key())
	if err = changed(result, err); err != nil {
		return err
	}
	if session != "" {
		if _, err = tx.ExecContext(ctx, `UPDATE sources SET session=? WHERE source=?`, session, source.key()); err != nil {
			return err
		}
	}
	if err = insertReplyChunks(ctx, tx, jobID, chunks, "pending"); err != nil {
		return err
	}
	return tx.Commit()
}

func completeTurn(ctx context.Context, tx *sql.Tx, source Source, jobID int64, session string, chunks []string) error {
	result, err := tx.ExecContext(ctx, `UPDATE jobs SET state='completed',ack_state=CASE WHEN ack_state='dispatching' THEN 'submitted' ELSE ack_state END WHERE id=? AND source=? AND state='running'`, jobID, source.key())
	if err = changed(result, err); err != nil {
		return err
	}
	if session != "" {
		_, err = tx.ExecContext(ctx, `UPDATE sources SET session=? WHERE source=?`, session, source.key())
		if err != nil {
			return err
		}
	}
	if err = insertReplyChunks(ctx, tx, jobID, chunks, "pending"); err != nil {
		return err
	}
	// Consume startup context in the same transaction as the session and replies.
	_, err = tx.ExecContext(ctx, `DELETE FROM dm_history_pending_context WHERE source=?`, source.key())
	return err
}

// RecordSubmittedReply persists a chat text that was already sent during a
// running turn, such as a coding progress update. Later CompleteTurn chunks
// continue after these ordinals.
func (s *Store) RecordSubmittedReply(ctx context.Context, source Source, jobID int64, text string) error {
	if strings.TrimSpace(text) == "" {
		return errors.New("reply text is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var running int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs WHERE id=? AND source=? AND state='running'`, jobID, source.key()).Scan(&running); err != nil {
		return err
	}
	if running != 1 {
		return errors.New("invalid queue state transition")
	}
	if err = insertReplyChunks(ctx, tx, jobID, []string{text}, "submitted"); err != nil {
		return err
	}
	return tx.Commit()
}

func insertReplyChunks(ctx context.Context, tx *sql.Tx, jobID int64, chunks []string, state string) error {
	var next int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(ordinal)+1,0) FROM replies WHERE job_id=?`, jobID).Scan(&next); err != nil {
		return err
	}
	for i, text := range chunks {
		if _, err := tx.ExecContext(ctx, `INSERT INTO replies(job_id,ordinal,text,state) VALUES(?,?,?,?)`, jobID, next+i, text, state); err != nil {
			return err
		}
	}
	return nil
}

func changed(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("invalid queue state transition")
	}
	return nil
}

// MarkTurnUnknown is conservative: an external error cannot prove no work occurred.
func (s *Store) MarkTurnUnknown(ctx context.Context, source Source, jobID int64, cause error) error {
	return s.markUnknown(ctx, source, jobID, 0, fmt.Sprint(cause))
}

func (s *Store) MarkReplyUnknown(ctx context.Context, source Source, replyID int64) error {
	return s.markUnknown(ctx, source, 0, replyID, "")
}

func (s *Store) markUnknown(ctx context.Context, source Source, jobID, replyID int64, reason string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var result sql.Result
	if jobID != 0 {
		result, err = tx.ExecContext(ctx, `UPDATE jobs SET state='unknown',error=? WHERE id=? AND source=? AND state='running'`, reason, jobID, source.key())
	} else {
		result, err = tx.ExecContext(ctx, `UPDATE replies SET state='unknown' WHERE id=? AND state='dispatching' AND job_id IN (SELECT id FROM jobs WHERE source=?)`, replyID, source.key())
	}
	if err = changed(result, err); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE sources SET paused=1 WHERE source=?`, source.key()); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) MarkReplySubmitted(ctx context.Context, source Source, replyID int64) error {
	return changed(s.db.ExecContext(ctx, `UPDATE replies SET state='submitted' WHERE id=? AND state='dispatching' AND job_id IN (SELECT id FROM jobs WHERE source=?)`, replyID, source.key()))
}

// DiscardRepliesAndResume abandons uncertain turns and discards ALL unresolved
// replies while retaining queued turns. It replaces the saved session with session
// (empty starts fresh) and clears only an ordinary pause, not source invalidation.
// It never reruns uncertain turns or resends discarded replies. Call only with the
// worker stopped, after a human has inspected external state.
// Source regressions require ResetSource instead.
func (s *Store) DiscardRepliesAndResume(ctx context.Context, source Source, session string) error {
	return s.resetConversation(ctx, source, session, nil, "")
}

// ResetSource additionally accepts a manually verified source generation and
// baseline. It discards queued work and inbox deduplication for that source.
// It also clears the history anchor: generation-only recovery is insufficient
// for CheckHistory. History-based recovery requires a manually verified anchor
// or explicitly starting with a fresh state database after inspecting old work.
func (s *Store) ResetSource(ctx context.Context, source Source, session string, baseline int64, generation string) error {
	if baseline < 0 {
		return errors.New("invalid baseline")
	}
	return s.resetConversation(ctx, source, session, &baseline, generation)
}

func (s *Store) resetConversation(ctx context.Context, source Source, session string, baseline *int64, generation string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var sourceInvalid bool
	if err = tx.QueryRowContext(ctx, `SELECT source_invalid FROM sources WHERE source=?`, source.key()).Scan(&sourceInvalid); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrUninitialized
		}
		return err
	}
	if sourceInvalid && baseline == nil {
		return ErrUncertain
	}
	var running int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs j WHERE source=? AND (state='running' OR EXISTS(SELECT 1 FROM replies r WHERE r.job_id=j.id AND r.state='dispatching'))`, source.key()).Scan(&running); err != nil {
		return err
	}
	if running > 0 {
		return ErrBusy
	}
	// Close leftover historical approval rows; those commands are no longer accepted.
	if _, err = tx.ExecContext(ctx, `UPDATE approvals SET state='closed',decision='deny' WHERE source=? AND state IN ('waiting','decided')`, source.key()); err != nil {
		return err
	}
	var unresolved int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM tool_operations o JOIN jobs j ON j.id=o.job_id WHERE j.source=? AND o.state IN ('dispatching','unknown') AND o.resolution=''`, source.key()).Scan(&unresolved); err != nil {
		return err
	}
	// A generation reset is not evidence that uncertain external effects were
	// reviewed. Resolve them through DiscardRepliesAndResume first.
	if baseline != nil && unresolved != 0 {
		return ErrUncertain
	}
	if baseline == nil && unresolved != 0 {
		if _, err = tx.ExecContext(ctx, `INSERT INTO tool_operation_reviews(source,job_id,operation_id,arguments,original_state,result,progress,detail)
 SELECT j.source,o.job_id,o.operation_id,o.arguments,o.state,o.result,
 (SELECT COALESCE(json_group_array(json_object('index',p.item_index,'result',p.result)),'[]') FROM tool_progress p WHERE p.job_id=o.job_id AND p.operation_id=o.operation_id),
 'HUMAN-REVIEWED: manually abandoned without replay via DiscardRepliesAndResume'
 FROM tool_operations o JOIN jobs j ON j.id=o.job_id WHERE j.source=? AND o.state IN ('dispatching','unknown') AND o.resolution=''`, source.key()); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE tool_operations SET resolution='abandoned' WHERE job_id IN (SELECT id FROM jobs WHERE source=?) AND state IN ('dispatching','unknown') AND resolution=''`, source.key()); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE jobs SET state='failed',error='manually abandoned' WHERE source=? AND state='unknown'`, source.key()); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM replies WHERE job_id IN (SELECT id FROM jobs WHERE source=?) AND state IN ('unknown','pending')`, source.key()); err != nil {
		return err
	}
	var dispatching int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM reminders WHERE source=? AND status='dispatching'`, source.key()).Scan(&dispatching); err != nil {
		return err
	}
	if dispatching > 0 {
		return ErrBusy
	}
	if _, err = tx.ExecContext(ctx, `UPDATE reminders SET status='cancelled' WHERE source=? AND status='unknown'`, source.key()); err != nil {
		return err
	}
	// Recovery never carries an incomplete action into a replacement session.
	if _, err = tx.ExecContext(ctx, `DELETE FROM reminder_clarifications WHERE source=?`, source.key()); err != nil {
		return err
	}
	if baseline != nil {
		if _, err = tx.ExecContext(ctx, `UPDATE reminders SET status='cancelled' WHERE source=? AND status='pending'`, source.key()); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM replies WHERE job_id IN (SELECT id FROM jobs WHERE source=?)`, source.key()); err != nil {
			return err
		}
		// Reset discards per-job data, but the detached human-review audit above
		// survives job deletion with original arguments, outcomes and progress.
		if _, err = tx.ExecContext(ctx, `DELETE FROM native_note_claims WHERE job_id IN (SELECT id FROM jobs WHERE source=?)`, source.key()); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM tool_progress WHERE job_id IN (SELECT id FROM jobs WHERE source=?)`, source.key()); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM tool_operations WHERE job_id IN (SELECT id FROM jobs WHERE source=?)`, source.key()); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM jobs WHERE source=?`, source.key()); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM inbox WHERE source=?`, source.key()); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM source_anchors WHERE source=?`, source.key()); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE sources SET cursor=?,generation=?,source_invalid=0 WHERE source=?`, *baseline, generation, source.key()); err != nil {
			return err
		}
	}
	if err = changed(tx.ExecContext(ctx, `UPDATE sources SET paused=0,session=? WHERE source=?`, session, source.key())); err != nil {
		return err
	}
	return tx.Commit()
}
