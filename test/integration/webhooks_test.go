//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/starvy/flagfish/internal/config"
	"github.com/starvy/flagfish/internal/egress"
	"github.com/starvy/flagfish/internal/jobs"
	"github.com/starvy/flagfish/internal/mail"
	"github.com/starvy/flagfish/internal/migrate"
)

// loopbackPoster is the poster these tests need: their receiver is an httptest server on
// 127.0.0.1, and the shipped egress policy refuses loopback precisely because an
// admin-supplied URL must not reach it. Naming the exemption is what a deployment with a
// genuine internal receiver does, and everything the tests did not name stays refused.
func loopbackPoster() jobs.WebhookPoster {
	return jobs.NewHTTPPosterWithPolicy(egress.Policy{
		Allowed: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")},
	})
}

func unixString(t time.Time) string { return strconv.FormatInt(t.Unix(), 10) }

// webhookReceiver is a fake Discord endpoint. It records every payload and signals a
// channel, so a test can block until a delivery actually lands rather than sleeping.
type webhookReceiver struct {
	ts   *httptest.Server
	body chan []byte
}

func newWebhookReceiver(t *testing.T) *webhookReceiver {
	t.Helper()
	r := &webhookReceiver{body: make(chan []byte, 8)}
	r.ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		b, err := io.ReadAll(req.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		r.body <- b
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(r.ts.Close)
	return r
}

// waitDelivery blocks for the next delivered payload; a timeout fails the test.
func (r *webhookReceiver) waitDelivery(t *testing.T) []byte {
	t.Helper()
	select {
	case b := <-r.body:
		return b
	case <-time.After(5 * time.Second):
		t.Fatal("expected a webhook delivery, none arrived")
		return nil
	}
}

// expectNoDelivery asserts nothing was delivered. It is called only after the job has
// reached a terminal state, so "nothing yet" means "dropped", not "not processed yet".
func (r *webhookReceiver) expectNoDelivery(t *testing.T) {
	t.Helper()
	select {
	case b := <-r.body:
		t.Fatalf("expected no delivery, got one: %s", b)
	default:
	}
}

// setupWebhook migrates, truncates, seeds the given config rows, and starts a worker
// whose poster is the real HTTP client aimed at the fake receiver.
func setupWebhook(t *testing.T, rows map[string]string) (*pgxpool.Pool, *river.Client[pgx.Tx]) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	if err := migrate.Run(ctx, dsn, log); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := migrate.RunRiver(ctx, dsn, log); err != nil {
		t.Fatalf("river migrate: %v", err)
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)

	// Own the two tables this suite touches. A leftover job from an earlier run would
	// deliver an announcement this test never enqueued.
	if _, execErr := pool.Exec(ctx, `TRUNCATE river_job RESTART IDENTITY`); execErr != nil {
		t.Fatalf("truncate river_job: %v", execErr)
	}
	if _, execErr := pool.Exec(ctx, `DELETE FROM config`); execErr != nil {
		t.Fatalf("clear config: %v", execErr)
	}
	for k, v := range rows {
		if _, execErr := pool.Exec(ctx, `INSERT INTO config (key, value) VALUES ($1,$2)`, k, v); execErr != nil {
			t.Fatalf("seed config %s: %v", k, execErr)
		}
	}

	cfg, err := config.New(ctx, config.NewPGStore(pool), log)
	if err != nil {
		t.Fatalf("config: %v", err)
	}

	inserter, err := jobs.NewInsertOnly(pool)
	if err != nil {
		t.Fatalf("inserter: %v", err)
	}
	worker, err := jobs.NewWorker(pool, jobs.WorkerDeps{
		Mailer: mail.Unconfigured{},
		Config: cfg,
		Poster: loopbackPoster(),
		Log:    log,
	})
	if err != nil {
		t.Fatalf("worker: %v", err)
	}
	if err := worker.Start(ctx); err != nil {
		t.Fatalf("worker start: %v", err)
	}
	t.Cleanup(func() {
		if err := worker.Stop(context.Background()); err != nil {
			t.Logf("worker stop: %v", err)
		}
	})

	return pool, inserter
}

// waitJobDone blocks until the given job reaches a terminal state, so a "no delivery"
// assertion is made only after the worker has actually run the job.
func waitJobDone(t *testing.T, pool *pgxpool.Pool, id int64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var state string
		if err := pool.QueryRow(context.Background(),
			`SELECT state FROM river_job WHERE id = $1`, id).Scan(&state); err != nil {
			t.Fatalf("read job state: %v", err)
		}
		if state == "completed" || state == "discarded" || state == "cancelled" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("job did not reach a terminal state in time")
}

func TestWebhookDeliversFirstBlood(t *testing.T) {
	recv := newWebhookReceiver(t)
	_, inserter := setupWebhook(t, map[string]string{
		"ctf_name":        "flagfish CTF",
		"webhook_enabled": "true",
		"webhook_url":     recv.ts.URL,
		"webhook_events":  "first_blood",
	})

	_, err := inserter.Insert(context.Background(), jobs.AnnounceFirstBlood{
		ChallengeID:   1,
		ChallengeName: "heap-overflow",
		SolveID:       1,
		SolvedAt:      time.Now(),
	}, nil)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	body := recv.waitDelivery(t)
	var payload struct {
		Embeds []struct {
			Title       string `json:"title"`
			Description string `json:"description"`
		} `json:"embeds"`
	}
	if uerr := json.Unmarshal(body, &payload); uerr != nil {
		t.Fatalf("delivered payload is not valid JSON: %v (%s)", uerr, body)
	}
	if len(payload.Embeds) != 1 {
		t.Fatalf("want one embed, got %d", len(payload.Embeds))
	}
	if payload.Embeds[0].Description != "**heap-overflow** just got its first blood." {
		t.Errorf("description = %q", payload.Embeds[0].Description)
	}
}

func TestWebhookSuppressedDuringFreeze(t *testing.T) {
	recv := newWebhookReceiver(t)
	freeze := time.Now().Add(-time.Minute)
	pool, inserter := setupWebhook(t, map[string]string{
		"webhook_enabled": "true",
		"webhook_url":     recv.ts.URL,
		"webhook_events":  "first_blood",
		"freeze":          unixString(freeze),
	})

	// Solved after the freeze: announcing it would leak what the frozen board hides.
	res, err := inserter.Insert(context.Background(), jobs.AnnounceFirstBlood{
		ChallengeID:   1,
		ChallengeName: "post-freeze",
		SolveID:       1,
		SolvedAt:      freeze.Add(30 * time.Second),
	}, nil)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	waitJobDone(t, pool, res.Job.ID)
	recv.expectNoDelivery(t)
}

func TestWebhookDroppedPastTTL(t *testing.T) {
	recv := newWebhookReceiver(t)
	pool, inserter := setupWebhook(t, map[string]string{
		"webhook_enabled": "true",
		"webhook_url":     recv.ts.URL,
		"webhook_events":  "first_blood",
	})

	// Solved two hours ago: past the TTL, an announcement is noise, not news.
	res, err := inserter.Insert(context.Background(), jobs.AnnounceFirstBlood{
		ChallengeID:   1,
		ChallengeName: "ancient",
		SolveID:       1,
		SolvedAt:      time.Now().Add(-2 * time.Hour),
	}, nil)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	waitJobDone(t, pool, res.Job.ID)
	recv.expectNoDelivery(t)
}
