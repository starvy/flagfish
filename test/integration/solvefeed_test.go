//go:build integration

package integration

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/starvy/flagfish/internal/accounts"
	"github.com/starvy/flagfish/internal/adminops"
	"github.com/starvy/flagfish/internal/board"
	"github.com/starvy/flagfish/internal/catalog"
	"github.com/starvy/flagfish/internal/config"
	"github.com/starvy/flagfish/internal/domain/account"
	"github.com/starvy/flagfish/internal/gameplay"
	"github.com/starvy/flagfish/internal/health"
	"github.com/starvy/flagfish/internal/httpapi"
	"github.com/starvy/flagfish/internal/metrics"
	"github.com/starvy/flagfish/internal/solvefeed"
)

// solveFix is the API fixture wired with the solve feed and a live LISTEN pump. Like the
// notifications fixture it owns the pump lifecycle here, where the tests that depend on it live.
type solveFix struct {
	*apiFix
}

func newSolveAPI(t *testing.T, mode account.Mode, cfgKV ...[2]string) *solveFix {
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
	reg := health.NewRegistry()
	feed := solvefeed.NewBroadcaster(pool, log, reg)

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
		SolveFeed: feed,
		Metrics:   metrics.New(ctx, pool, log, reg),
	})

	ts := httptest.NewServer(srv.Router)
	t.Cleanup(ts.Close)

	pumpCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	var runErr error
	go func() {
		defer close(done)
		runErr = feed.Run(pumpCtx)
	}()
	// Registered after ts.Close, so it runs first (LIFO): stop the pump and let it hand its pooled
	// connection back before the pool closes.
	t.Cleanup(func() {
		cancel()
		<-done
		if runErr != nil && !errors.Is(runErr, context.Canceled) {
			t.Errorf("solve feed pump exited: %v", runErr)
		}
	})

	return &solveFix{apiFix: &apiFix{
		t: t, pool: pool, acct: acct, server: ts,
		client: &http.Client{
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}}
}

// sseSolve is the decoded data payload. A separate type keeps the test independent of the server's
// own struct, so a field rename cannot pass by agreeing with itself.
type sseSolve struct {
	SolveID     int64     `json:"solve_id"`
	ChallengeID int64     `json:"challenge_id"`
	FirstBlood  bool      `json:"first_blood"`
	SolvedAt    time.Time `json:"solved_at"`
}

type solveStream struct {
	res    *http.Response
	events <-chan sseSolve
	// names carries the SSE event name of each payload, so the contract's `event: solve` is
	// asserted rather than assumed.
	names <-chan string
}

func (s *solveStream) close() { _ = s.res.Body.Close() }

func (s *solveStream) next(t *testing.T, within time.Duration) sseSolve {
	t.Helper()
	select {
	case ev, ok := <-s.events:
		if !ok {
			t.Fatal("stream closed before an event arrived")
		}
		return ev
	case <-time.After(within):
		t.Fatal("timed out waiting for a solve event")
		return sseSolve{}
	}
}

// expectNone fails if any event arrives within the window. It is how suppression is proved: the
// assertion is an absence, so it has to be given real time to be wrong.
func (s *solveStream) expectNone(t *testing.T, within time.Duration, why string) {
	t.Helper()
	select {
	case ev, ok := <-s.events:
		if !ok {
			return // stream closed; nothing was delivered, which is what we are asserting
		}
		t.Fatalf("%s: received solve %+v, want nothing", why, ev)
	case <-time.After(within):
	}
}

func (sf *solveFix) openSolveStream(cookie string) *solveStream {
	sf.t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet,
		sf.server.URL+"/api/v1/events/solves", http.NoBody)
	if err != nil {
		sf.t.Fatalf("stream request: %v", err)
	}
	req.AddCookie(&http.Cookie{Name: accounts.SessionCookie, Value: cookie})

	res, err := sf.client.Do(req) //nolint:bodyclose // owned by the returned *solveStream
	if err != nil {
		sf.t.Fatalf("open stream: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		res.Body.Close()
		sf.t.Fatalf("open stream: status %d, want 200", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		res.Body.Close()
		sf.t.Fatalf("open stream: content-type %q, want text/event-stream", ct)
	}

	events := make(chan sseSolve, 16)
	names := make(chan string, 16)
	go func() {
		defer close(events)
		defer close(names)
		r := bufio.NewReader(res.Body)
		var lastName string
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			if name, ok := strings.CutPrefix(line, "event: "); ok {
				lastName = strings.TrimSpace(name)
				continue
			}
			data, ok := strings.CutPrefix(line, "data: ")
			if !ok {
				continue
			}
			var ev sseSolve
			if json.Unmarshal([]byte(strings.TrimSpace(data)), &ev) == nil {
				names <- lastName
				events <- ev
			}
		}
	}()
	return &solveStream{res: res, events: events, names: names}
}

// seedFirstBloodChallenge inserts a visible challenge that ANNOUNCES first blood. The column
// defaults to 'none', which means the challenge does not track first blood at all — so a solve on a
// default challenge correctly reports first_blood=false, and a test that wants the flag has to say
// so, exactly as an author would.
func (sf *solveFix) seedFirstBloodChallenge(name string, value int) int64 {
	sf.t.Helper()
	var id int64
	if err := sf.pool.QueryRow(context.Background(),
		`INSERT INTO challenges (name, category, value, first_blood)
		 VALUES ($1,'misc',$2,'announce') RETURNING id`, name, value).Scan(&id); err != nil {
		sf.t.Fatalf("seed first-blood challenge: %v", err)
	}
	return id
}

// submit posts a flag as the given player.
func (sf *solveFix) submit(chID int64, flag, cookie, csrf string) (apiResp, []byte) {
	sf.t.Helper()
	return sf.do(http.MethodPost, fmt.Sprintf("/api/v1/challenges/%d/attempt", chID),
		map[string]any{"flag": flag},
		withCookie(cookie), withCSRF(csrf))
}

// A correct flag pulses exactly once, naming the challenge and carrying the first-blood fact decided
// under the challenge lock. A wrong flag — most of the traffic — pulses nothing at all.
func TestSolveFeedDeliversCorrectSolvesOnly(t *testing.T) {
	sf := newSolveAPI(t, account.ModeUsers)
	playerCookie, playerCSRF := sf.register("Ada", "ada@ctf.test", "correct-horse-battery")

	chID := sf.seedFirstBloodChallenge("Streamed", 100)
	sf.seedFlag(chID, "flag{correct}")

	watcher, _ := sf.register("Watcher", "watcher@ctf.test", "correct-horse-battery")
	s := sf.openSolveStream(watcher)
	defer s.close()

	// A wrong answer must never reach the feed: it never takes the challenge lock, and it never
	// publishes.
	if res, body := sf.submit(chID, "flag{wrong}", playerCookie, playerCSRF); res.StatusCode != http.StatusOK {
		t.Fatalf("wrong submit: got %d (%s)", res.StatusCode, body)
	}
	s.expectNone(t, time.Second, "a wrong answer")

	if res, body := sf.submit(chID, "flag{correct}", playerCookie, playerCSRF); res.StatusCode != http.StatusOK {
		t.Fatalf("correct submit: got %d (%s)", res.StatusCode, body)
	}

	ev := s.next(t, 3*time.Second)
	if ev.ChallengeID != chID {
		t.Errorf("event challenge_id = %d, want %d", ev.ChallengeID, chID)
	}
	if !ev.FirstBlood {
		t.Error("event first_blood = false, but this was the first solve of the challenge")
	}
	if ev.SolveID == 0 {
		t.Error("event solve_id = 0, want the id of the solve that landed")
	}
	if ev.SolvedAt.IsZero() {
		t.Error("event solved_at is zero, want the solve's timestamp")
	}

	// The contract names the event, and a client subscribing to `solve` gets nothing if we rename it.
	select {
	case name := <-s.names:
		if name != "solve" {
			t.Errorf("SSE event name = %q, want %q", name, "solve")
		}
	default:
		t.Error("no SSE event name was captured")
	}

	// A duplicate solve by the same account is already-solved: no second row, so no second pulse.
	if res, body := sf.submit(chID, "flag{correct}", playerCookie, playerCSRF); res.StatusCode != http.StatusOK {
		t.Fatalf("duplicate submit: got %d (%s)", res.StatusCode, body)
	}
	s.expectNone(t, time.Second, "a duplicate solve")
}

// The second solver of a challenge is not a first blood, and the flag on the wire has to say so —
// it is stamped by the transaction, not recomputed by the listener.
func TestSolveFeedMarksOnlyTheFirstBlood(t *testing.T) {
	sf := newSolveAPI(t, account.ModeUsers)
	chID := sf.seedFirstBloodChallenge("Contested", 100)
	sf.seedFlag(chID, "flag{shared}")

	watcher, _ := sf.register("Watcher", "watcher@ctf.test", "correct-horse-battery")
	s := sf.openSolveStream(watcher)
	defer s.close()

	firstCookie, firstCSRF := sf.register("First", "first@ctf.test", "correct-horse-battery")
	secondCookie, secondCSRF := sf.register("Second", "second@ctf.test", "correct-horse-battery")

	if res, body := sf.submit(chID, "flag{shared}", firstCookie, firstCSRF); res.StatusCode != http.StatusOK {
		t.Fatalf("first submit: got %d (%s)", res.StatusCode, body)
	}
	if ev := s.next(t, 3*time.Second); !ev.FirstBlood {
		t.Error("the first solver's event has first_blood = false")
	}

	if res, body := sf.submit(chID, "flag{shared}", secondCookie, secondCSRF); res.StatusCode != http.StatusOK {
		t.Fatalf("second submit: got %d (%s)", res.StatusCode, body)
	}
	ev := s.next(t, 3*time.Second)
	if ev.FirstBlood {
		t.Error("the second solver's event has first_blood = true; the blood was already taken")
	}
	if ev.ChallengeID != chID {
		t.Errorf("second event challenge_id = %d, want %d", ev.ChallengeID, chID)
	}
}

// The anti-leak property: a solve landing at or after the freeze must not reach a client, or the
// feed becomes a live scoreboard that outruns the frozen one.
func TestSolveFeedSuppressedByFreeze(t *testing.T) {
	// Frozen as of an hour ago, so every solve in this test lands after the horizon.
	freeze := time.Now().Add(-time.Hour).Unix()
	sf := newSolveAPI(
		t, account.ModeUsers,
		[2]string{"freeze", strconv.FormatInt(freeze, 10)},
	)

	chID := sf.seedChallenge("Frozen", "misc", 100)
	sf.seedFlag(chID, "flag{frozen}")

	watcher, _ := sf.register("Watcher", "watcher@ctf.test", "correct-horse-battery")
	s := sf.openSolveStream(watcher)
	defer s.close()

	playerCookie, playerCSRF := sf.register("Ada", "ada@ctf.test", "correct-horse-battery")
	res, body := sf.submit(chID, "flag{frozen}", playerCookie, playerCSRF)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("submit: got %d (%s)", res.StatusCode, body)
	}

	// The solve really happened — the freeze hides it, it does not stop play.
	var solves int
	if err := sf.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM solves WHERE challenge_id = $1`, chID).Scan(&solves); err != nil {
		t.Fatalf("count solves: %v", err)
	}
	if solves != 1 {
		t.Fatalf("solves = %d, want 1: the freeze must hide the solve, not prevent it", solves)
	}

	s.expectNone(t, 2*time.Second, "a solve after the freeze horizon")
}

// An admin gets no exemption here. Widening it would put the frozen board's contents on a channel
// whose URL never says so — the admin surfaces are where live data lives.
func TestSolveFeedFreezeHasNoAdminExemption(t *testing.T) {
	freeze := time.Now().Add(-time.Hour).Unix()
	sf := newSolveAPI(
		t, account.ModeUsers,
		[2]string{"freeze", strconv.FormatInt(freeze, 10)},
	)

	chID := sf.seedChallenge("Frozen", "misc", 100)
	sf.seedFlag(chID, "flag{frozen}")

	adminCookie, _, _ := sf.admin("root", "root@example.com")
	s := sf.openSolveStream(adminCookie)
	defer s.close()

	playerCookie, playerCSRF := sf.register("Ada", "ada@ctf.test", "correct-horse-battery")
	if res, body := sf.submit(chID, "flag{frozen}", playerCookie, playerCSRF); res.StatusCode != http.StatusOK {
		t.Fatalf("submit: got %d (%s)", res.StatusCode, body)
	}

	s.expectNone(t, 2*time.Second, "a frozen solve on an admin's stream")
}

// The stream is behind the wall, like every other authenticated surface.
func TestSolveFeedRejectsAnonymous(t *testing.T) {
	sf := newSolveAPI(t, account.ModeUsers)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet,
		sf.server.URL+"/api/v1/events/solves", http.NoBody)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	res, err := sf.client.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode == http.StatusOK {
		t.Fatalf("anonymous stream was accepted (status %d); it must be rejected", res.StatusCode)
	}
	if res.StatusCode != http.StatusForbidden && res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous stream: status %d, want 401 or 403", res.StatusCode)
	}
}
