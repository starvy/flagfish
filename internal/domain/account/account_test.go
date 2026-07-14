package account_test

import (
	"errors"
	"testing"

	"github.com/starvy/flagfish/internal/domain/account"
)

func ptr(v int64) *int64 { return &v }

func TestResolveIsTheWholeDuality(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mode    account.Mode
		mem     account.Membership
		want    account.ID
		wantErr error
	}{
		{"users mode: the account is the user", account.ModeUsers, account.Membership{UserID: 7}, 7, nil},
		{"users mode ignores a team entirely", account.ModeUsers, account.Membership{UserID: 7, TeamID: ptr(99)}, 7, nil},
		{"teams mode: the account is the team", account.ModeTeams, account.Membership{UserID: 7, TeamID: ptr(99)}, 99, nil},
		{"teams mode, no team: no account", account.ModeTeams, account.Membership{UserID: 7}, 0, account.ErrTeamless},
		{"teams mode, zero team id is no team", account.ModeTeams, account.Membership{UserID: 7, TeamID: ptr(0)}, 0, account.ErrTeamless},
	} {
		got, err := tc.mode.Resolve(tc.mem)
		if tc.wantErr != nil {
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("%s: err = %v, want %v", tc.name, err, tc.wantErr)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("%s: Resolve = %v, %v; want %v", tc.name, got, err, tc.want)
		}
	}
}

// A teamless user in teams mode must be a loud absence, not a zero value that
// quietly becomes account_id 0 and lands solves on a nonexistent account.
func TestTeamlessIsAnErrorNotAZeroValue(t *testing.T) {
	id, err := account.ModeTeams.Resolve(account.Membership{UserID: 7})
	if err == nil {
		t.Fatal("Resolve must not silently produce an account for a teamless user")
	}
	if id != 0 {
		t.Fatalf("id = %d on the error path; callers must not be able to use it", id)
	}
	if !account.ModeTeams.Teamless(account.Membership{UserID: 7}) {
		t.Error("Teamless must agree with Resolve")
	}
	// In users mode, "teamless" is not a concept.
	if account.ModeUsers.Teamless(account.Membership{UserID: 7}) {
		t.Error("nobody is teamless in users mode")
	}
}

func TestParseMode(t *testing.T) {
	if m, err := account.ParseMode("teams"); err != nil || m != account.ModeTeams {
		t.Errorf("ParseMode(teams) = %v, %v", m, err)
	}
	if m, err := account.ParseMode("users"); err != nil || m != account.ModeUsers {
		t.Errorf("ParseMode(users) = %v, %v", m, err)
	}
	for _, bad := range []string{"", "Teams", "team", "user", "solo"} {
		if _, err := account.ParseMode(bad); !errors.Is(err, account.ErrUnknownMode) {
			t.Errorf("ParseMode(%q) must fail at boot, got %v", bad, err)
		}
	}
}

// The mode is a field we trust. This is the assertion that earns the trust.
func TestAssertModeAtBoot(t *testing.T) {
	if err := account.AssertModeAtBoot(account.ModeTeams, 40); err != nil {
		t.Errorf("teams mode with teams is the normal case: %v", err)
	}
	if err := account.AssertModeAtBoot(account.ModeUsers, 0); err != nil {
		t.Errorf("users mode with no teams is the normal case: %v", err)
	}

	err := account.AssertModeAtBoot(account.ModeUsers, 1)
	if !errors.Is(err, account.ErrModeMismatch) {
		t.Fatalf("user_mode=users with a populated teams table MUST refuse to boot, got %v", err)
	}
}
