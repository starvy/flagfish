//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/starvy/flagfish/internal/domain/account"
)

// seedTag attaches a tag value to a challenge directly in SQL.
func (f *apiFix) seedTag(challengeID int64, value string) {
	f.t.Helper()
	if _, err := f.pool.Exec(context.Background(),
		`INSERT INTO tags (challenge_id, value) VALUES ($1,$2)`, challengeID, value); err != nil {
		f.t.Fatalf("seed tag %q: %v", value, err)
	}
}

func (f *apiFix) tagUses(value string) int {
	f.t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM tags WHERE value = $1`, value).Scan(&n); err != nil {
		f.t.Fatalf("tag uses %q: %v", value, err)
	}
	return n
}

type auditFeed struct {
	Entries []struct {
		Action      string `json:"action"`
		TargetTable string `json:"target_table"`
		ActorID     *int64 `json:"actor_id"`
	} `json:"entries"`
	Total int64 `json:"total"`
}

func decodeAudit(t *testing.T, body []byte) auditFeed {
	t.Helper()
	var a auditFeed
	if err := json.Unmarshal(body, &a); err != nil {
		t.Fatalf("decode audit: %v (%s)", err, body)
	}
	return a
}

// The audit read API serves the trigger-written trail, newest first, and its filters narrow it.
func TestAdminAuditReadAndFilter(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers)
	cookie, csrf, adminID := f.admin("root", "root@example.com")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	// Two mutations of different shapes, so the filters have something to separate.
	res, body := f.do(http.MethodPost, "/api/v1/admin/challenges",
		map[string]any{"name": "pwn", "category": "binary", "value": 500}, auth...)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create: got %d (%s)", res.StatusCode, body)
	}
	chID := decodeID(t, body)
	res, _ = f.do(http.MethodPatch, "/api/v1/admin/challenges/"+itoa(chID),
		map[string]any{"value": 300}, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("update: got %d", res.StatusCode)
	}

	// Unfiltered: the whole feed, attributed to the admin.
	res, body = f.do(http.MethodGet, "/api/v1/admin/audit", nil, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("audit list: got %d (%s)", res.StatusCode, body)
	}
	all := decodeAudit(t, body)
	if all.Total < 2 {
		t.Fatalf("audit total = %d, want >= 2", all.Total)
	}

	// action=INSERT&target_table=challenges: only the create, never the update.
	res, body = f.do(http.MethodGet, "/api/v1/admin/audit?action=INSERT&target_table=challenges", nil, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("audit filtered: got %d (%s)", res.StatusCode, body)
	}
	inserts := decodeAudit(t, body)
	if len(inserts.Entries) == 0 {
		t.Fatal("filtered feed is empty")
	}
	for _, e := range inserts.Entries {
		if e.Action != "INSERT" || e.TargetTable != "challenges" {
			t.Errorf("filter leaked a %s on %s", e.Action, e.TargetTable)
		}
	}

	// actor filter isolates one admin's writes.
	res, body = f.do(http.MethodGet, "/api/v1/admin/audit?actor="+itoa(adminID), nil, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("audit by actor: got %d", res.StatusCode)
	}
	byActor := decodeAudit(t, body)
	if len(byActor.Entries) == 0 {
		t.Fatal("actor feed is empty")
	}
	for _, e := range byActor.Entries {
		if e.ActorID == nil || *e.ActorID != adminID {
			t.Errorf("actor filter leaked a row for %v", e.ActorID)
		}
	}
}

func TestAdminAuditRejectsNonAdmin(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers)
	cookie, csrf := f.register("mallory", "mallory@example.com", "correct-horse-battery")

	res, _ := f.do(http.MethodGet, "/api/v1/admin/audit", nil, withCookie(cookie), withCSRF(csrf))
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("audit as user: got %d, want 403", res.StatusCode)
	}
	resA, _ := f.do(http.MethodGet, "/api/v1/admin/audit", nil)
	if resA.StatusCode != http.StatusForbidden {
		t.Errorf("audit anonymous: got %d, want 403", resA.StatusCode)
	}
}

// Clearing the freeze via an explicit null must actually null it, and the public board must then
// behave as unfrozen — a post-freeze solve that was hidden reappears.
func TestAdminClearFreezeUnfreezesBoard(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers)
	cookie, csrf, _ := f.admin("root", "root@example.com")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	freeze := time.Now().Add(-time.Hour)
	res, body := f.do(http.MethodPatch, "/api/v1/admin/config",
		map[string]any{"freeze": freeze.Format(time.RFC3339)}, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("set freeze: got %d (%s)", res.StatusCode, body)
	}

	ch := f.seedChallenge("Reversing", "rev", 100)
	early := f.seedNamedUser("Early", "early@ctf.test")
	late := f.seedNamedUser("Late", "late@ctf.test")
	f.seedDatedSolve(ch, early, 100, freeze.Add(-time.Hour))
	f.seedDatedSolve(ch, late, 500, time.Now())

	// Frozen: the post-freeze solver is hidden.
	board := f.decodeStandings(f.get(t, "/api/v1/scoreboard"))
	if board.has("Late") {
		t.Fatalf("post-freeze solver visible while frozen: %+v", board.Standings)
	}

	// Clear the freeze with an explicit null.
	res, body = f.do(http.MethodPatch, "/api/v1/admin/config",
		map[string]any{"freeze": nil}, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("clear freeze: got %d (%s)", res.StatusCode, body)
	}
	var cfg struct {
		Freeze *time.Time `json:"freeze"`
	}
	if err := json.Unmarshal(body, &cfg); err != nil {
		t.Fatalf("decode config: %v (%s)", err, body)
	}
	if cfg.Freeze != nil {
		t.Fatalf("freeze not cleared: %v", cfg.Freeze)
	}

	// Unfrozen: the late solver is now on the board.
	board = f.decodeStandings(f.get(t, "/api/v1/scoreboard"))
	if !board.has("Late") {
		t.Fatalf("post-freeze solver still hidden after clearing freeze: %+v", board.Standings)
	}
}

// A PATCH omitting a nullable field keeps it; a PATCH sending an explicit null clears it.
func TestAdminChallengePatchOmittedVsNull(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers)
	cookie, csrf, _ := f.admin("root", "root@example.com")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	res, body := f.do(http.MethodPost, "/api/v1/admin/challenges",
		map[string]any{"name": "pwn", "category": "binary", "value": 500, "attribution": "alice"}, auth...)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create: got %d (%s)", res.StatusCode, body)
	}
	chID := decodeID(t, body)

	attributionOf := func(body []byte) *string {
		var v struct {
			Attribution *string `json:"attribution"`
		}
		if err := json.Unmarshal(body, &v); err != nil {
			t.Fatalf("decode attribution: %v (%s)", err, body)
		}
		return v.Attribution
	}
	if a := attributionOf(body); a == nil || *a != "alice" {
		t.Fatalf("attribution not set on create: %v", a)
	}

	// Omit attribution: it survives.
	res, body = f.do(http.MethodPatch, "/api/v1/admin/challenges/"+itoa(chID),
		map[string]any{"value": 300}, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("patch value: got %d (%s)", res.StatusCode, body)
	}
	if a := attributionOf(body); a == nil || *a != "alice" {
		t.Fatalf("omitted attribution was not kept: %v", a)
	}

	// Explicit null: it clears.
	res, body = f.do(http.MethodPatch, "/api/v1/admin/challenges/"+itoa(chID),
		map[string]any{"attribution": nil}, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("patch null: got %d (%s)", res.StatusCode, body)
	}
	if a := attributionOf(body); a != nil {
		t.Fatalf("explicit null did not clear attribution: %v", *a)
	}
}

// Clearing a dynamic-scoring param the challenge still needs is refused loudly, not silently stored.
func TestAdminChallengeClearRejectedByCheck(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers)
	cookie, csrf, _ := f.admin("root", "root@example.com")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	res, body := f.do(http.MethodPost, "/api/v1/admin/challenges",
		map[string]any{
			"name": "decayed", "category": "misc", "value": 500,
			"function": "linear", "initial": 500, "minimum": 100, "decay": 20,
		}, auth...)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create dynamic: got %d (%s)", res.StatusCode, body)
	}
	chID := decodeID(t, body)

	res, body = f.do(http.MethodPatch, "/api/v1/admin/challenges/"+itoa(chID),
		map[string]any{"decay": nil}, auth...)
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("clear decay on a dynamic challenge: got %d, want 422 (%s)", res.StatusCode, body)
	}
}

// Tag admin: list, merge (with a collision), and a guarded whole-board delete — each mutation
// audited.
func TestAdminTagMergeAndDelete(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers)
	cookie, csrf, adminID := f.admin("root", "root@example.com")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	a := f.seedChallenge("A", "rev", 100)
	b := f.seedChallenge("B", "rev", 100)
	f.seedTag(a, "web")
	f.seedTag(a, "pwn")
	f.seedTag(b, "web")

	// list
	res, body := f.do(http.MethodGet, "/api/v1/admin/tags", nil, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list tags: got %d (%s)", res.StatusCode, body)
	}
	var tags struct {
		Tags []struct {
			Value string `json:"value"`
			Uses  int64  `json:"uses"`
		} `json:"tags"`
	}
	if err := json.Unmarshal(body, &tags); err != nil {
		t.Fatalf("decode tags: %v (%s)", err, body)
	}
	if len(tags.Tags) != 2 {
		t.Fatalf("want 2 distinct tags, got %+v", tags.Tags)
	}

	// merge web -> pwn: challenge A already has pwn (a collision that is dropped), challenge B is
	// renamed. pwn ends up on both; web is gone.
	res, body = f.do(http.MethodPost, "/api/v1/admin/tags/web/merge",
		map[string]any{"into": "pwn"}, auth...)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("merge: got %d (%s)", res.StatusCode, body)
	}
	if got := f.tagUses("web"); got != 0 {
		t.Errorf("web still has %d uses after merge", got)
	}
	if got := f.tagUses("pwn"); got != 2 {
		t.Errorf("pwn has %d uses after merge, want 2", got)
	}
	// The merge both deleted a collision row and renamed another — both audited.
	if got := f.auditCount("tags", "DELETE", adminID); got == 0 {
		t.Error("no DELETE audit row for the merged-away collision")
	}
	if got := f.auditCount("tags", "UPDATE", adminID); got == 0 {
		t.Error("no UPDATE audit row for the rename")
	}

	// delete without force is refused while in use.
	res, body = f.do(http.MethodDelete, "/api/v1/admin/tags/pwn", nil, auth...)
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("unforced delete of in-use tag: got %d, want 409 (%s)", res.StatusCode, body)
	}

	// delete with force removes it from the whole board and is audited.
	res, _ = f.do(http.MethodDelete, "/api/v1/admin/tags/pwn?force=true", nil, auth...)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("forced delete: got %d, want 204", res.StatusCode)
	}
	if got := f.tagUses("pwn"); got != 0 {
		t.Errorf("pwn survived a forced delete: %d uses", got)
	}

	// deleting a tag that does not exist is a 404.
	res, _ = f.do(http.MethodDelete, "/api/v1/admin/tags/nope?force=true", nil, auth...)
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("delete of absent tag: got %d, want 404", res.StatusCode)
	}
}

// Bulk reorder sets positions transactionally and rejects a batch that names a missing challenge.
func TestAdminReorderChallenges(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers)
	cookie, csrf, adminID := f.admin("root", "root@example.com")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	a := f.seedChallenge("A", "rev", 100)
	b := f.seedChallenge("B", "rev", 100)

	res, body := f.do(http.MethodPut, "/api/v1/admin/challenges/order",
		map[string]any{"items": []map[string]any{{"id": a, "position": 5}, {"id": b, "position": 2}}}, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("reorder: got %d (%s)", res.StatusCode, body)
	}
	if got := f.challengePosition(a); got != 5 {
		t.Errorf("A position = %d, want 5", got)
	}
	if got := f.challengePosition(b); got != 2 {
		t.Errorf("B position = %d, want 2", got)
	}
	if got := f.auditCount("challenges", "UPDATE", adminID); got == 0 {
		t.Error("no UPDATE audit row for the reorder")
	}

	// A batch naming a missing id is refused whole.
	res, _ = f.do(http.MethodPut, "/api/v1/admin/challenges/order",
		map[string]any{"items": []map[string]any{{"id": a, "position": 9}, {"id": 999999, "position": 1}}}, auth...)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("reorder with missing id: got %d, want 404", res.StatusCode)
	}
	if got := f.challengePosition(a); got != 5 {
		t.Errorf("A position changed to %d despite the batch being refused", got)
	}
}

func (f *apiFix) challengePosition(id int64) int32 {
	f.t.Helper()
	var p int32
	if err := f.pool.QueryRow(context.Background(),
		`SELECT position FROM challenges WHERE id = $1`, id).Scan(&p); err != nil {
		f.t.Fatalf("challenge position %d: %v", id, err)
	}
	return p
}
