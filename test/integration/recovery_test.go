//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/starvy/flagfish/internal/domain/account"
)

// detailOf reads the RFC 7807 detail, which is where a Huma operation's denial names its reason.
func detailOf(t *testing.T, body []byte) string {
	t.Helper()
	var p struct {
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("decode problem: %v (%s)", err, body)
	}
	return p.Detail
}

func instanceRegistrationVis(t *testing.T, f *apiFix) string {
	t.Helper()
	res, body := f.do(http.MethodGet, "/api/v1/instance", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("instance: %d (%s)", res.StatusCode, body)
	}
	var v struct {
		RegistrationVisibility string `json:"registration_visibility"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decode instance: %v", err)
	}
	return v.RegistrationVisibility
}

// ─────────────────────────────────────────────────── registration_visibility=mlc

// `mlc` promised accounts from a MajorLeagueCyber OAuth callback that does not exist in this
// binary. An operator who picked it from the dropdown 404'd their own registration form with
// nothing able to create an account behind it — and no admin route mints users, so the event
// had no players and no way to get any. It cannot be selected.
func TestRegistrationVisibilityMLCCannotBeSelected(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers)
	cookie, csrf, _ := f.admin("root", "root@example.com")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	res, body := f.do(http.MethodPatch, "/api/v1/admin/config",
		map[string]any{"registration_visibility": "mlc"}, auth...)
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("PATCH registration_visibility=mlc: %d, want 422 (%s)", res.StatusCode, body)
	}

	// The two legal values still are legal, and still round-trip.
	for _, want := range []string{"private", "public"} {
		res, body = f.do(http.MethodPatch, "/api/v1/admin/config",
			map[string]any{"registration_visibility": want}, auth...)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("PATCH registration_visibility=%s: %d (%s)", want, res.StatusCode, body)
		}
		if got := instanceRegistrationVis(t, f); got != want {
			t.Errorf("instance reports %q, want %q", got, want)
		}
	}

	var stored string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT value FROM config WHERE key = 'registration_visibility'`).Scan(&stored); err != nil {
		t.Fatalf("read stored value: %v", err)
	}
	if stored == "mlc" {
		t.Error("the rejected value reached the config table")
	}
}

// The other half: an instance that already stored `mlc` — from an import, or from a version
// that still offered it — must boot, and must have a registration form somebody can reach.
// Refusing to start would lock the operator out of the very screen that repairs it, which is a
// worse failure than the one being fixed. The substitution is loud, not silent.
func TestStoredMLCBootsWithRegistrationReachable(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers, [2]string{"registration_visibility", "mlc"})

	if got := instanceRegistrationVis(t, f); got != "public" {
		t.Fatalf("instance reports %q, want public — registration has to be reachable", got)
	}

	// Not just reported reachable: actually reachable. This is the 404 the bug produced.
	res, body := f.do(http.MethodGet, "/api/v1/register/fields", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("registration form: %d, want 200 (%s)", res.StatusCode, body)
	}
	res, body = f.do(http.MethodPost, "/api/v1/register", map[string]any{
		"name": "player", "email": "player@example.com", "password": "correct-horse-battery",
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("register: %d, want 200 (%s)", res.StatusCode, body)
	}

	// And the operator is told, on the screen that can fix it.
	f.promoteAdmin("player@example.com")
	cookie, csrf := f.login("player@example.com", "correct-horse-battery")
	res, body = f.do(http.MethodGet, "/api/v1/admin/config", nil, withCookie(cookie), withCSRF(csrf))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("admin config: %d (%s)", res.StatusCode, body)
	}
	var cfg struct {
		Repairs                []string `json:"repairs"`
		Problems               []string `json:"problems"`
		RegistrationVisibility string   `json:"registration_visibility"`
	}
	if err := json.Unmarshal(body, &cfg); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	if len(cfg.Repairs) != 1 || !strings.Contains(cfg.Repairs[0], "registration_visibility") {
		t.Errorf("repairs = %v, want the substitution named", cfg.Repairs)
	}
	// A repair is not a coherence problem; reporting it as one would imply boot should have refused.
	if len(cfg.Problems) != 0 {
		t.Errorf("problems = %v, want empty", cfg.Problems)
	}
	if cfg.RegistrationVisibility != "public" {
		t.Errorf("config screen shows %q, want the value actually being served", cfg.RegistrationVisibility)
	}
}

// ─────────────────────────────────────────────────────────── verification repair

// verifyEmailsWithAMailer gates gameplay on verification and gives the instance a mailer to do it
// with. The mailer rows are not optional decoration: verify_emails without one is refused at load,
// because it gates every gameplay route on an email the instance could never send.
func verifyEmailsWithAMailer() [][2]string {
	return [][2]string{
		{"verify_emails", "true"},
		{"mail_server", "smtp.invalid"},
		{"mail_port", "587"},
		{"mailfrom_addr", "ctf@example.com"},
	}
}

// The disaster the coherence rules cannot catch: mail_server is set but wrong — bad password,
// blocked port, a relay that accepts and drops. Everyone registers, no mail arrives, every
// gameplay route 403s. An organizer must be able to unblock a player from the product.
func TestAdminMarkVerifiedUnblocksAPlayer(t *testing.T) {
	f := newAdminAPI(
		t, account.ModeUsers,
		[2]string{"verify_emails", "true"},
		// Configured, and wrong in a way nothing can detect from here.
		[2]string{"mail_server", "smtp.invalid"},
		[2]string{"mail_port", "587"},
		[2]string{"mailfrom_addr", "ctf@example.com"},
	)
	adminCookie, adminCSRF, adminID := f.admin("root", "root@example.com")
	auth := []func(*http.Request){withCookie(adminCookie), withCSRF(adminCSRF)}

	playerCookie, _ := f.register("player", "player@example.com", "correct-horse-battery")
	uid := f.userID("player@example.com")

	res, body := f.do(http.MethodGet, "/api/v1/challenges", nil, withCookie(playerCookie))
	if res.StatusCode != http.StatusForbidden || detailOf(t, body) != "unverified" {
		t.Fatalf("challenges before the repair: %d %s, want 403 unverified", res.StatusCode, body)
	}

	before := f.auditCount("users", "UPDATE", adminID)

	res, body = f.do(http.MethodPut, "/api/v1/admin/users/"+itoa(uid)+"/verified",
		map[string]any{"verified": true}, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("mark verified: %d (%s)", res.StatusCode, body)
	}
	var out struct {
		ID       int64  `json:"id"`
		Name     string `json:"name"`
		Verified bool   `json:"verified"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.ID != uid || out.Name != "player" || !out.Verified {
		t.Errorf("response = %+v, want the row it touched", out)
	}

	// The principal is rebuilt from the row on every request, so the same cookie plays now —
	// no re-login, no second registration.
	res, body = f.do(http.MethodGet, "/api/v1/challenges", nil, withCookie(playerCookie))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("challenges after the repair: %d, want 200 (%s)", res.StatusCode, body)
	}

	// Flipping someone's verification is exactly the kind of act that belongs in the trail.
	if after := f.auditCount("users", "UPDATE", adminID); after != before+1 {
		t.Errorf("audit rows for the actor: %d → %d, want exactly one more", before, after)
	}
	var verified bool
	if err := f.pool.QueryRow(context.Background(),
		`SELECT verified FROM users WHERE id = $1`, uid).Scan(&verified); err != nil {
		t.Fatalf("read verified: %v", err)
	}
	if !verified {
		t.Error("the flip did not reach the row")
	}

	// It is reversible, and the reversal walls them again.
	res, body = f.do(http.MethodPut, "/api/v1/admin/users/"+itoa(uid)+"/verified",
		map[string]any{"verified": false}, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("un-verify: %d (%s)", res.StatusCode, body)
	}
	res, body = f.do(http.MethodGet, "/api/v1/challenges", nil, withCookie(playerCookie))
	if res.StatusCode != http.StatusForbidden || detailOf(t, body) != "unverified" {
		t.Errorf("challenges after un-verify: %d %s, want 403 unverified", res.StatusCode, body)
	}
}

// A recovery only an admin may perform. A player who could verify themselves would make
// verify_emails mean nothing at all.
func TestMarkVerifiedIsAdminOnly(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers, verifyEmailsWithAMailer()...)
	f.admin("root", "root@example.com")

	cookie, csrf := f.register("player", "player@example.com", "correct-horse-battery")
	uid := f.userID("player@example.com")

	for _, tc := range []struct {
		name   string
		path   string
		method string
		body   any
	}{
		{"single", "/api/v1/admin/users/" + itoa(uid) + "/verified", http.MethodPut, map[string]any{"verified": true}},
		{"bulk", "/api/v1/admin/users/verify-all", http.MethodPost, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, body := f.do(tc.method, tc.path, tc.body, withCookie(cookie), withCSRF(csrf))
			if res.StatusCode != http.StatusForbidden {
				t.Fatalf("as a player: %d, want 403 (%s)", res.StatusCode, body)
			}
			var verified bool
			if err := f.pool.QueryRow(context.Background(),
				`SELECT verified FROM users WHERE id = $1`, uid).Scan(&verified); err != nil {
				t.Fatalf("read verified: %v", err)
			}
			if verified {
				t.Error("a player verified themselves")
			}
		})
	}
}

// When the mailer is what is broken it is broken for the whole field, and clicking through a
// paginated list one player at a time is not a recovery.
func TestAdminVerifyAllUnblocksTheField(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers, verifyEmailsWithAMailer()...)
	adminCookie, adminCSRF, adminID := f.admin("root", "root@example.com")
	auth := []func(*http.Request){withCookie(adminCookie), withCSRF(adminCSRF)}

	cookies := make([]string, 0, 3)
	for _, name := range []string{"one", "two", "three"} {
		cookie, _ := f.register(name, name+"@example.com", "correct-horse-battery")
		cookies = append(cookies, cookie)
	}

	before := f.auditCount("users", "UPDATE", adminID)

	res, body := f.do(http.MethodPost, "/api/v1/admin/users/verify-all", nil, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("verify-all: %d (%s)", res.StatusCode, body)
	}
	var out struct {
		Verified int64 `json:"verified"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// The admin registered unverified too, so the sweep covers four accounts.
	if out.Verified != 4 {
		t.Errorf("verified = %d, want 4", out.Verified)
	}

	for i, cookie := range cookies {
		res, body = f.do(http.MethodGet, "/api/v1/challenges", nil, withCookie(cookie))
		if res.StatusCode != http.StatusOK {
			t.Errorf("player %d after verify-all: %d (%s)", i, res.StatusCode, body)
		}
	}

	// One audit row per account moved, all stamped with the acting admin.
	if after := f.auditCount("users", "UPDATE", adminID); after != before+4 {
		t.Errorf("audit rows for the actor: %d → %d, want four more", before, after)
	}

	// Idempotent: the second run has nothing to do and says so, rather than sweeping again.
	res, body = f.do(http.MethodPost, "/api/v1/admin/users/verify-all", nil, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("second verify-all: %d (%s)", res.StatusCode, body)
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Verified != 0 {
		t.Errorf("second verified = %d, want 0", out.Verified)
	}
	if after := f.auditCount("users", "UPDATE", adminID); after != before+4 {
		t.Error("the idempotent re-run still wrote audit rows")
	}
}

// ───────────────────────────────────────────────────────── forced password change

// A forced change must not need a mailbox. The wall blocks /me — which is what the SPA shell
// loads before it renders anything — so the denial has to point at the logged-in change form,
// not at the emailed-token reset: with a broken mailer the latter is a lockout, and the shell
// answering the denial with /login would bounce the user between the two forever.
func TestForcedPasswordChangePointsAtAReachableForm(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers)
	adminCookie, adminCSRF, _ := f.admin("root", "root@example.com")
	auth := []func(*http.Request){withCookie(adminCookie), withCSRF(adminCSRF)}

	f.register("victim", "victim@example.com", "correct-horse-battery")
	uid := f.userID("victim@example.com")

	res, body := f.do(http.MethodPut, "/api/v1/admin/users/"+itoa(uid)+"/force-password-change", nil, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("force: %d (%s)", res.StatusCode, body)
	}

	cookie, csrf := f.login("victim@example.com", "correct-horse-battery")
	res, body = f.do(http.MethodGet, "/api/v1/me", nil, withCookie(cookie))
	if res.StatusCode != http.StatusForbidden || detailOf(t, body) != "password-change-required" {
		t.Fatalf("/me while forced: %d %s, want 403 password-change-required", res.StatusCode, body)
	}
	if loc := res.Header.Get("Location"); loc != "/change-password" {
		t.Errorf("Location = %q, want /change-password — the token reset needs a mailbox", loc)
	}

	// And that destination's endpoint really is the exit: no token, no mail, just the
	// credential they signed in with a moment ago.
	res, body = f.do(http.MethodPost, "/api/v1/me/password", map[string]any{
		"current_password": "correct-horse-battery", "new_password": "a whole new passphrase",
	}, withCookie(cookie), withCSRF(csrf))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("change while forced: %d, want 200 (%s)", res.StatusCode, body)
	}

	fresh := sessionCookie(res)
	if fresh == "" {
		t.Fatal("no session minted by the change")
	}
	res, body = f.do(http.MethodGet, "/api/v1/me", nil, withCookie(fresh))
	if res.StatusCode != http.StatusOK {
		t.Errorf("/me after the change: %d, want 200 (%s)", res.StatusCode, body)
	}
}
