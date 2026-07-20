package config

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"maps"
	"slices"
	"testing"

	"github.com/starvy/flagfish/internal/domain/account"
)

// memStore is the smallest Store that lets a white-box test drive Set. The
// exported-API fixtures live in config_test; this one exists only because the
// rules table is not reachable from there.
type memStore struct {
	rows map[string]string
	mode account.Mode
}

func (s *memStore) All(context.Context) (map[string]string, error) {
	return maps.Clone(s.rows), nil
}

func (s *memStore) Mode(context.Context) (account.Mode, bool, error) {
	return s.mode, true, nil
}

func (s *memStore) Replace(_ context.Context, kv map[string]string) error {
	maps.Copy(s.rows, kv)
	return nil
}

// The backstop in Set cannot fire unless a rule under-declares its keys: a write
// disjoint from a rule's declared keys cannot change that rule's inputs, so a NEW
// violation of it proves the declaration wrong. Plant exactly that authoring
// mistake and assert the write is refused — as a hard error, not as operator 422.
func TestSetBackstopCatchesAnUnderDeclaredRule(t *testing.T) {
	saved := coherenceRules
	t.Cleanup(func() { coherenceRules = saved })
	coherenceRules = append(slices.Clone(coherenceRules), coherenceRule{
		keys: []string{"ctf_name"}, // wrong on purpose: the check reads paused
		check: func(s *Snapshot) error {
			if s.Paused {
				return errors.New("test rule: paused is set")
			}
			return nil
		},
	})

	ctx := context.Background()
	store := &memStore{rows: map[string]string{"setup": "true"}, mode: account.ModeUsers}
	m, err := New(ctx, store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}

	err = m.Set(ctx, map[string]string{"paused": "true"})
	if err == nil {
		t.Fatal("a violation the write created in a rule that does not declare the written key was tolerated — " +
			"the under-declared rule went unnoticed")
	}
	if errors.Is(err, ErrRejected) {
		t.Errorf("the backstop names a bug in the rules table, not operator error: got ErrRejected (%v)", err)
	}
	if store.rows["paused"] == "true" {
		t.Error("the write landed despite the backstop firing")
	}
}
