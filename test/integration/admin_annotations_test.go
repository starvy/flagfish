//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/starvy/flagfish/internal/domain/account"
)

// playerAnnotations reads the annotation map off the player detail view.
func (f *apiFix) playerAnnotations(chID int64, cookie string) map[string]string {
	f.t.Helper()
	res, body := f.do(http.MethodGet, fmt.Sprintf("/api/v1/challenges/%d", chID), nil, withCookie(cookie))
	if res.StatusCode != http.StatusOK {
		f.t.Fatalf("player detail: got %d (%s)", res.StatusCode, body)
	}
	var out struct {
		Annotations map[string]string `json:"annotations"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		f.t.Fatalf("decode detail annotations: %v (%s)", err, body)
	}
	return out.Annotations
}

// playerListAnnotations reads one challenge's annotations off the board listing.
func (f *apiFix) playerListAnnotations(chID int64, cookie string) (map[string]string, bool) {
	f.t.Helper()
	res, body := f.do(http.MethodGet, "/api/v1/challenges", nil, withCookie(cookie))
	if res.StatusCode != http.StatusOK {
		f.t.Fatalf("player list: got %d (%s)", res.StatusCode, body)
	}
	var out struct {
		Challenges []struct {
			ID          int64             `json:"id"`
			Locked      bool              `json:"locked"`
			Annotations map[string]string `json:"annotations"`
		} `json:"challenges"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		f.t.Fatalf("decode list: %v (%s)", err, body)
	}
	for _, c := range out.Challenges {
		if c.ID == chID {
			return c.Annotations, true
		}
	}
	return nil, false
}

func (f *apiFix) annotationPath(chID int64, key string) string {
	return fmt.Sprintf("/api/v1/admin/challenges/%d/annotations/%s", chID, key)
}

// The write path is an upsert, so setting a key twice is a success and not a conflict — that is the
// difference from a tag, and it is the property the constraint exists to provide.
func TestAdminAnnotationSetAndRemove(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers)
	cookie, csrf, adminID := f.admin("root", "root@example.com")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	chID := f.adminChallenge("Annotated", 100, auth...)
	playerCookie, _ := f.register("Ada", "ada@ctf.test", "correct horse battery")

	res, body := f.do(http.MethodPut, f.annotationPath(chID, "country"), map[string]any{"value": "CZ"}, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("set: got %d, want 200 (%s)", res.StatusCode, body)
	}
	var echoed struct {
		ChallengeID int64  `json:"challenge_id"`
		Key         string `json:"key"`
		Value       string `json:"value"`
	}
	if err := json.Unmarshal(body, &echoed); err != nil {
		t.Fatalf("decode echo: %v (%s)", err, body)
	}
	if echoed.ChallengeID != chID || echoed.Key != "country" || echoed.Value != "CZ" {
		t.Fatalf("echo = %+v, want challenge %d / country / CZ", echoed, chID)
	}

	if got := f.playerAnnotations(chID, playerCookie); got["country"] != "CZ" {
		t.Fatalf("player detail annotations = %v, want country=CZ", got)
	}
	if got, ok := f.playerListAnnotations(chID, playerCookie); !ok || got["country"] != "CZ" {
		t.Fatalf("player list annotations = %v (found=%v), want country=CZ", got, ok)
	}

	// Upsert: the same key again replaces the value rather than conflicting.
	res, body = f.do(http.MethodPut, f.annotationPath(chID, "country"), map[string]any{"value": "SK"}, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("re-set: got %d, want 200 — setting a key is idempotent (%s)", res.StatusCode, body)
	}
	if got := f.playerAnnotations(chID, playerCookie); got["country"] != "SK" {
		t.Fatalf("after upsert = %v, want country=SK", got)
	}

	// A second, unrelated key coexists: the namespace is open.
	if res, body = f.do(http.MethodPut, f.annotationPath(chID, "difficulty"),
		map[string]any{"value": "hard"}, auth...); res.StatusCode != http.StatusOK {
		t.Fatalf("set second key: got %d (%s)", res.StatusCode, body)
	}
	got := f.playerAnnotations(chID, playerCookie)
	if len(got) != 2 || got["country"] != "SK" || got["difficulty"] != "hard" {
		t.Fatalf("annotations = %v, want country=SK and difficulty=hard", got)
	}

	// A missing challenge is the FK violation, mapped to 404.
	if res, body = f.do(http.MethodPut, f.annotationPath(999999, "country"),
		map[string]any{"value": "CZ"}, auth...); res.StatusCode != http.StatusNotFound {
		t.Fatalf("set on missing challenge: got %d, want 404 (%s)", res.StatusCode, body)
	}

	// Remove drops one key and leaves the other; a second remove finds nothing.
	if res, body = f.do(http.MethodDelete, f.annotationPath(chID, "country"), nil, auth...); res.StatusCode != http.StatusNoContent {
		t.Fatalf("remove: got %d, want 204 (%s)", res.StatusCode, body)
	}
	got = f.playerAnnotations(chID, playerCookie)
	if len(got) != 1 || got["difficulty"] != "hard" {
		t.Fatalf("after remove = %v, want only difficulty=hard", got)
	}
	if res, body = f.do(http.MethodDelete, f.annotationPath(chID, "country"), nil, auth...); res.StatusCode != http.StatusNotFound {
		t.Fatalf("re-remove: got %d, want 404 (%s)", res.StatusCode, body)
	}

	// Both the set and the remove were audited against the acting admin.
	if n := f.auditCount("challenge_annotations", "INSERT", adminID); n == 0 {
		t.Error("no INSERT audit row for the annotation set")
	}
	if n := f.auditCount("challenge_annotations", "DELETE", adminID); n == 0 {
		t.Error("no DELETE audit row for the annotation remove")
	}
}

// A challenge with no annotations must serve {}, never null: a client that has to handle both will
// get the empty case wrong, because that is the one nobody tests against.
func TestAdminAnnotationsAlwaysAnObject(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers)
	cookie, csrf, _ := f.admin("root", "root@example.com")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	chID := f.adminChallenge("Bare", 100, auth...)
	playerCookie, _ := f.register("Ada", "ada@ctf.test", "correct horse battery")

	res, body := f.do(http.MethodGet, fmt.Sprintf("/api/v1/challenges/%d", chID), nil, withCookie(playerCookie))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("detail: got %d (%s)", res.StatusCode, body)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	if string(raw["annotations"]) != "{}" {
		t.Fatalf("annotations = %s, want {}", raw["annotations"])
	}

	// Same on the admin detail echo.
	res, body = f.do(http.MethodGet, fmt.Sprintf("/api/v1/admin/challenges/%d", chID), nil, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("admin detail: got %d (%s)", res.StatusCode, body)
	}
	var adminRaw map[string]json.RawMessage
	if err := json.Unmarshal(body, &adminRaw); err != nil {
		t.Fatalf("decode admin detail: %v (%s)", err, body)
	}
	if string(adminRaw["annotations"]) != "{}" {
		t.Fatalf("admin annotations = %s, want {}", adminRaw["annotations"])
	}
}

// The admin detail echoes what was set, so the authoring console can render it without a second call.
func TestAdminChallengeDetailEchoesAnnotations(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers)
	cookie, csrf, _ := f.admin("root", "root@example.com")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	chID := f.adminChallenge("Echoed", 100, auth...)
	if res, body := f.do(http.MethodPut, f.annotationPath(chID, "country"),
		map[string]any{"value": "JP"}, auth...); res.StatusCode != http.StatusOK {
		t.Fatalf("set: got %d (%s)", res.StatusCode, body)
	}

	res, body := f.do(http.MethodGet, fmt.Sprintf("/api/v1/admin/challenges/%d", chID), nil, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("admin detail: got %d (%s)", res.StatusCode, body)
	}
	var out struct {
		Annotations map[string]string `json:"annotations"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	if out.Annotations["country"] != "JP" {
		t.Fatalf("admin detail annotations = %v, want country=JP", out.Annotations)
	}
}

// Validation is refused at the boundary with a 422, not stored and discovered by a renderer that
// cannot draw it.
func TestAdminAnnotationValidation(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers)
	cookie, csrf, _ := f.admin("root", "root@example.com")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}
	chID := f.adminChallenge("Validated", 100, auth...)

	tests := []struct {
		name  string
		key   string
		value string
		want  int
	}{
		{name: "valid country", key: "country", value: "CZ", want: http.StatusOK},
		{name: "country lowercase", key: "country", value: "cz", want: http.StatusUnprocessableEntity},
		{name: "country mixed case", key: "country", value: "Cz", want: http.StatusUnprocessableEntity},
		{name: "country alpha-3", key: "country", value: "CZE", want: http.StatusUnprocessableEntity},
		{name: "country one letter", key: "country", value: "C", want: http.StatusUnprocessableEntity},
		{name: "country unassigned", key: "country", value: "XX", want: http.StatusUnprocessableEntity},
		{name: "country not a code at all", key: "country", value: "Czechia", want: http.StatusUnprocessableEntity},

		{name: "arbitrary key is unconstrained", key: "difficulty", value: "Czechia", want: http.StatusOK},
		{name: "key uppercase", key: "Country", value: "CZ", want: http.StatusUnprocessableEntity},
		{name: "key leading digit", key: "1country", value: "CZ", want: http.StatusUnprocessableEntity},
		{name: "key leading underscore", key: "_country", value: "CZ", want: http.StatusUnprocessableEntity},
		{name: "key hyphenated", key: "country-code", value: "CZ", want: http.StatusUnprocessableEntity},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, body := f.do(http.MethodPut, f.annotationPath(chID, tt.key),
				map[string]any{"value": tt.value}, auth...)
			if res.StatusCode != tt.want {
				t.Fatalf("set %s=%s: got %d, want %d (%s)", tt.key, tt.value, res.StatusCode, tt.want, body)
			}
		})
	}

	// Nothing a rejection produced reached the table: the refusals above are refusals, not writes
	// that happened to answer with an error status.
	var n int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM challenge_annotations WHERE challenge_id = $1`, chID).Scan(&n); err != nil {
		t.Fatalf("count annotations: %v", err)
	}
	if n != 2 {
		t.Fatalf("stored %d annotations, want 2 (only the two accepted writes)", n)
	}
}

// A locked challenge withholds its annotations exactly as it withholds its description: where a
// challenge sits on the map is a clue about what it is.
func TestLockedChallengeStripsAnnotations(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers)
	cookie, csrf, _ := f.admin("root", "root@example.com")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	gate := f.adminChallenge("Gate", 100, auth...)
	locked := f.seedChallengeWithReqs("Locked", "misc", 200,
		fmt.Sprintf(`{"prerequisites":[%d],"anonymize":true}`, gate))

	if res, body := f.do(http.MethodPut, f.annotationPath(locked, "country"),
		map[string]any{"value": "JP"}, auth...); res.StatusCode != http.StatusOK {
		t.Fatalf("set on locked challenge: got %d (%s)", res.StatusCode, body)
	}

	playerCookie, _ := f.register("Ada", "ada@ctf.test", "correct horse battery")

	if got := f.playerAnnotations(locked, playerCookie); len(got) != 0 {
		t.Fatalf("locked detail annotations = %v, want none — a locked challenge hands out no clues", got)
	}
	got, found := f.playerListAnnotations(locked, playerCookie)
	if !found {
		t.Fatal("the locked challenge is not on the board at all; it should be visible but locked")
	}
	if len(got) != 0 {
		t.Fatalf("locked list annotations = %v, want none", got)
	}

	// The admin, who is not gated by prerequisites, still sees it — the stripping is about the
	// player's view, not about the row being gone.
	res, body := f.do(http.MethodGet, fmt.Sprintf("/api/v1/admin/challenges/%d", locked), nil, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("admin detail: got %d (%s)", res.StatusCode, body)
	}
	var out struct {
		Annotations map[string]string `json:"annotations"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	if out.Annotations["country"] != "JP" {
		t.Fatalf("admin view of the locked challenge = %v, want country=JP", out.Annotations)
	}
}
