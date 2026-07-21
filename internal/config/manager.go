package config

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
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

	// problems are the coherence violations the current snapshot is serving with —
	// swapped together with it, so the two never disagree.
	problems atomic.Pointer[[]string]
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
	// At boot an incoherent table is as fatal as an unparseable one: never start on
	// half an SMTP config. A running instance is judged more gently — see Refresh.
	if problems := m.Problems(); len(problems) > 0 {
		return nil, fmt.Errorf("config: load: invalid configuration (refusing to start):\n%s",
			strings.Join(problems, "\n"))
	}
	return m, nil
}

// Current is the read path, and it is the whole read path.
func (m *Manager) Current() *Snapshot { return m.snap.Load() }

// Problems reports the coherence violations the current snapshot carries. Empty on
// a healthy instance; non-empty only when incoherence arrived out of band and a
// write to unrelated keys was allowed through anyway. Callers must not mutate it.
func (m *Manager) Problems() []string {
	if p := m.problems.Load(); p != nil {
		return *p
	}
	return nil
}

// Repairs reports the stored values the current snapshot substituted for. It rides on
// the snapshot itself, so it is swapped with the config it describes.
func (m *Manager) Repairs() []string { return m.Current().Repairs() }

// Refresh re-reads the table and swaps the snapshot.
//
// On a parse error the old snapshot is kept: a running instance whose operator just
// wrote a broken value keeps serving the last good config and logs loudly, rather
// than falling back to defaults or serving half a config. At boot there is no old
// snapshot, so the same error is fatal — never run on a config nobody validated.
//
// A coherence violation is different: every value parsed, so the snapshot is real —
// it is served, logged loudly, and surfaced through Problems. Refusing the swap
// would pin the fleet to a stale snapshot over an incoherence Set refuses to
// create, and would brick the very API that can repair it.
func (m *Manager) Refresh(ctx context.Context) error {
	rows, err := m.store.All(ctx)
	if err != nil {
		return err
	}
	mode, err := m.instanceMode(ctx)
	if err != nil {
		return err
	}
	snap, err := buildParsed(rows, mode)
	if err != nil {
		return err
	}
	problems := []string{}
	for _, v := range snap.violations() {
		problems = append(problems, v.err.Error())
	}
	if len(problems) > 0 {
		m.log.Error("config is incoherent; serving it anyway", "problems", problems)
	}
	if repairs := snap.Repairs(); len(repairs) > 0 {
		m.log.Error("config holds values this build cannot honour; serving substitutes", "repairs", repairs)
	}
	m.snap.Store(snap)
	m.problems.Store(&problems)
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
//
// Coherence is judged rule by rule: a violated rule refuses the write only when
// it reads a key being written. A violation among untouched keys — seeded out of
// band, where boot would have refused it — is logged loudly and left standing
// rather than holding every other knob hostage behind a repair the route may not
// even be able to express.
func (m *Manager) Set(ctx context.Context, kv map[string]string) error {
	if err := refuseWithdrawn(kv); err != nil {
		return err
	}
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
	mergedSnap, err := buildParsed(merged, mode)
	if err != nil {
		// Everything already stored parsed at the last load, so the key this error
		// names is one the caller sent.
		return fmt.Errorf("%w: %w", ErrRejected, err)
	}
	if err := m.checkCoherence(mergedSnap, current, kv, mode); err != nil {
		return err
	}
	if err := m.store.Replace(ctx, kv); err != nil {
		return err
	}

	// Refresh locally rather than waiting for our own NOTIFY to come back: the
	// caller's next read must see their own write.
	return m.Refresh(ctx)
}

// refuseWithdrawn rejects a write that would store a value this build cannot honour.
// It runs before the merge because the load path repairs such a value rather than
// failing on it — without this the repair would quietly swallow the operator's choice
// and hand back a setting they did not make.
func refuseWithdrawn(kv map[string]string) error {
	var refused []error
	for _, key := range slices.Sorted(maps.Keys(kv)) {
		def, known := registry[key]
		if !known || def.refuseWrite == nil {
			continue
		}
		if err := def.refuseWrite(strings.TrimSpace(kv[key])); err != nil {
			refused = append(refused, fmt.Errorf("config key %q: %w", key, err))
		}
	}
	if len(refused) > 0 {
		return fmt.Errorf("%w: %w", ErrRejected, errors.Join(refused...))
	}
	return nil
}

// checkCoherence refuses the write for every violated rule that reads a written
// key, and tolerates — loudly — the violations the write could not have caused.
func (m *Manager) checkCoherence(merged *Snapshot, current, kv map[string]string, mode *account.Mode) error {
	violated := merged.violations()
	if len(violated) == 0 {
		return nil
	}

	// Which rules were already broken before this write. A parse failure here means
	// the table was corrupted out of band since the last load; treat it as "nothing
	// was broken before" so the backstop below refuses rather than tolerates.
	before := map[int]bool{}
	if snap, err := buildParsed(current, mode); err == nil {
		for _, v := range snap.violations() {
			before[v.rule] = true
		}
	}

	var refused []error
	for _, v := range violated {
		rule := coherenceRules[v.rule]
		if touchesAny(rule.keys, kv) {
			refused = append(refused, v.err)
			continue
		}
		// Backstop: the write is disjoint from every key this rule declares, so the
		// rule's inputs cannot have changed — a violation that is new anyway means
		// the rule under-declares its keys. That is a bug here, not operator error,
		// and it must not pass as "pre-existing".
		if !before[v.rule] {
			return fmt.Errorf("config: rule over %v newly violated by a write to %v — the rule under-declares its keys: %w",
				rule.keys, slices.Sorted(maps.Keys(kv)), v.err)
		}
		m.log.Error("config: tolerating pre-existing incoherence among keys this write does not touch",
			"keys", rule.keys, "problem", v.err.Error())
	}
	if len(refused) > 0 {
		return fmt.Errorf("%w: invalid configuration:\n%w", ErrRejected, errors.Join(refused...))
	}
	return nil
}

func touchesAny(keys []string, kv map[string]string) bool {
	for _, k := range keys {
		if _, ok := kv[k]; ok {
			return true
		}
	}
	return false
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
