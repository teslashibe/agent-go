package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"
)

const (
	MaxHistoryMessages = 10000
	// Leave room for authenticated context, recent turns, and the current message.
	MaxHistoryBytes = 384 * 1024
	MaxRequestBytes = 512 * 1024
)

// HistoryMessage is transport evidence, not an actionable inbox event. Metadata
// preserves attachment/transcript and attribution fields supplied by the export.
type HistoryMessage struct {
	ID        int64           `json:"id"`
	GUID      string          `json:"guid"`
	ChatID    int64           `json:"chat_id"`
	ChatGUID  string          `json:"chat_guid"`
	IsGroup   bool            `json:"is_group"`
	Sender    string          `json:"sender"`
	IsFromMe  bool            `json:"is_from_me"`
	CreatedAt time.Time       `json:"created_at"`
	Text      string          `json:"text"`
	Metadata  json.RawMessage `json:"metadata,omitempty"`
}

// OpenForHistoryImport deliberately skips worker crash recovery: even a denied
// import must leave running jobs, reminders, pauses, and the old session intact.
// The caller must hold the daemon's exclusive state lock and use existing state.
func OpenForHistoryImport(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if _, err = db.Exec(`PRAGMA foreign_keys=ON; PRAGMA busy_timeout=5000;`); err == nil {
		err = s.migrateHistory()
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrateHistory() error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS history_messages (
 source TEXT NOT NULL REFERENCES sources(source), guid TEXT NOT NULL,
 message_id INTEGER NOT NULL, created_at TEXT NOT NULL, sender TEXT NOT NULL,
 from_me INTEGER NOT NULL, text TEXT NOT NULL, payload TEXT NOT NULL,
 PRIMARY KEY(source,guid), UNIQUE(source,message_id)
 );
 CREATE TABLE IF NOT EXISTS history_imports (
 source TEXT PRIMARY KEY REFERENCES sources(source), reset_id INTEGER NOT NULL
 );
 CREATE TABLE IF NOT EXISTS dm_history_bootstraps (
 source TEXT PRIMARY KEY REFERENCES sources(source)
 );
 CREATE TABLE IF NOT EXISTS dm_history_pending_context (
 source TEXT PRIMARY KEY REFERENCES sources(source)
 );`)
	return err
}

// ImportHistory requires the same exclusive process lock as the daemon, with no
// active worker. It never changes inbox, cursor, jobs, replies, or reminders.
// Only a successful import containing new records clears the shared session.
// Exact repeats are no-ops, including after /new; conflicting GUIDs fail closed.
func (s *Store) ImportHistory(ctx context.Context, source Source, messages []HistoryMessage) (int, error) {
	return s.importHistory(ctx, source, messages, false)
}

// DMHistoryBootstrapped reports whether the first complete DM snapshot was
// committed, including an empty snapshot. It does not infer completion from a cursor.
func (s *Store) DMHistoryBootstrapped(ctx context.Context, source Source) (bool, error) {
	if err := source.Validate(); err != nil {
		return false, err
	}
	var complete bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM dm_history_bootstraps WHERE source=?)`, source.key()).Scan(&complete)
	return complete, err
}

// BootstrapDMHistory archives the first complete startup snapshot exactly once.
// Call after CheckHistory and before starting workers, under the daemon lock.
// Unlike a manual import it preserves sessions and queued work. Completion is
// committed with the archive, never inferred from source initialization.
func (s *Store) BootstrapDMHistory(ctx context.Context, source Source, messages []HistoryMessage) (int, error) {
	if source.Group {
		return 0, errors.New("automatic history bootstrap is for direct chats only")
	}
	if len(messages) >= MaxHistoryMessages {
		return 0, errors.New("history reached the upstream 10000-message cap; completeness cannot be established")
	}
	return s.importHistory(ctx, source, messages, true)
}

func (s *Store) importHistory(ctx context.Context, source Source, messages []HistoryMessage, bootstrap bool) (int, error) {
	if err := source.Validate(); err != nil {
		return 0, err
	}
	if (!bootstrap && len(messages) == 0) || len(messages) > MaxHistoryMessages {
		return 0, errors.New("history must contain 1–10000 complete messages")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var paused, invalid bool
	if err = tx.QueryRowContext(ctx, `SELECT paused,source_invalid FROM sources WHERE source=?`, source.key()).Scan(&paused, &invalid); errors.Is(err, sql.ErrNoRows) {
		return 0, ErrUninitialized
	}
	if err != nil {
		return 0, err
	}
	if paused || invalid {
		return 0, ErrUncertain
	}
	if bootstrap {
		var complete bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM dm_history_bootstraps WHERE source=?)`, source.key()).Scan(&complete); err != nil {
			return 0, err
		}
		if complete {
			return 0, nil
		}
	}
	var busy int
	// Queued turns and pending replies remain live work during bootstrap; only
	// manual imports require them to drain. Active/uncertain work always blocks.
	err = tx.QueryRowContext(ctx, `SELECT
 (SELECT COUNT(*) FROM jobs j WHERE source=? AND (state IN ('running','unknown') OR (? AND state='queued') OR EXISTS (SELECT 1 FROM replies r WHERE r.job_id=j.id AND r.state!='submitted' AND (? OR r.state!='pending'))))
 + (SELECT COUNT(*) FROM reminders WHERE source=? AND status IN ('dispatching','unknown'))`, source.key(), !bootstrap, !bootstrap, source.key()).Scan(&busy)
	if err != nil {
		return 0, err
	}
	if busy != 0 {
		return 0, ErrBusy
	}
	inserted := 0
	seenIDs := make(map[int64]bool, len(messages))
	seenGUIDs := make(map[string]bool, len(messages))
	for _, m := range messages {
		if bootstrap && (seenIDs[m.ID] || seenGUIDs[m.GUID]) {
			return 0, errors.New("history contains duplicate identities; completeness cannot be established")
		}
		seenIDs[m.ID], seenGUIDs[m.GUID] = true, true
		if m.ID <= 0 || m.GUID == "" || m.ChatID != source.ChatID || m.ChatGUID != source.ChatGUID || m.IsGroup != source.Group || m.CreatedAt.IsZero() || (!m.IsFromMe && !source.AllowsSender(m.Sender)) || !utf8.ValidString(m.Text) {
			return 0, errors.New("history message has invalid identity, timestamp, text, or unauthorized sender")
		}
		data, err := json.Marshal(m)
		if err != nil {
			return 0, err
		}
		if len(data) > MaxHistoryBytes {
			return 0, errors.New("full history exceeds 384 KiB archive budget; nothing imported")
		}
		var previous string
		err = tx.QueryRowContext(ctx, `SELECT payload FROM history_messages WHERE source=? AND guid=?`, source.key(), m.GUID).Scan(&previous)
		if err == nil {
			if previous != string(data) {
				return 0, fmt.Errorf("history GUID %q conflicts with archived content", m.GUID)
			}
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO history_messages(source,guid,message_id,created_at,sender,from_me,text,payload) VALUES(?,?,?,?,?,?,?,?)`, source.key(), m.GUID, m.ID, m.CreatedAt.UTC().Format(time.RFC3339Nano), m.Sender, m.IsFromMe, m.Text, string(data))
		if err != nil {
			return 0, err
		}
		inserted++
	}
	var count, bytes int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(length(CAST(payload AS BLOB))+1),0)+2 FROM history_messages WHERE source=?`, source.key()).Scan(&count, &bytes); err != nil {
		return 0, err
	}
	if count > MaxHistoryMessages || bytes > MaxHistoryBytes {
		return 0, errors.New("full history exceeds archive budget (10000 messages / 384 KiB); nothing imported")
	}
	if bootstrap {
		// Do not resurrect pre-/new history or disturb a previous manual import.
		if _, err = tx.ExecContext(ctx, `INSERT INTO history_imports(source,reset_id) VALUES(?,0) ON CONFLICT(source) DO NOTHING`, source.key()); err != nil {
			return 0, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO dm_history_bootstraps(source) VALUES(?)`, source.key()); err != nil {
			return 0, err
		}
		// Existing sessions have never seen this archive. Keep delivery pending
		// until a successful turn commits; context reads and failures cannot consume it.
		if _, err = tx.ExecContext(ctx, `INSERT INTO dm_history_pending_context(source) SELECT source FROM sources WHERE source=? AND session!=''`, source.key()); err != nil {
			return 0, err
		}
	} else if inserted > 0 {
		if _, err = tx.ExecContext(ctx, `INSERT INTO history_imports(source,reset_id) VALUES(?,COALESCE((SELECT MAX(message_id) FROM inbox WHERE source=? AND disposition='new'),0)) ON CONFLICT(source) DO UPDATE SET reset_id=excluded.reset_id`, source.key(), source.key()); err != nil {
			return 0, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE sources SET session='' WHERE source=?`, source.key()); err != nil {
			return 0, err
		}
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return inserted, nil
}

// /new advances the inbox reset marker, hiding (not deleting) earlier imports.
func archivedConversation(ctx context.Context, tx *sql.Tx, source Source) (string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT h.payload FROM history_messages h JOIN history_imports a ON a.source=h.source
 WHERE h.source=? AND a.reset_id=COALESCE((SELECT MAX(message_id) FROM inbox WHERE source=h.source AND disposition='new'),0)
 ORDER BY julianday(h.created_at),h.message_id`, source.key())
	if err != nil {
		return "", err
	}
	defer rows.Close()
	messages := []json.RawMessage{}
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return "", err
		}
		messages = append(messages, json.RawMessage(data))
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	data, err := json.Marshal(messages)
	if err != nil {
		return "", err
	}
	if len(data) > MaxHistoryBytes {
		return "", errors.New("full archived history exceeds request budget; refusing to truncate")
	}
	return string(data), nil
}
