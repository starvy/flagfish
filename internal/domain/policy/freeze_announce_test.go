package policy_test

import (
	"testing"
	"time"

	"github.com/starvy/flagfish/internal/domain/policy"
)

func TestSuppressedByFreeze(t *testing.T) {
	freeze := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)

	for _, tc := range []struct {
		name     string
		freezeAt *time.Time
		at       time.Time
		want     bool
	}{
		{"no freeze configured", nil, freeze.Add(time.Hour), false},
		{"well before freeze", &freeze, freeze.Add(-time.Hour), false},
		{"one second before freeze", &freeze, freeze.Add(-time.Second), false},
		// The horizon is strict: an event exactly at the freeze instant is already
		// hidden from the board, so it is suppressed too.
		{"exactly at freeze", &freeze, freeze, true},
		{"just after freeze", &freeze, freeze.Add(time.Second), true},
		{"well after freeze", &freeze, freeze.Add(time.Hour), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := policy.SuppressedByFreeze(tc.freezeAt, tc.at); got != tc.want {
				t.Errorf("SuppressedByFreeze(%v, %v) = %v, want %v", tc.freezeAt, tc.at, got, tc.want)
			}
		})
	}
}
