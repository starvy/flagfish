//go:build e2e

package e2e

import (
	"net/http"
	"testing"
)

// TestAnticheatReports checks the three anti-cheat report endpoints are admin-only and return
// their documented shapes. The detectors need real evidence to surface a hit, so the assertion
// is on access control and response shape, not on a specific finding.
func TestAnticheatReports(t *testing.T) {
	// Walled off from ordinary users.
	u := mustRegister(t)
	if r := u.adminReq(t, http.MethodGet, "/anticheat/flag-sharing", nil); r.code != 403 {
		t.Errorf("non-admin flag-sharing = %d, want 403", r.code)
	}

	// flag-sharing.
	var sharing struct {
		Pairs []any `json:"pairs"`
		Total int64 `json:"total"`
	}
	admin.adminReq(t, http.MethodGet, "/anticheat/flag-sharing", nil).require(t, http.StatusOK).decode(t, &sharing)

	// ip-overlap.
	var overlap struct {
		Clusters    []any `json:"clusters"`
		MinAccounts int64 `json:"min_accounts"`
	}
	admin.adminReq(t, http.MethodGet, "/anticheat/ip-overlap?min_accounts=2", nil).require(t, http.StatusOK).decode(t, &overlap)

	// per-account overview for a real account (the admin's own).
	var acct struct {
		AccountID int64 `json:"account_id"`
		Sharing   []any `json:"sharing"`
		IPOverlap []any `json:"ip_overlap"`
	}
	admin.adminReq(t, http.MethodGet, path("/anticheat/accounts/%d", admin.userID), nil).require(t, http.StatusOK).decode(t, &acct)
	if acct.AccountID != admin.userID {
		t.Errorf("account overview id = %d, want %d", acct.AccountID, admin.userID)
	}
}
