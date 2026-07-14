//go:build integration

package integration

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/starvy/flagfish/internal/domain/account"
)

func TestChallengeBoardListsSeeded(t *testing.T) {
	f := newAPI(t, account.ModeUsers)
	cookie, _ := f.register("Ada", "ada@ctf.test", "correct horse battery")
	f.seedChallenge("Sanity", "misc", 100)

	res, body := f.do(http.MethodGet, "/api/v1/challenges", nil, withCookie(cookie))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list: status %d: %s", res.StatusCode, body)
	}
	list := f.decodeChallenges(body)
	if len(list.Challenges) != 1 {
		t.Fatalf("want one challenge, got %+v", list.Challenges)
	}
	c := list.Challenges[0]
	if c.Name != "Sanity" || c.Category != "misc" || c.Solved {
		t.Fatalf("unexpected challenge row: %+v", c)
	}
	if c.SolveCount == nil || *c.SolveCount != 0 {
		t.Fatalf("solve_count = %v, want 0", c.SolveCount)
	}
}

func TestChallengeDetailOmitsHintContent(t *testing.T) {
	f := newAPI(t, account.ModeUsers)
	cookie, _ := f.register("Ada", "ada@ctf.test", "correct horse battery")
	id := f.seedChallenge("Sanity", "misc", 100)
	const hintContent = "look under the doormat"
	f.seedHint(id, hintContent, 10)

	res, body := f.do(http.MethodGet, fmt.Sprintf("/api/v1/challenges/%d", id), nil, withCookie(cookie))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("detail: status %d: %s", res.StatusCode, body)
	}
	got := string(body)
	if !strings.Contains(got, `"unlocked":false`) {
		t.Fatalf("detail body missing locked hint: %s", got)
	}
	if strings.Contains(got, hintContent) {
		t.Fatalf("detail leaked hint content: %s", got)
	}
}

func TestChallengeDetailUnknownID(t *testing.T) {
	f := newAPI(t, account.ModeUsers)
	cookie, _ := f.register("Ada", "ada@ctf.test", "correct horse battery")

	res, _ := f.do(http.MethodGet, "/api/v1/challenges/999999", nil, withCookie(cookie))
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404 for unknown challenge, got %d", res.StatusCode)
	}
}

func TestChallengeSolvesEmpty(t *testing.T) {
	f := newAPI(t, account.ModeUsers)
	cookie, _ := f.register("Ada", "ada@ctf.test", "correct horse battery")
	id := f.seedChallenge("Sanity", "misc", 100)

	res, body := f.do(http.MethodGet, fmt.Sprintf("/api/v1/challenges/%d/solves", id), nil, withCookie(cookie))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("solves: status %d: %s", res.StatusCode, body)
	}
	if got := string(body); !strings.Contains(got, `"solves":[]`) {
		t.Fatalf("want empty solves list, got: %s", got)
	}
}
