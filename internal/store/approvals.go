package store

// migrateApprovals keeps the historical table so existing state databases open.
// Approval commands are no longer accepted; leftover rows stay closed.
func (s *Store) migrateApprovals() error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS approvals (
 token TEXT PRIMARY KEY, source TEXT NOT NULL REFERENCES sources(source),
 job_id INTEGER NOT NULL, sender TEXT NOT NULL,
 thread_id TEXT NOT NULL, turn_id TEXT NOT NULL, request_id TEXT NOT NULL,
 item_id TEXT NOT NULL, kind TEXT NOT NULL, fingerprint TEXT NOT NULL,
 description TEXT NOT NULL, created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL,
 cursor INTEGER NOT NULL, delivery TEXT NOT NULL CHECK(delivery IN ('dispatching','submitted','unknown')),
 state TEXT NOT NULL CHECK(state IN ('waiting','decided','consumed','closed')),
 decision TEXT NOT NULL DEFAULT 'deny' CHECK(decision IN ('deny','once','cancel')),
 response_guid TEXT NOT NULL DEFAULT '',
 UNIQUE(source,thread_id,turn_id,request_id)
);
CREATE UNIQUE INDEX IF NOT EXISTS approvals_one_waiter ON approvals(job_id) WHERE state IN ('waiting','decided');
UPDATE approvals SET state='closed',decision='deny',delivery=CASE WHEN delivery='dispatching' THEN 'unknown' ELSE delivery END WHERE state IN ('waiting','decided');`)
	return err
}
