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

// configBody is the admin config round-trip surface these tests read back.
type configBody struct {
	Name            string   `json:"name"`
	Paused          bool     `json:"paused"`
	VerifyEmails    bool     `json:"verify_emails"`
	ViewAfterCTF    bool     `json:"view_after_ctf"`
	TeamCreation    bool     `json:"team_creation"`
	NumUsers        int      `json:"num_users"`
	NumTeams        int      `json:"num_teams"`
	TeamSize        int      `json:"team_size"`
	MailPort        int      `json:"mail_port"`
	MailTLS         bool     `json:"mail_tls"`
	MailFrom        string   `json:"mailfrom_addr"`
	MailServerSet   bool     `json:"mail_server_set"`
	MailUsernameSet bool     `json:"mail_username_set"`
	MailPasswordSet bool     `json:"mail_password_set"`
	WebhookEnabled  bool     `json:"webhook_enabled"`
	WebhookEvents   []string `json:"webhook_events"`
	WebhookURLSet   bool     `json:"webhook_url_set"`
	Problems        []string `json:"problems"`
}

func decodeConfig(t *testing.T, body []byte) configBody {
	t.Helper()
	var out configBody
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode config: %v (%s)", err, body)
	}
	return out
}

// configAuditRows counts audit rows the config triggers stamped for the actor.
func (f *apiFix) configAuditRows(actorID int64) int {
	f.t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_log WHERE target_table = 'config' AND actor_id = $1`, actorID).Scan(&n); err != nil {
		f.t.Fatalf("config audit count: %v", err)
	}
	return n
}

func TestAdminConfigGameTogglesRoundTrip(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers)
	cookie, csrf, adminID := f.admin("root", "root@example.com")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	before := f.configAuditRows(adminID)
	// The mailer rides along: verify_emails without one is refused, so the write that
	// turns verification on is the write that gives it something to send with.
	res, body := f.do(http.MethodPatch, "/api/v1/admin/config", map[string]any{
		"verify_emails": true, "view_after_ctf": true, "team_creation": false,
		"mail_server": "smtp.ctf.test", "mail_port": 587, "mailfrom_addr": "noreply@ctf.test",
	}, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("patch toggles: got %d, want 200 (%s)", res.StatusCode, body)
	}
	got := decodeConfig(t, body)
	if !got.VerifyEmails || !got.ViewAfterCTF || got.TeamCreation {
		t.Errorf("toggles did not round-trip: %+v", got)
	}
	if f.configAuditRows(adminID) <= before {
		t.Error("no audit row for the toggle write")
	}

	// GET agrees with the echo.
	res, body = f.do(http.MethodGet, "/api/v1/admin/config", nil, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("get config: got %d (%s)", res.StatusCode, body)
	}
	if got = decodeConfig(t, body); !got.VerifyEmails || got.TeamCreation {
		t.Errorf("GET disagrees with the write: %+v", got)
	}
}

func TestAdminConfigLimitsRoundTrip(t *testing.T) {
	f := newAdminAPI(t, account.ModeTeams)
	cookie, csrf, adminID := f.admin("root", "root@example.com")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	before := f.configAuditRows(adminID)
	res, body := f.do(http.MethodPatch, "/api/v1/admin/config",
		map[string]any{"num_users": 500, "num_teams": 100, "team_size": 4}, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("patch limits: got %d, want 200 (%s)", res.StatusCode, body)
	}
	got := decodeConfig(t, body)
	if got.NumUsers != 500 || got.NumTeams != 100 || got.TeamSize != 4 {
		t.Errorf("limits did not round-trip: %+v", got)
	}
	if f.configAuditRows(adminID) <= before {
		t.Error("no audit row for the limits write")
	}

	// Back to unlimited: zero is a value, not an omission.
	res, body = f.do(http.MethodPatch, "/api/v1/admin/config", map[string]any{"num_users": 0}, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("reset num_users: got %d (%s)", res.StatusCode, body)
	}
	if got = decodeConfig(t, body); got.NumUsers != 0 {
		t.Errorf("num_users = %d after reset, want 0", got.NumUsers)
	}

	// A negative cap dies in schema validation, before any config machinery runs.
	res, body = f.do(http.MethodPatch, "/api/v1/admin/config", map[string]any{"num_users": -1}, auth...)
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("num_users -1: got %d, want 422 (%s)", res.StatusCode, body)
	}
}

func TestAdminConfigTeamSizeRefusedInUsersMode(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers)
	cookie, csrf, _ := f.admin("root", "root@example.com")

	res, body := f.do(http.MethodPatch, "/api/v1/admin/config",
		map[string]any{"team_size": 4}, withCookie(cookie), withCSRF(csrf))
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("team_size in users mode: got %d, want 422 (%s)", res.StatusCode, body)
	}
}

func TestAdminConfigMailRoundTripAndSecrets(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers)
	cookie, csrf, adminID := f.admin("root", "root@example.com")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	const (
		secretServer   = "smtp.secret-host.example"
		secretUser     = "mailer-user-7"
		secretPassword = "hunter2-secret-value"
	)

	res, body := f.do(http.MethodGet, "/api/v1/admin/config", nil, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("get: got %d (%s)", res.StatusCode, body)
	}
	if got := decodeConfig(t, body); got.MailServerSet || got.MailUsernameSet || got.MailPasswordSet {
		t.Fatalf("presence booleans true on a fresh instance: %+v", got)
	}

	before := f.configAuditRows(adminID)
	res, body = f.do(http.MethodPatch, "/api/v1/admin/config", map[string]any{
		"mail_server":   secretServer,
		"mail_username": secretUser,
		"mail_password": secretPassword,
		"mail_port":     2525,
		"mail_tls":      true,
		"mailfrom_addr": "CTF <noreply@ctf.example>",
	}, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("patch mail: got %d, want 200 (%s)", res.StatusCode, body)
	}
	got := decodeConfig(t, body)
	if got.MailPort != 2525 || !got.MailTLS || got.MailFrom != "CTF <noreply@ctf.example>" {
		t.Errorf("public mail fields did not round-trip: %+v", got)
	}
	if !got.MailServerSet || !got.MailUsernameSet || !got.MailPasswordSet {
		t.Errorf("presence booleans did not flip on set: %+v", got)
	}
	if f.configAuditRows(adminID) <= before {
		t.Error("no audit row for the mail write")
	}

	// The values themselves must never come back — not as fields, not embedded in
	// anything. The raw body is the only honest place to check.
	for _, secret := range []string{secretServer, secretUser, secretPassword} {
		if strings.Contains(string(body), secret) {
			t.Errorf("the PATCH echo carries the secret %q:\n%s", secret, body)
		}
	}
	res, body = f.do(http.MethodGet, "/api/v1/admin/config", nil, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("get after set: got %d (%s)", res.StatusCode, body)
	}
	for _, secret := range []string{secretServer, secretUser, secretPassword} {
		if strings.Contains(string(body), secret) {
			t.Errorf("GET /admin/config carries the secret %q:\n%s", secret, body)
		}
	}

	// "" clears: the password's presence boolean drops, the username's stays.
	res, body = f.do(http.MethodPatch, "/api/v1/admin/config", map[string]any{"mail_password": ""}, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("clear password: got %d (%s)", res.StatusCode, body)
	}
	got = decodeConfig(t, body)
	if got.MailPasswordSet {
		t.Error("mail_password_set still true after the clear")
	}
	if !got.MailUsernameSet || !got.MailServerSet {
		t.Errorf("clearing the password disturbed its neighbours: %+v", got)
	}
}

func TestAdminConfigWebhookRoundTripAndSecrets(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers)
	cookie, csrf, _ := f.admin("root", "root@example.com")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	const secretURL = "https://discord.example/api/webhooks/1234/tok-3ce9f1-secret"

	res, body := f.do(http.MethodPatch, "/api/v1/admin/config", map[string]any{
		"webhook_url":     secretURL,
		"webhook_enabled": true,
		"webhook_events":  []string{"first_blood", "solve"},
	}, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("patch webhook: got %d, want 200 (%s)", res.StatusCode, body)
	}
	got := decodeConfig(t, body)
	if !got.WebhookEnabled || !got.WebhookURLSet {
		t.Errorf("webhook fields did not round-trip: %+v", got)
	}
	if len(got.WebhookEvents) != 2 || got.WebhookEvents[0] != "first_blood" || got.WebhookEvents[1] != "solve" {
		t.Errorf("webhook_events = %v, want [first_blood solve]", got.WebhookEvents)
	}
	if strings.Contains(string(body), secretURL) {
		t.Errorf("the webhook URL is a credential and came back anyway:\n%s", body)
	}

	// An unknown event name dies at schema validation.
	res, body = f.do(http.MethodPatch, "/api/v1/admin/config",
		map[string]any{"webhook_events": []string{"bogus"}}, auth...)
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("bogus webhook event: got %d, want 422 (%s)", res.StatusCode, body)
	}

	// Clearing the URL requires disabling the feed in the same write — and then the
	// presence boolean drops.
	res, body = f.do(http.MethodPatch, "/api/v1/admin/config",
		map[string]any{"webhook_enabled": false, "webhook_url": ""}, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("clear webhook: got %d (%s)", res.StatusCode, body)
	}
	if got = decodeConfig(t, body); got.WebhookURLSet || got.WebhookEnabled {
		t.Errorf("webhook clear did not land: %+v", got)
	}
}

func TestAdminConfigCoherenceRefusals(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers)
	cookie, csrf, _ := f.admin("root", "root@example.com")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	cases := []struct {
		name   string
		patch  map[string]any
		wantIn string
	}{
		{"mail_server without mailfrom_addr", map[string]any{"mail_server": "smtp.example.com", "mail_port": 587}, "mailfrom_addr"},
		{"webhook_enabled without webhook_url", map[string]any{"webhook_enabled": true}, "webhook_url"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, body := f.do(http.MethodPatch, "/api/v1/admin/config", tc.patch, auth...)
			if res.StatusCode != http.StatusUnprocessableEntity {
				t.Fatalf("got %d, want 422 (%s)", res.StatusCode, body)
			}
			if !strings.Contains(string(body), tc.wantIn) {
				t.Errorf("the refusal does not name %q — the operator cannot act on it: %s", tc.wantIn, body)
			}
		})
	}
}

// The footgun this phase removes: config rows seeded out of band with half an
// SMTP config used to make EVERY PATCH — even a rename — come back 422, with no
// route able to supply the missing key. Now the unrelated write lands, the
// incoherence is surfaced as problems, and the mail group can be repaired
// through the same API.
func TestAdminConfigSeededIncoherenceDoesNotBrickTheAPI(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers)
	cookie, csrf, _ := f.admin("root", "root@example.com")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	// Out of band, after boot: the state a broken import or a stray UPDATE leaves.
	if _, err := f.pool.Exec(context.Background(),
		`INSERT INTO config (key, value) VALUES ('mail_server', 'smtp.seeded.example')`); err != nil {
		t.Fatalf("seed incoherent mail: %v", err)
	}

	res, body := f.do(http.MethodPatch, "/api/v1/admin/config", map[string]any{"name": "still alive"}, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("a rename was refused over mail incoherence it did not touch: got %d (%s)", res.StatusCode, body)
	}
	got := decodeConfig(t, body)
	if got.Name != "still alive" {
		t.Errorf("name = %q, the write did not land", got.Name)
	}
	if len(got.Problems) == 0 {
		t.Fatal("problems is empty: the tolerated incoherence is invisible to the operator who can repair it")
	}
	if !strings.Contains(strings.Join(got.Problems, "\n"), "mailfrom_addr") {
		t.Errorf("problems does not name the missing key: %q", got.Problems)
	}

	// And the same API repairs the group.
	res, body = f.do(http.MethodPatch, "/api/v1/admin/config",
		map[string]any{"mailfrom_addr": "noreply@ctf.example", "mail_port": 587}, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("repair: got %d, want 200 (%s)", res.StatusCode, body)
	}
	got = decodeConfig(t, body)
	if len(got.Problems) != 0 {
		t.Errorf("problems = %q after the repair, want none", got.Problems)
	}
	if !got.MailServerSet {
		t.Error("mail_server_set = false — the seeded server vanished during the repair")
	}
}

// Pausing is incident response, and it must actually stop the game: the toggle
// lands through the config PATCH, the policy layer refuses submissions for
// everyone including the admin who paused it, and the public instance endpoint
// tells every client why the flag input just went dark.
func TestAdminConfigPausedEndToEnd(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers)
	cookie, csrf, _ := f.admin("root", "root@example.com")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	chID := f.seedChallenge("pausable", "misc", 100)
	f.seedFlag(chID, "flag{paused}")

	res, body := f.do(http.MethodPatch, "/api/v1/admin/config", map[string]any{"paused": true}, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("pause: got %d, want 200 (%s)", res.StatusCode, body)
	}
	if got := decodeConfig(t, body); !got.Paused {
		t.Fatalf("paused did not echo true: %+v", got)
	}

	// The admin is paused with everybody else — there is no exemption on submit.
	res, body = f.do(http.MethodPost, "/api/v1/challenges/"+itoa(chID)+"/attempt",
		map[string]any{"flag": "flag{paused}"}, auth...)
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("attempt while paused: got %d, want 403 (%s)", res.StatusCode, body)
	}
	if !strings.Contains(string(body), "paused") {
		t.Errorf("the denial does not say why: %s", body)
	}

	// The pause is public knowledge: every client's banner reads it from /instance.
	res, body = f.do(http.MethodGet, "/api/v1/instance", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("instance: got %d (%s)", res.StatusCode, body)
	}
	var inst struct {
		Paused bool `json:"paused"`
	}
	if err := json.Unmarshal(body, &inst); err != nil {
		t.Fatalf("decode instance: %v (%s)", err, body)
	}
	if !inst.Paused {
		t.Error("GET /instance does not disclose the pause")
	}

	// Resume, and the game is back.
	res, body = f.do(http.MethodPatch, "/api/v1/admin/config", map[string]any{"paused": false}, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("resume: got %d (%s)", res.StatusCode, body)
	}
	res, body = f.do(http.MethodPost, "/api/v1/challenges/"+itoa(chID)+"/attempt",
		map[string]any{"flag": "flag{paused}"}, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("attempt after resume: got %d, want 200 (%s)", res.StatusCode, body)
	}
}
