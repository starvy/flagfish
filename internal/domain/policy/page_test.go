package policy_test

import (
	"testing"

	"github.com/starvy/flagfish/internal/domain/policy"
)

// TestPageView pins the two per-page gates. A diff here is a content-visibility change and must be
// read as one; deleting either check in PageView flips a row below and fails.
func TestPageView(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		draft        bool
		authRequired bool
		p            policy.Principal
		want         policy.Outcome
	}{
		// A published, open page is the base case: anyone reads it.
		{"published open, anonymous", false, false, anon(), policy.Allow},
		{"published open, player", false, false, player(), policy.Allow},

		// draft hides existence from everyone on the public surface — even an admin, who edits it
		// through the admin surface instead. 404, not 403, so the slug is not confirmed.
		{"draft, anonymous", true, false, anon(), policy.NotFound},
		{"draft, player", true, false, player(), policy.NotFound},
		{"draft, admin on public surface", true, false, admin(), policy.NotFound},
		{"draft beats auth_required", true, true, anon(), policy.NotFound},

		// auth_required gates a published page to participants: anon is sent to log in, any authed
		// caller is let through.
		{"auth_required, anonymous", false, true, anon(), policy.AuthRequired},
		{"auth_required, player", false, true, player(), policy.Allow},
		{"auth_required, admin", false, true, admin(), policy.Allow},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := policy.PageView(tc.draft, tc.authRequired, tc.p)
			if got != tc.want {
				t.Errorf("PageView(draft=%v, auth=%v) = %+v, want %+v", tc.draft, tc.authRequired, got, tc.want)
			}
		})
	}
}
