//go:build integration

package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/starvy/flagfish/internal/domain/account"
)

type reqsChallenge struct {
	ID           int64 `json:"id"`
	Requirements struct {
		Prerequisites []int64 `json:"prerequisites"`
		Visibility    string  `json:"visibility"`
	} `json:"requirements"`
}

type setReqsResult struct {
	Challenge reqsChallenge `json:"challenge"`
	Warnings  []string      `json:"warnings"`
}

func decodeSetReqs(t *testing.T, body []byte) setReqsResult {
	t.Helper()
	var v setReqsResult
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decode requirements result: %v (%s)", err, body)
	}
	return v
}

// adminChallenge creates a visible challenge through the API and returns its id.
func (f *apiFix) adminChallenge(name string, value int, auth ...func(*http.Request)) int64 {
	f.t.Helper()
	res, body := f.do(http.MethodPost, "/api/v1/admin/challenges",
		map[string]any{"name": name, "category": "misc", "value": value}, auth...)
	if res.StatusCode != http.StatusCreated {
		f.t.Fatalf("create challenge %s: got %d (%s)", name, res.StatusCode, body)
	}
	return decodeID(f.t, body)
}

func (f *apiFix) adminFlag(chID int64, flag string, auth ...func(*http.Request)) {
	f.t.Helper()
	res, body := f.do(http.MethodPost, fmt.Sprintf("/api/v1/admin/challenges/%d/flags", chID),
		map[string]any{"type": "static", "content": flag}, auth...)
	if res.StatusCode != http.StatusCreated {
		f.t.Fatalf("add flag to %d: got %d (%s)", chID, res.StatusCode, body)
	}
}

type boardItem struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Locked bool   `json:"locked"`
}

// playerBoard lists the challenges as the given player sees them, keyed by id.
func (f *apiFix) playerBoard(cookie string) map[int64]boardItem {
	f.t.Helper()
	res, body := f.do(http.MethodGet, "/api/v1/challenges", nil, withCookie(cookie))
	if res.StatusCode != http.StatusOK {
		f.t.Fatalf("player board: got %d (%s)", res.StatusCode, body)
	}
	var out struct {
		Challenges []boardItem `json:"challenges"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		f.t.Fatalf("decode board: %v (%s)", err, body)
	}
	byID := make(map[int64]boardItem, len(out.Challenges))
	for _, c := range out.Challenges {
		byID[c.ID] = c
	}
	return byID
}

// TestAdminSetRequirementsDrivesPlayerVisibility: the requirements PUT is the native way to gate a
// challenge, and each visibility renders on the player board exactly as the imported column does —
// hidden is absent, masked is ???, preview keeps the name; solving the prerequisite unlocks.
func TestAdminSetRequirementsDrivesPlayerVisibility(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers)
	cookie, csrf, _ := f.admin("root", "root@example.com")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	gate := f.adminChallenge("Gate", 100, auth...)
	f.adminFlag(gate, "flag{gate}", auth...)
	boss := f.adminChallenge("Boss", 200, auth...)
	f.adminFlag(boss, "flag{boss}", auth...)

	playerCookie, playerCSRF := f.register("Ada", "ada@ctf.test", "correct horse battery")

	put := func(visibility string) setReqsResult {
		res, body := f.do(http.MethodPut, fmt.Sprintf("/api/v1/admin/challenges/%d/requirements", boss),
			map[string]any{"prerequisites": []int64{gate}, "visibility": visibility}, auth...)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("set requirements (%s): got %d (%s)", visibility, res.StatusCode, body)
		}
		return decodeSetReqs(t, body)
	}

	// hidden: the locked challenge is off the player's board entirely.
	out := put("hidden")
	if got := out.Challenge.Requirements; len(got.Prerequisites) != 1 || got.Prerequisites[0] != gate || got.Visibility != "hidden" {
		t.Fatalf("echo = %+v, want [%d]/hidden", got, gate)
	}
	if len(out.Warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", out.Warnings)
	}
	if _, ok := f.playerBoard(playerCookie)[boss]; ok {
		t.Error("hidden-locked challenge is on the player board")
	}

	// masked: on the board, locked, name withheld.
	put("masked")
	if item, ok := f.playerBoard(playerCookie)[boss]; !ok {
		t.Error("masked-locked challenge is missing from the board")
	} else if !item.Locked || item.Name != "???" {
		t.Errorf("masked row = %+v, want locked ???", item)
	}

	// preview: on the board, locked, real name shown.
	put("preview")
	if item, ok := f.playerBoard(playerCookie)[boss]; !ok {
		t.Error("preview-locked challenge is missing from the board")
	} else if !item.Locked || item.Name != "Boss" {
		t.Errorf("preview row = %+v, want locked Boss", item)
	}

	// Solving the prerequisite unlocks it, real name and all.
	res, body := f.do(http.MethodPost, f.attemptPath(gate),
		map[string]any{"flag": "flag{gate}"}, withCookie(playerCookie), withCSRF(playerCSRF))
	if res.StatusCode != http.StatusOK || decodeAttempt(t, body).Status != "correct" {
		t.Fatalf("solve gate: got %d (%s)", res.StatusCode, body)
	}
	if item, ok := f.playerBoard(playerCookie)[boss]; !ok {
		t.Error("unlocked challenge is missing from the board")
	} else if item.Locked || item.Name != "Boss" {
		t.Errorf("unlocked row = %+v, want unlocked Boss", item)
	}
	res, body = f.do(http.MethodPost, f.attemptPath(boss),
		map[string]any{"flag": "flag{boss}"}, withCookie(playerCookie), withCSRF(playerCSRF))
	if res.StatusCode != http.StatusOK || decodeAttempt(t, body).Status != "correct" {
		t.Fatalf("solve boss after unlock: got %d (%s)", res.StatusCode, body)
	}
}

// TestAdminSetRequirementsValidation: dangling ids and self-reference are 422s — stricter than the
// importer, which only guarantees shape — and an empty PUT clears. Duplicates collapse silently.
func TestAdminSetRequirementsValidation(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers)
	cookie, csrf, adminID := f.admin("root", "root@example.com")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	gate := f.adminChallenge("Gate", 100, auth...)
	boss := f.adminChallenge("Boss", 200, auth...)

	// A prerequisite that does not exist is refused, naming the id.
	res, body := f.do(http.MethodPut, fmt.Sprintf("/api/v1/admin/challenges/%d/requirements", boss),
		map[string]any{"prerequisites": []int64{gate, 999999}}, auth...)
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("dangling id: got %d, want 422 (%s)", res.StatusCode, body)
	}
	if !strings.Contains(string(body), "999999") {
		t.Errorf("the refusal does not name the missing id: %s", body)
	}

	// Self-reference is refused.
	res, body = f.do(http.MethodPut, fmt.Sprintf("/api/v1/admin/challenges/%d/requirements", boss),
		map[string]any{"prerequisites": []int64{boss}}, auth...)
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("self-reference: got %d, want 422 (%s)", res.StatusCode, body)
	}

	// A missing challenge is a 404, not a silent no-op.
	res, body = f.do(http.MethodPut, "/api/v1/admin/challenges/999999/requirements",
		map[string]any{"prerequisites": []int64{gate}}, auth...)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("missing challenge: got %d, want 404 (%s)", res.StatusCode, body)
	}

	// Duplicates collapse rather than storing a double edge.
	res, body = f.do(http.MethodPut, fmt.Sprintf("/api/v1/admin/challenges/%d/requirements", boss),
		map[string]any{"prerequisites": []int64{gate, gate}, "visibility": "preview"}, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("dedupe put: got %d (%s)", res.StatusCode, body)
	}
	if got := decodeSetReqs(t, body).Challenge.Requirements.Prerequisites; len(got) != 1 || got[0] != gate {
		t.Fatalf("deduped echo = %v, want [%d]", got, gate)
	}

	// An empty list clears the gate.
	res, body = f.do(http.MethodPut, fmt.Sprintf("/api/v1/admin/challenges/%d/requirements", boss),
		map[string]any{"prerequisites": []int64{}}, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("clear put: got %d (%s)", res.StatusCode, body)
	}
	out := decodeSetReqs(t, body)
	if len(out.Challenge.Requirements.Prerequisites) != 0 || out.Challenge.Requirements.Visibility != "hidden" {
		t.Fatalf("cleared echo = %+v, want empty/hidden", out.Challenge.Requirements)
	}

	// Every accepted write above stamped an audit row against the admin.
	if got := f.auditCount("challenges", "UPDATE", adminID); got == 0 {
		t.Error("no UPDATE audit row for the requirements writes")
	}
}

// TestAdminSetRequirementsCycleWarns: a cycle is stored — the importer admits them and the runtime
// gate never traverses — but the response names it, because a cycle can only be broken by an admin.
func TestAdminSetRequirementsCycleWarns(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers)
	cookie, csrf, _ := f.admin("root", "root@example.com")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	a := f.adminChallenge("A", 100, auth...)
	b := f.adminChallenge("B", 100, auth...)

	res, body := f.do(http.MethodPut, fmt.Sprintf("/api/v1/admin/challenges/%d/requirements", a),
		map[string]any{"prerequisites": []int64{b}}, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("first edge: got %d (%s)", res.StatusCode, body)
	}
	if warns := decodeSetReqs(t, body).Warnings; len(warns) != 0 {
		t.Fatalf("first edge warned: %v", warns)
	}

	res, body = f.do(http.MethodPut, fmt.Sprintf("/api/v1/admin/challenges/%d/requirements", b),
		map[string]any{"prerequisites": []int64{a}}, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("closing edge: got %d, want 200 — cycles are tolerated with a warning (%s)", res.StatusCode, body)
	}
	out := decodeSetReqs(t, body)
	if len(out.Warnings) != 1 || !strings.Contains(out.Warnings[0], "cycle") {
		t.Fatalf("warnings = %v, want one naming the cycle", out.Warnings)
	}
	for _, id := range []int64{a, b} {
		if !strings.Contains(out.Warnings[0], fmt.Sprintf("%d", id)) {
			t.Errorf("warning %q does not name challenge %d", out.Warnings[0], id)
		}
	}
}
