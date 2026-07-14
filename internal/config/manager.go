package config

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"sync/atomic"

	"github.com/starvy/flagfish/internal/domain/account"
)

// ErrRejected marks a write refused by validation, so a transport can tell "the operator sent a bad
// value" (a 4xx with the parse error) from "the database is down" (a 5xx with none of it).
var ErrRejected = errors.New("config: rejected")

// Store is the config table. It is an interface because this package must not
// know whether the rows arrive from Postgres, a test map, or an import fixture —
// and because the sqlc-generated code does not exist yet, which is a reason to
// define the seam, not a reason to wait.
type Store interface {
	// All reads every row.
	All(ctx context.Context) (map[string]string, error)

	// Mode reads the authoritative account model from the instance singleton — the
	// same row the gameplay path keys on, so the snapshot and the SQL cannot disagree.
	// The second result is false before setup, when there is no instance row yet and
	// the mode is not meaningful.
	Mode(ctx context.Context) (account.Mode, bool, error)

	// Replace upserts the given keys in one transaction and notifies every replica.
	// Keys absent from kv are left alone.
	//
	// The atomic bulk PATCH is this method: one transaction over the rows, one
	// NOTIFY, one swap. Atomic and cache-coherent by construction rather than as
	// a feature somebody has to engineer.
	Replace(ctx context.Context, kv map[string]string) error
}

// Watcher blocks until the config changes somewhere in the fleet. The Postgres
// implementation is LISTEN config_changed, reusing the same LISTEN/NOTIFY
// plumbing that backs SSE.
type Watcher interface {
	Watch(ctx context.Context, onChange func()) error
}

// A Manager owns the snapshot.
//
// Reads are a pointer dereference through an atomic.Pointer. The snapshot is
// swapped wholesale, never mutated, so a reader either sees the entire old config
// or the entire new one — never half of each. That matters: a scoreboard request
// that read the old freeze time and the new visibility setting would be
// inconsistent in a way no test would ever catch.
type Manager struct {
	store Store
	log   *slog.Logger
	snap  atomic.Pointer[Snapshot]
}

// New loads the table, parses it, validates it, and stores the snapshot.
//
// If the config is malformed, this returns an error and the process must not
// start. That is the entire point: a bad value fails here, loudly, at boot, with
// the key name in the message — instead of silently becoming the wrong type at some
// call site three months into an event.
func New(ctx context.Context, store Store, log *slog.Logger) (*Manager, error) {
	m := &Manager{store: store, log: log}
	if err := m.Refresh(ctx); err != nil {
		return nil, fmt.Errorf("config: load: %w", err)
	}
	return m, nil
}

// Current is the read path, and it is the whole read path.
func (m *Manager) Current() *Snapshot { return m.snap.Load() }

// Refresh re-reads the table and swaps the snapshot.
//
// On a parse error the old snapshot is kept: a running instance whose operator just
// wrote a broken value keeps serving the last good config and logs loudly, rather
// than falling back to defaults or serving half a config. At boot there is no old
// snapshot, so the same error is fatal — never run on a config nobody validated.
func (m *Manager) Refresh(ctx context.Context) error {
	rows, err := m.store.All(ctx)
	if err != nil {
		return err
	}
	mode, err := m.instanceMode(ctx)
	if err != nil {
		return err
	}
	snap, err := Build(rows, mode)
	if err != nil {
		return err
	}
	m.snap.Store(snap)
	return nil
}

// instanceMode reads the account model from the instance singleton, returning nil
// before setup, when there is no instance row yet.
func (m *Manager) instanceMode(ctx context.Context) (*account.Mode, error) {
	mode, ok, err := m.store.Mode(ctx)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return &mode, nil
}

// Set validates the result of the write before performing it, so a value that
// would not survive a boot cannot be stored in the first place. Refusing at the
// write is the same principle as failing at boot, moved one step earlier.
func (m *Manager) Set(ctx context.Context, kv map[string]string) error {
	current, err := m.store.All(ctx)
	if err != nil {
		return err
	}
	merged := maps.Clone(current)
	if merged == nil {
		merged = map[string]string{}
	}
	maps.Copy(merged, kv)

	mode, err := m.instanceMode(ctx)
	if err != nil {
		return err
	}
	if _, err := Build(merged, mode); err != nil {
		return fmt.Errorf("%w: %w", ErrRejected, err)
	}
	if err := m.store.Replace(ctx, kv); err != nil {
		return err
	}

	// Refresh locally rather than waiting for our own NOTIFY to come back: the
	// caller's next read must see their own write.
	return m.Refresh(ctx)
}

// Run watches for changes until ctx is done. It is one goroutine per process.
func (m *Manager) Run(ctx context.Context, w Watcher) error {
	return w.Watch(ctx, func() {
		if err := m.Refresh(ctx); err != nil {
			// Keep the last good snapshot and say so. A quietly-degraded config is
			// worse than a loud one, and there is nothing safe to fall back to.
			m.log.Error("config refresh failed; keeping the previous snapshot", "error", err)
			return
		}
		m.log.Info("config reloaded")
	})
}
