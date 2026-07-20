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

type nextChallengeBody struct {
	ID     int64  `json:"id"`
	NextID *int64 `json:"next_id"`
	Locked bool   `json:"locked"`
}

func decodeNext(t *testing.T, body []byte) nextChallengeBody {
	t.Helper()
	var v nextChallengeBody
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decode challenge: %v (%s)", err, body)
	}
	return v
}

// TestAdminNextIDSetterAndConstraints: next_id is an ordinary authoring field on PATCH, and the FK
// (existence) and the self-reference CHECK arbitrate the two ways it can be wrong — neither is a
// pre-read in Go.
func TestAdminNextIDSetterAndConstraints(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers)
	cookie, csrf, _ := f.admin("root", "root@example.com")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	mk := func(name string) int64 {
		res, body := f.do(http.MethodPost, "/api/v1/admin/challenges",
			map[string]any{"name": name, "category": "misc", "value": 100}, auth...)
		if res.StatusCode != http.StatusCreated {
			t.Fatalf("create %s: got %d (%s)", name, res.StatusCode, body)
		}
		return decodeNext(t, body).ID
	}
	a, b := mk("alpha"), mk("beta")

	// A supplied, existing target lands and is echoed.
	res, body := f.do(http.MethodPatch, "/api/v1/admin/challenges/"+itoa(a),
		map[string]any{"next_id": b}, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("set next_id: got %d, want 200 (%s)", res.StatusCode, body)
	}
	if got := decodeNext(t, body); got.NextID == nil || *got.NextID != b {
		t.Fatalf("echo next_id = %+v, want %d", got.NextID, b)
	}

	// A self-reference is a loop the CHECK refuses, not the handler.
	res, body = f.do(http.MethodPatch, "/api/v1/admin/challenges/"+itoa(a),
		map[string]any{"next_id": a}, auth...)
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("self-reference: got %d, want 422 (%s)", res.StatusCode, body)
	}

	// A non-existent target trips the foreign key, mapped to a 422 that names the id.
	res, body = f.do(http.MethodPatch, "/api/v1/admin/challenges/"+itoa(a),
		map[string]any{"next_id": int64(9_999_999)}, auth...)
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("dangling target: got %d, want 422 (%s)", res.StatusCode, body)
	}

	// Null clears the pointer.
	res, body = f.do(http.MethodPatch, "/api/v1/admin/challenges/"+itoa(a),
		map[string]any{"next_id": nil}, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("clear next_id: got %d, want 200 (%s)", res.StatusCode, body)
	}
	if got := decodeNext(t, body); got.NextID != nil {
		t.Fatalf("after clear next_id = %v, want nil", *got.NextID)
	}

	// The constraint is the database's, not the app's: a raw write cannot store a self-reference.
	if _, err := f.pool.Exec(context.Background(),
		`UPDATE challenges SET next_id = id WHERE id = $1`, a); err == nil {
		t.Fatal("a self-referencing next_id was stored — the CHECK is missing")
	}
}

// TestNextIDDeletionSetsNull: deleting the suggested-next challenge clears the pointer rather than
// leaving it dangling — the FK's ON DELETE SET NULL, observed end to end.
func TestNextIDDeletionSetsNull(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers)
	cookie, csrf, _ := f.admin("root", "root@example.com")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	mk := func(name string) int64 {
		res, body := f.do(http.MethodPost, "/api/v1/admin/challenges",
			map[string]any{"name": name, "category": "misc", "value": 100}, auth...)
		if res.StatusCode != http.StatusCreated {
			t.Fatalf("create %s: got %d (%s)", name, res.StatusCode, body)
		}
		return decodeNext(t, body).ID
	}
	a, b := mk("head"), mk("tail")

	if res, body := f.do(http.MethodPatch, "/api/v1/admin/challenges/"+itoa(a),
		map[string]any{"next_id": b}, auth...); res.StatusCode != http.StatusOK {
		t.Fatalf("set next_id: got %d (%s)", res.StatusCode, body)
	}
	if res, body := f.do(http.MethodDelete, "/api/v1/admin/challenges/"+itoa(b), nil, auth...); res.StatusCode != http.StatusNoContent {
		t.Fatalf("delete target: got %d, want 204 (%s)", res.StatusCode, body)
	}

	var next *int64
	if err := f.pool.QueryRow(context.Background(),
		`SELECT next_id FROM challenges WHERE id = $1`, a).Scan(&next); err != nil {
		t.Fatalf("read next_id after delete: %v", err)
	}
	if next != nil {
		t.Fatalf("next_id = %d after target delete, want NULL (SET NULL)", *next)
	}
}

// TestNextIDOnVerdictAndDetail: a correct solve carries the suggestion on the verdict, a wrong
// answer does not, the unlocked detail carries it, and a locked (preview) detail withholds it — a
// locked challenge must not advertise the graph.
func TestNextIDOnVerdictAndDetail(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers)
	cookie, csrf, _ := f.admin("root", "root@example.com")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	res, body := f.do(http.MethodPost, "/api/v1/admin/challenges",
		map[string]any{"name": "target", "category": "misc", "value": 100}, auth...)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create target: got %d (%s)", res.StatusCode, body)
	}
	target := decodeNext(t, body).ID

	res, body = f.do(http.MethodPost, "/api/v1/admin/challenges",
		map[string]any{"name": "source", "category": "misc", "value": 100}, auth...)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create source: got %d (%s)", res.StatusCode, body)
	}
	source := decodeNext(t, body).ID

	if res, body = f.do(http.MethodPost, "/api/v1/admin/challenges/"+itoa(source)+"/flags",
		map[string]any{"type": "static", "content": "flag{go-next}"}, auth...); res.StatusCode != http.StatusCreated {
		t.Fatalf("add flag: got %d (%s)", res.StatusCode, body)
	}
	if res, body = f.do(http.MethodPatch, "/api/v1/admin/challenges/"+itoa(source),
		map[string]any{"next_id": target}, auth...); res.StatusCode != http.StatusOK {
		t.Fatalf("set next_id: got %d (%s)", res.StatusCode, body)
	}

	pc, pcsrf := f.register("Ada", "ada@ctf.test", "correct horse battery")

	// A wrong answer returns before the correct path, and never carries the suggestion.
	res, body = f.do(http.MethodPost, f.attemptPath(source),
		map[string]any{"flag": "flag{nope}"}, withCookie(pc), withCSRF(pcsrf))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("wrong attempt: got %d (%s)", res.StatusCode, body)
	}
	if got := decodeAttempt(t, body); got.Status != "incorrect" || got.NextID != nil {
		t.Fatalf("wrong attempt = %+v, want incorrect without next_id", got)
	}

	// The correct solve carries it.
	res, body = f.do(http.MethodPost, f.attemptPath(source),
		map[string]any{"flag": "flag{go-next}"}, withCookie(pc), withCSRF(pcsrf))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("correct attempt: got %d (%s)", res.StatusCode, body)
	}
	if got := decodeAttempt(t, body); got.Status != "correct" || got.NextID == nil || *got.NextID != target {
		t.Fatalf("correct attempt = %+v, want correct with next_id=%d", got, target)
	}

	// The unlocked detail carries it too, so a revisited solved challenge still shows the pointer.
	res, body = f.do(http.MethodGet, fmt.Sprintf("/api/v1/challenges/%d", source), nil, withCookie(pc))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("detail: got %d (%s)", res.StatusCode, body)
	}
	if got := decodeNext(t, body); got.NextID == nil || *got.NextID != target {
		t.Fatalf("unlocked detail next_id = %v, want %d", got.NextID, target)
	}

	// A locked (preview) challenge that carries a next_id withholds it: a locked stub must not
	// advertise the graph.
	locked := f.seedChallengeWithReqs("gated", "misc", 100,
		fmt.Sprintf(`{"prerequisites":[%d],"anonymize":"preview"}`, target))
	if _, err := f.pool.Exec(context.Background(),
		`UPDATE challenges SET next_id = $1 WHERE id = $2`, source, locked); err != nil {
		t.Fatalf("set next_id on gated: %v", err)
	}
	res, body = f.do(http.MethodGet, fmt.Sprintf("/api/v1/challenges/%d", locked), nil, withCookie(pc))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("locked detail: got %d (%s)", res.StatusCode, body)
	}
	if got := decodeNext(t, body); !got.Locked || got.NextID != nil {
		t.Fatalf("locked detail = %+v, want locked without next_id", got)
	}
}
