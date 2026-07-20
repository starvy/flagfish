//go:build integration

package integration

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/starvy/flagfish/internal/domain/account"
)

// The observability floor: /metrics must serve the exposition format, /readyz must report 200 while
// the pool is up, and the two load-bearing spans must actually record when their endpoints are hit.
func TestMetricsAndReadyz(t *testing.T) {
	f := newAPI(t, account.ModeUsers)

	// /readyz pings the pool, which is up.
	res, _ := f.do(http.MethodGet, "/readyz", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("/readyz: status %d, want 200 with a live pool", res.StatusCode)
	}

	// /metrics serves, and carries both the runtime collector and the always-on pool gauges.
	res, body := f.do(http.MethodGet, "/metrics", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("/metrics: status %d, want 200", res.StatusCode)
	}
	for _, want := range []string{"go_goroutines", "flagfish_db_pool_connections", "flagfish_db_pool_max_connections"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("/metrics body missing %q", want)
		}
	}

	// Exercise both instrumented spans, then confirm they registered a sample.
	cookie, csrf := f.register("player", "player@example.com", "correct-horse-battery")
	chID := f.seedChallenge("Warmup", "misc", 100)
	f.seedFlag(chID, "flag{correct}")

	sub, subBody := f.do(http.MethodPost, fmt.Sprintf("/api/v1/challenges/%d/attempt", chID),
		map[string]any{"flag": "flag{correct}"}, withCookie(cookie), withCSRF(csrf))
	if sub.StatusCode != http.StatusOK {
		t.Fatalf("submit: status %d: %s", sub.StatusCode, subBody)
	}

	board, boardBody := f.do(http.MethodGet, "/api/v1/scoreboard", nil, withCookie(cookie))
	if board.StatusCode != http.StatusOK {
		t.Fatalf("scoreboard: status %d: %s", board.StatusCode, boardBody)
	}

	_, body = f.do(http.MethodGet, "/metrics", nil)
	for _, want := range []string{
		`flagfish_submit_duration_seconds_count{result="correct"}`,
		"flagfish_scoreboard_duration_seconds_count",
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("/metrics did not record %q after the span was exercised", want)
		}
	}
}
