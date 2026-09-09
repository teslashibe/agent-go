package store

import (
	"context"
	"errors"
)

// InitializeEmptyDM records an explicitly observed empty private chat. It never
// replaces existing state or invents a real message anchor. Once intake advances
// the cursor, CheckHistory requires that exact real inbox row/GUID as usual.
func (s *Store) InitializeEmptyDM(ctx context.Context, source Source) error {
	if err := source.Validate(); err != nil {
		return err
	}
	if source.Group {
		return errors.New("empty bootstrap is only supported for a private chat")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT INTO sources(source,cursor,generation) VALUES(?,0,'') ON CONFLICT(source) DO NOTHING`, source.key())
	if err != nil {
		return err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if inserted == 1 {
		if _, err = tx.ExecContext(ctx, `INSERT INTO source_anchors(source,message_id,guid) VALUES(?,0,'empty-dm')`, source.key()); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO dm_history_bootstraps(source) VALUES(?)`, source.key()); err != nil {
			return err
		}
	}
	return tx.Commit()
}
