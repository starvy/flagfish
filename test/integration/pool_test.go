//go:build integration

package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/starvy/flagfish/internal/domain/account"
)

// hexHash is what a client uploads for a flag: the hex sha256 the server probes the pool by. It
// mirrors flags.Hash over a whitespace-free flag.
func hexHash(flag string) string {
	h := sha256.Sum256([]byte(flag))
	return hex.EncodeToString(h[:])
}

func (f *apiFix) instanceCount(challengeID int64) int64 {
	f.t.Helper()
	var n int64
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM challenge_instances WHERE challenge_id = $1`, challengeID).Scan(&n); err != nil {
		f.t.Fatalf("count instances: %v", err)
	}
	return n
}

type poolUploadResp struct {
	Generation int32    `json:"generation"`
	Inserted   int      `json:"inserted"`
	Idempotent bool     `json:"idempotent"`
	Warnings   []string `json:"warnings"`
}

func TestPool_Upload_Duplicate_Idempotent_List_Stats(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers)
	cookie, csrf, _ := f.admin("Admin", "admin@ctf.test")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}
	ch := f.seedUniqueChallenge("pwn/heap")
	path := "/api/v1/admin/challenges/" + itoa(ch) + "/instances"

	// 1. Upload three instances → generation 1, all three inserted.
	res, body := f.do(http.MethodPut, path, map[string]any{"instances": []map[string]any{
		{"value_hash": hexHash("flagfish{a}")},
		{"value_hash": hexHash("flagfish{b}"), "vars": map[string]any{"host": "x.ctf"}},
		{"value_hash": hexHash("flagfish{c}")},
	}}, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("upload: status %d: %s", res.StatusCode, body)
	}
	var up poolUploadResp
	if err := json.Unmarshal(body, &up); err != nil {
		t.Fatalf("decode upload: %v (%s)", err, body)
	}
	if up.Generation != 1 || up.Inserted != 3 || up.Idempotent {
		t.Fatalf("upload = %+v, want generation 1, inserted 3, idempotent false", up)
	}
	if n := f.instanceCount(ch); n != 3 {
		t.Fatalf("instance rows = %d, want 3", n)
	}

	// 2. Idempotent re-upload of the same set (reordered) writes nothing and reports gen 1.
	res, body = f.do(http.MethodPut, path, map[string]any{"instances": []map[string]any{
		{"value_hash": hexHash("flagfish{c}")},
		{"value_hash": hexHash("flagfish{a}")},
		{"value_hash": hexHash("flagfish{b}")},
	}}, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("re-upload: status %d: %s", res.StatusCode, body)
	}
	_ = json.Unmarshal(body, &up)
	if up.Generation != 1 || up.Inserted != 0 || !up.Idempotent {
		t.Fatalf("re-upload = %+v, want generation 1, inserted 0, idempotent true", up)
	}
	if n := f.instanceCount(ch); n != 3 {
		t.Fatalf("instance rows after idempotent re-upload = %d, want 3", n)
	}

	// 3. An in-batch duplicate is refused atomically: 422, and nothing is written.
	res, body = f.do(http.MethodPut, path, map[string]any{"instances": []map[string]any{
		{"value_hash": hexHash("flagfish{d}")},
		{"value_hash": hexHash("flagfish{d}")},
	}}, auth...)
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("duplicate batch: status %d, want 422: %s", res.StatusCode, body)
	}
	if n := f.instanceCount(ch); n != 3 {
		t.Fatalf("instance rows after refused batch = %d, want 3 — the reject was not atomic", n)
	}

	// 4. A genuinely new set lands in generation 2.
	res, body = f.do(http.MethodPut, path, map[string]any{"instances": []map[string]any{
		{"value_hash": hexHash("flagfish{d}")},
		{"value_hash": hexHash("flagfish{e}")},
	}}, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("second upload: status %d: %s", res.StatusCode, body)
	}
	_ = json.Unmarshal(body, &up)
	if up.Generation != 2 || up.Inserted != 2 {
		t.Fatalf("second upload = %+v, want generation 2, inserted 2", up)
	}

	// 5. List: newest generation first, five rows total.
	res, body = f.do(http.MethodGet, path+"?per_page=100", nil, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list: status %d: %s", res.StatusCode, body)
	}
	var list struct {
		Instances []struct {
			Generation int32  `json:"generation"`
			ValueHash  string `json:"value_hash"`
			IssuedTo   *int64 `json:"issued_to"`
		} `json:"instances"`
		Total int64 `json:"total"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("decode list: %v (%s)", err, body)
	}
	if list.Total != 5 || len(list.Instances) != 5 {
		t.Fatalf("list total = %d (%d rows), want 5", list.Total, len(list.Instances))
	}
	if list.Instances[0].Generation != 2 {
		t.Fatalf("first listed instance generation = %d, want 2 (newest first)", list.Instances[0].Generation)
	}
	if list.Instances[0].IssuedTo != nil {
		t.Fatalf("a freshly uploaded instance is issued_to = %v, want null", *list.Instances[0].IssuedTo)
	}

	// 6. Pool stats: one pool, five total, none issued yet.
	res, body = f.do(http.MethodGet, "/api/v1/admin/pool/stats", nil, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("stats: status %d: %s", res.StatusCode, body)
	}
	var stats struct {
		Pools []struct {
			ChallengeID int64   `json:"challenge_id"`
			Total       int64   `json:"total"`
			Issued      int64   `json:"issued"`
			Utilization float64 `json:"utilization"`
		} `json:"pools"`
	}
	if err := json.Unmarshal(body, &stats); err != nil {
		t.Fatalf("decode stats: %v (%s)", err, body)
	}
	if len(stats.Pools) != 1 {
		t.Fatalf("pools = %d, want 1", len(stats.Pools))
	}
	p := stats.Pools[0]
	if p.ChallengeID != ch || p.Total != 5 || p.Issued != 0 || p.Utilization != 0 {
		t.Fatalf("pool stat = %+v, want challenge %d total 5 issued 0 util 0", p, ch)
	}
}

// An artifact that is not a file of the challenge is refused, so a per-account artifact can never
// point across challenges.
func TestPool_Upload_RejectsForeignArtifact(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers)
	cookie, csrf, _ := f.admin("Admin", "admin@ctf.test")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}
	ch := f.seedUniqueChallenge("pwn/heap")

	// A file belonging to a *different* challenge.
	other := f.seedUniqueChallenge("rev/vm")
	var fileID int64
	if err := f.pool.QueryRow(context.Background(),
		`INSERT INTO files (location, sha256sum, size_bytes, challenge_id, name)
		 VALUES ('x', '\x00', 1, $1, 'a.bin') RETURNING id`, other).Scan(&fileID); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	res, body := f.do(http.MethodPut, "/api/v1/admin/challenges/"+itoa(ch)+"/instances",
		map[string]any{"instances": []map[string]any{
			{"value_hash": hexHash("flagfish{a}"), "artifact_id": fileID},
		}}, auth...)
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("foreign artifact: status %d, want 422: %s", res.StatusCode, body)
	}
	if n := f.instanceCount(ch); n != 0 {
		t.Fatalf("instances after refused upload = %d, want 0", n)
	}
}
