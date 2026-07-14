package config

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/starvy/flagfish/internal/audit"
	"github.com/starvy/flagfish/internal/domain/account"
)

// configChannel is the LISTEN/NOTIFY channel. One channel, one payload-free
// signal: "re-read the table". We deliberately do not ship the changed values in
// the payload — an 8000-byte NOTIFY limit is not a place to discover you have
// outgrown your design, and re-reading 100 rows is free.
const configChannel = "config_changed"

// NotifyChanged fires the config-changed signal on tx, so every replica re-reads the table when it
// commits. The console bootstrap writes config.setup outside this package — in the same transaction
// as the first admin — and it must still wake a server that is already running, or that server keeps
// serving a snapshot in which setup was never done.
func NotifyChanged(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `SELECT pg_notify($1, '')`, configChannel); err != nil {
		return fmt.Errorf("config: notify: %w", err)
	}
	return nil
}

// PGStore is the Postgres-backed config table.
//
// It writes raw SQL rather than going through sqlc, and that is deliberate: this
// package owns the config table, and the two statements below are the entire surface.
// Keeping them here is what makes "one typed door" true rather than aspirational.
type PGStore struct {
	pool *pgxpool.Pool
}

func NewPGStore(pool *pgxpool.Pool) *PGStore { return &PGStore{pool: pool} }

func (s *PGStore) All(ctx context.Context) (map[string]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT key, value FROM config`)
	if err != nil {
		return nil, fmt.Errorf("config: select: %w", err)
	}
	defer rows.Close()

	out := map[string]string{}
	for rows.Next() {
		var k string
		var v *string // a NULL value is a set-but-empty key, not a missing one
		if err := rows.Scan(&k, &v); err != nil {
			return nil, fmt.Errorf("config: scan: %w", err)
		}
		if v == nil {
			out[k] = ""
			continue
		}
		out[k] = *v
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("config: select: %w", err)
	}
	return out, nil
}

// Mode reads the account model from the instance singleton. This is the same row the
// gameplay SQL keys on, so sourcing Snapshot.Mode here is what keeps the Go side and
// the hot-path SQL answering to one fact. No rows means the instance is not set up
// yet: the mode is not meaningful, and the caller falls back to the default rather
// than inventing one.
func (s *PGStore) Mode(ctx context.Context) (account.Mode, bool, error) {
	var raw string
	err := s.pool.QueryRow(ctx, `SELECT user_mode FROM instance`).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("config: read instance user_mode: %w", err)
	}
	m, err := account.ParseMode(raw)
	if err != nil {
		return 0, false, fmt.Errorf("config: instance user_mode: %w", err)
	}
	return m, true, nil
}

// Replace is the atomic bulk write: every key lands or none does, and exactly one
// NOTIFY fires — inside the transaction, so a rolled-back write never wakes the
// fleet up to re-read a change that did not happen.
//
// UNIQUE(key) is what makes the upsert possible: without it duplicate keys are
// storable and reads turn nondeterministic, so the schema enforces it.
func (s *PGStore) Replace(ctx context.Context, kv map[string]string) error {
	if len(kv) == 0 {
		return nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("config: begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit

	// The audit trigger on config attributes these rows to whoever the context says is acting.
	// Absent an actor the rows still land — as system writes — because a failed audit stamp must
	// never be the reason a config write is lost, but a silent one must never be the default either.
	if actor, ok := audit.ActorFrom(ctx); ok {
		if err := audit.Stamp(ctx, tx, actor); err != nil {
			return fmt.Errorf("config: %w", err)
		}
	}

	batch := &pgx.Batch{}
	for k, v := range kv {
		batch.Queue(
			`INSERT INTO config (key, value) VALUES ($1, $2)
			 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`,
			k, v,
		)
	}
	if err := tx.SendBatch(ctx, batch).Close(); err != nil {
		return fmt.Errorf("config: upsert: %w", err)
	}
	if err := NotifyChanged(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("config: commit: %w", err)
	}
	return nil
}

// PGWatcher turns NOTIFY config_changed into a callback. It holds a DEDICATED
// connection outside the pool, because a connection that is blocked in
// WaitForNotification is not available for anything else and must not be handed
// back to a query.
type PGWatcher struct {
	pool *pgxpool.Pool
}

func NewPGWatcher(pool *pgxpool.Pool) *PGWatcher { return &PGWatcher{pool: pool} }

func (w *PGWatcher) Watch(ctx context.Context, onChange func()) error {
	conn, err := w.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("config: acquire listener: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "LISTEN "+configChannel); err != nil {
		return fmt.Errorf("config: listen: %w", err)
	}

	for {
		if _, err := conn.Conn().WaitForNotification(ctx); err != nil {
			// Wrapped, not naked: this includes ctx cancellation, and the caller
			// decides whether to retry by unwrapping with errors.Is.
			return fmt.Errorf("config: wait for notification: %w", err)
		}
		onChange()
	}
}
