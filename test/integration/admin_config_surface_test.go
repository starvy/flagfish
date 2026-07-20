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
	res, body := f.do(http.MethodPatch, "/api/v1/admin/config", map[string]any{
		"verify_emails": true, "view_after_ctf": true, "team_creation": false,
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
