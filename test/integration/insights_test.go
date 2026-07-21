//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/starvy/flagfish/internal/accounts"
	"github.com/starvy/flagfish/internal/adminops"
	"github.com/starvy/flagfish/internal/anticheat"
	"github.com/starvy/flagfish/internal/board"
	"github.com/starvy/flagfish/internal/catalog"
	"github.com/starvy/flagfish/internal/config"
	"github.com/starvy/flagfish/internal/db"
	"github.com/starvy/flagfish/internal/domain/account"
	"github.com/starvy/flagfish/internal/gameplay"
	"github.com/starvy/flagfish/internal/httpapi"
	"github.com/starvy/flagfish/internal/metrics"
	"github.com/starvy/flagfish/internal/stats"
)

// newInsightAPI wires the read-only admin/monitoring surface — the submissions log, the stats
// dashboard and the score-history detail — over a real server, so all three new endpoints are
// reachable. cfgKV seeds config rows (e.g. a freeze) before the snapshot is read.
func newInsightAPI(t *testing.T, mode account.Mode, cfgKV ...[2]string) *apiFix {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("TEST_DATABASE_URL is not set — run `task test-integration`")
	}
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)

	truncate(t, ctx, pool)
	seedInstance(t, ctx, pool, mode)
	for _, kv := range cfgKV {
		if _, execErr := pool.Exec(ctx, `INSERT INTO config (key, value) VALUES ($1,$2)`, kv[0], kv[1]); execErr != nil {
			t.Fatalf("seed config %s: %v", kv[0], execErr)
		}
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg, err := config.New(ctx, config.NewPGStore(pool), log)
	if err != nil {
		t.Fatalf("config: %v", err)
	}

	acct := accounts.NewService(pool, mode, log)
	srv := httpapi.New(httpapi.Options{
		Config:    cfg,
		Auth:      acct,
		Limiter:   accounts.NewLimiter(pool, 1000, 60_000_000_000),
		Log:       log,
		Accounts:  acct,
		Gameplay:  gameplay.New(pool, stubInserter{}, mode),
		Catalog:   catalog.New(pool),
		Board:     board.New(pool, mode),
		AdminOps:  adminops.New(pool),
		Anticheat: anticheat.New(pool),
		Stats:     stats.New(pool),
		Metrics:   metrics.New(ctx, pool, log, nil),
	})

	ts := httptest.NewServer(srv.Router)
	t.Cleanup(ts.Close)

	return &apiFix{
		t: t, pool: pool, q: db.New(pool), acct: acct, server: ts,
		client: &http.Client{
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

// seedDatedSubmission inserts one attempt with an explicit date so keyset ordering is deterministic.
// attributedTo <= 0 leaves attribution NULL, matching a wrong answer or a non-unique correct one.
func (f *apiFix) seedDatedSubmission(challengeID, userID int64, kind string, at time.Time, attributedTo int64) int64 {
	f.t.Helper()
	var attr *int64
	if attributedTo > 0 {
		attr = &attributedTo
	}
	var id int64
	if err := f.pool.QueryRow(context.Background(),
		`INSERT INTO submissions (challenge_id, user_id, type, provided, date, attributed_account_id)
		 VALUES ($1,$2,$3,'x',$4,$5) RETURNING id`,
		challengeID, userID, kind, at, attr).Scan(&id); err != nil {
		f.t.Fatalf("seed submission: %v", err)
	}
	return id
}

type subListView struct {
	Submissions []struct {
		ID                  int64     `json:"id"`
		Date                time.Time `json:"date"`
		Type                string    `json:"type"`
		ChallengeID         int64     `json:"challenge_id"`
		ChallengeName       string    `json:"challenge_name"`
		UserID              int64     `json:"user_id"`
		UserName            *string   `json:"user_name"`
		AttributedAccountID *int64    `json:"attributed_account_id"`
	} `json:"submissions"`
	NextCursor string `json:"next_cursor"`
	Limit      int    `json:"limit"`
}

func (f *apiFix) decodeSubList(body []byte) subListView {
	f.t.Helper()
	var v subListView
	if err := json.Unmarshal(body, &v); err != nil {
		f.t.Fatalf("decode submissions: %v (%s)", err, body)
	}
	return v
}

// --- submissions log ------------------------------------------------------

// The feed is keyset-paginated newest-first: two pages of two must walk the whole log strictly in
// descending order with no row seen twice, and the final short page must carry no cursor.
func TestSubmissionsLogKeysetPagination(t *testing.T) {
	f := newInsightAPI(t, account.ModeUsers)
	ch := f.seedChallenge("Rev", "rev", 100)
	u := f.seedNamedUser("Ada", "ada@ctf.test")
	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	ids := make([]int64, 0, 5)
	for i := range 5 {
		ids = append(ids, f.seedDatedSubmission(ch, u, "incorrect", base.Add(time.Duration(i)*time.Minute), 0))
	}
	admin := f.registerAdmin("Boss", "boss@ctf.test", "correct-horse-battery")

	var seen []int64
	cursor := ""
	pages := 0
	for {
		path := "/api/v1/admin/submissions?limit=2"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		page := f.decodeSubList(f.getAuthed(t, path, admin))
		pages++
		var last int64
		for i, s := range page.Submissions {
			if len(seen) > 0 && s.ID >= seen[len(seen)-1] {
				t.Fatalf("keyset order broken: %d after %d", s.ID, seen[len(seen)-1])
			}
			if i > 0 && s.ID >= last {
				t.Fatalf("within-page order broken: %d after %d", s.ID, last)
			}
			last = s.ID
			seen = append(seen, s.ID)
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
		if pages > 10 {
			t.Fatal("pagination did not terminate")
		}
	}
	if len(seen) != len(ids) {
		t.Fatalf("expected %d rows across pages, saw %d", len(ids), len(seen))
	}
	// Newest id first.
	if seen[0] != ids[len(ids)-1] {
		t.Fatalf("newest-first violated: first row %d, newest seeded %d", seen[0], ids[len(ids)-1])
	}
}

// Filters combine with AND and the stamped attribution is passed through verbatim.
func TestSubmissionsLogFilters(t *testing.T) {
	f := newInsightAPI(t, account.ModeUsers)
	ch1 := f.seedChallenge("A", "web", 100)
	ch2 := f.seedChallenge("B", "pwn", 200)
	ada := f.seedNamedUser("Ada", "ada@ctf.test")
	bob := f.seedNamedUser("Bob", "bob@ctf.test")
	base := time.Now().Add(-time.Hour)
	f.seedDatedSubmission(ch1, ada, "incorrect", base, 0)
	f.seedDatedSubmission(ch1, ada, "correct", base.Add(time.Minute), bob) // attributed to Bob (shared)
	f.seedDatedSubmission(ch2, bob, "incorrect", base.Add(2*time.Minute), 0)
	admin := f.registerAdmin("Boss", "boss@ctf.test", "correct-horse-battery")

	// type=correct
	got := f.decodeSubList(f.getAuthed(t, "/api/v1/admin/submissions?type=correct", admin))
	if len(got.Submissions) != 1 || got.Submissions[0].Type != "correct" {
		t.Fatalf("type filter: want 1 correct, got %+v", got.Submissions)
	}
	if got.Submissions[0].AttributedAccountID == nil || *got.Submissions[0].AttributedAccountID != bob {
		t.Fatalf("attribution not passed through: want %d, got %v", bob, got.Submissions[0].AttributedAccountID)
	}

	// challenge_id=ch1 AND user_id=ada
	got = f.decodeSubList(f.getAuthed(t, "/api/v1/admin/submissions?challenge_id="+itoa(ch1)+"&user_id="+itoa(ada), admin))
	if len(got.Submissions) != 2 {
		t.Fatalf("AND filter: want 2 rows for ch1+ada, got %d", len(got.Submissions))
	}
	for _, s := range got.Submissions {
		if s.ChallengeID != ch1 || s.UserID != ada {
			t.Fatalf("AND filter leaked a row: %+v", s)
		}
	}
}

func TestSubmissionsLogRejectsNonAdmin(t *testing.T) {
	f := newInsightAPI(t, account.ModeUsers)
	player, _ := f.register("Ada", "ada@ctf.test", "correct-horse-battery")

	res, _ := f.do(http.MethodGet, "/api/v1/admin/submissions", nil)
	if res.StatusCode == http.StatusOK {
		t.Fatalf("anonymous reached the submissions log: %d", res.StatusCode)
	}
	res, _ = f.do(http.MethodGet, "/api/v1/admin/submissions", nil, withCookie(player))
	if res.StatusCode == http.StatusOK {
		t.Fatalf("non-admin reached the submissions log: %d", res.StatusCode)
	}
}

func TestSubmissionsLogRejectsBadInput(t *testing.T) {
	f := newInsightAPI(t, account.ModeUsers)
	admin := f.registerAdmin("Boss", "boss@ctf.test", "correct-horse-battery")
	for _, path := range []string{
		"/api/v1/admin/submissions?type=bogus",
		"/api/v1/admin/submissions?cursor=not-base64!!",
	} {
		res, body := f.do(http.MethodGet, path, nil, withCookie(admin))
		if res.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("GET %s: want 422, got %d: %s", path, res.StatusCode, body)
		}
	}
}

// --- stats ----------------------------------------------------------------

type statsView struct {
	Totals struct {
		Solves           int64 `json:"solves"`
		Submissions      int64 `json:"submissions"`
		Awards           int64 `json:"awards"`
		SolvedChallenges int64 `json:"solved_challenges"`
	} `json:"totals"`
	SubmissionsByType []struct {
		Type  string `json:"type"`
		Count int64  `json:"count"`
	} `json:"submissions_by_type"`
	ChallengeSolves []struct {
		ChallengeID int64 `json:"challenge_id"`
		SolveCount  int64 `json:"solve_count"`
	} `json:"challenge_solves"`
	SolvesOverTime []struct {
		Count int64 `json:"count"`
	} `json:"solves_over_time"`
	Bucket string `json:"bucket"`
}

func TestStatsOverview(t *testing.T) {
	f := newInsightAPI(t, account.ModeUsers)
	ch1 := f.seedChallenge("A", "web", 100)
	ch2 := f.seedChallenge("B", "pwn", 200)
	unsolved := f.seedChallenge("C", "misc", 50)
	ada := f.seedNamedUser("Ada", "ada@ctf.test")
	bob := f.seedNamedUser("Bob", "bob@ctf.test")
	at := time.Now().Add(-time.Hour)
	f.seedDatedSolve(ch1, ada, 100, at)
	f.seedDatedSolve(ch1, bob, 100, at.Add(time.Minute))
	f.seedDatedSolve(ch2, ada, 200, at.Add(2*time.Minute))
	f.seedDatedSubmission(ch1, ada, "correct", at, 0)
	f.seedDatedSubmission(ch1, bob, "incorrect", at, 0)
	f.seedDatedSubmission(ch2, ada, "incorrect", at, 0)
	if _, err := f.pool.Exec(context.Background(),
		`INSERT INTO awards (user_id, type, name, value) VALUES ($1,'standard','bonus',10)`, ada); err != nil {
		t.Fatalf("seed award: %v", err)
	}
	admin := f.registerAdmin("Boss", "boss@ctf.test", "correct-horse-battery")

	var v statsView
	if err := json.Unmarshal(f.getAuthed(t, "/api/v1/admin/stats?bucket=day", admin), &v); err != nil {
		t.Fatalf("decode stats: %v", err)
	}
	if v.Totals.Solves != 3 {
		t.Fatalf("total solves: want 3, got %d", v.Totals.Solves)
	}
	if v.Totals.Submissions != 3 {
		t.Fatalf("total submissions: want 3, got %d", v.Totals.Submissions)
	}
	if v.Totals.Awards != 1 {
		t.Fatalf("total awards: want 1, got %d", v.Totals.Awards)
	}
	if v.Totals.SolvedChallenges != 2 {
		t.Fatalf("solved challenges: want 2, got %d", v.Totals.SolvedChallenges)
	}
	byType := map[string]int64{}
	for _, r := range v.SubmissionsByType {
		byType[r.Type] = r.Count
	}
	if byType["correct"] != 1 || byType["incorrect"] != 2 {
		t.Fatalf("submissions by type wrong: %+v", byType)
	}
	// Every challenge appears, including the 0-solve one, so the least-solved tail is visible.
	counts := map[int64]int64{}
	for _, r := range v.ChallengeSolves {
		counts[r.ChallengeID] = r.SolveCount
	}
	if counts[ch1] != 2 || counts[ch2] != 1 || counts[unsolved] != 0 {
		t.Fatalf("per-challenge solve counts wrong: %+v", counts)
	}
	if len(v.SolvesOverTime) == 0 {
		t.Fatal("solves over time is empty")
	}
	if v.Bucket != "day" {
		t.Fatalf("bucket echo wrong: %q", v.Bucket)
	}
}

func TestStatsRejectsNonAdmin(t *testing.T) {
	f := newInsightAPI(t, account.ModeUsers)
	player, _ := f.register("Ada", "ada@ctf.test", "correct-horse-battery")
	res, _ := f.do(http.MethodGet, "/api/v1/admin/stats", nil, withCookie(player))
	if res.StatusCode == http.StatusOK {
		t.Fatalf("non-admin reached stats: %d", res.StatusCode)
	}
}

func TestStatsRejectsBadBucket(t *testing.T) {
	f := newInsightAPI(t, account.ModeUsers)
	admin := f.registerAdmin("Boss", "boss@ctf.test", "correct-horse-battery")
	res, body := f.do(http.MethodGet, "/api/v1/admin/stats?bucket=fortnight", nil, withCookie(admin))
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("bad bucket: want 422, got %d: %s", res.StatusCode, body)
	}
}
