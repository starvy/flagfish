//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/starvy/flagfish/internal/domain/account"
)

type scoreboardBody struct {
	Standings []struct {
		Rank      int    `json:"rank"`
		AccountID int64  `json:"account_id"`
		Name      string `json:"name"`
		Score     int64  `json:"score"`
	} `json:"standings"`
}

func (f *apiFix) seedUser(name, email string) int64 {
	f.t.Helper()
	var id int64
	if err := f.pool.QueryRow(context.Background(),
		`INSERT INTO users (name, email) VALUES ($1,$2) RETURNING id`,
		name, email).Scan(&id); err != nil {
		f.t.Fatalf("seed user %s: %v", email, err)
	}
	return id
}

func (f *apiFix) seedSolve(challengeID, userID int64, value int) {
	f.t.Helper()
	if _, err := f.pool.Exec(context.Background(),
		`INSERT INTO solves (challenge_id, user_id, value) VALUES ($1,$2,$3)`,
		challengeID, userID, value); err != nil {
		f.t.Fatalf("seed solve: %v", err)
	}
}

func TestScoreboardRanksByScore(t *testing.T) {
	f := newAPI(t, account.ModeUsers)

	alice := f.seedUser("Alice", "alice@ctf.test")
	bob := f.seedUser("Bob", "bob@ctf.test")
	chal := f.seedChallenge("Warmup", "misc", 100)
	f.seedSolve(chal, alice, 300)
	f.seedSolve(chal, bob, 100)

	res, body := f.do(http.MethodGet, "/api/v1/scoreboard", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("scoreboard: status %d: %s", res.StatusCode, body)
	}

	var got scoreboardBody
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	if len(got.Standings) != 2 {
		t.Fatalf("want 2 standings, got %d: %s", len(got.Standings), body)
	}

	first := got.Standings[0]
	if first.Rank != 1 || first.AccountID != alice || first.Name != "Alice" || first.Score != 300 {
		t.Fatalf("rank 1 should be Alice/300: %+v", first)
	}
	second := got.Standings[1]
	if second.Rank != 2 || second.AccountID != bob || second.Score != 100 {
		t.Fatalf("rank 2 should be Bob/100: %+v", second)
	}
}

func TestScoreboardEmpty(t *testing.T) {
	f := newAPI(t, account.ModeUsers)

	res, body := f.do(http.MethodGet, "/api/v1/scoreboard", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("scoreboard: status %d: %s", res.StatusCode, body)
	}

	var got scoreboardBody
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	if len(got.Standings) != 0 {
		t.Fatalf("want no standings, got %d: %s", len(got.Standings), body)
	}
}
