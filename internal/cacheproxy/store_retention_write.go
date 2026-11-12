package cacheproxy

import (
	"context"
	"database/sql"
	"time"
)

// writeMapping serializes mapping snapshots and retention decisions with the
// commit and its invalidation. prepare runs under both locks before the
// transaction; false skips the transaction but still requests retention.
func (s *Store) writeMapping(ctx context.Context, prepare func() (bool, error), write func(*sql.Tx) error) error {
	s.retentionRunMu.Lock()
	defer s.retentionRunMu.Unlock()
	s.mappingMu.Lock()
	defer s.mappingMu.Unlock()

	needed := true
	if prepare != nil {
		var err error
		needed, err = prepare()
		if err != nil {
			return err
		}
	}
	var changed bool
	if needed {
		if err := s.commitRetentionWrite(ctx, func(tx *sql.Tx) error {
			// Both counters belong to this transaction's connection, excluding
			// unrelated writes on other connections. No-op UPSERTs must be guarded.
			var before, after int64
			if err := tx.QueryRowContext(ctx, "SELECT total_changes()").Scan(&before); err != nil {
				return err
			}
			if err := write(tx); err != nil {
				return err
			}
			if err := tx.QueryRowContext(ctx, "SELECT total_changes()").Scan(&after); err != nil {
				return err
			}
			changed = after != before
			return nil
		}); err != nil {
			return err
		}
	}
	if changed {
		s.mu.Lock()
		s.metadataUpdated = time.Now()
		s.mu.Unlock()
	}
	if changed || !needed {
		s.changed(false)
	}
	return nil
}

// Preferences affect retention and queue selection, but not mapping snapshots.
func (s *Store) writePreference(ctx context.Context, write func(*sql.Tx) error) error {
	s.retentionRunMu.Lock()
	defer s.retentionRunMu.Unlock()
	if err := s.commitRetentionWrite(ctx, write); err != nil {
		// Preserve failure visibility before another retention decision starts.
		return s.storageError(err)
	}
	s.changed(true)
	return nil
}

// The caller holds retentionRunMu until the committed change is invalidated.
func (s *Store) commitRetentionWrite(ctx context.Context, write func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := write(tx); err != nil {
		return err
	}
	return tx.Commit()
}
