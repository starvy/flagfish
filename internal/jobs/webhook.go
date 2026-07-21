package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/riverqueue/river"

	"github.com/starvy/flagfish/internal/config"
	"github.com/starvy/flagfish/internal/domain/policy"
	"github.com/starvy/flagfish/internal/egress"
)

// announceTTL bounds how late an announcement may still be delivered. River retries a
// failed webhook with backoff, and a retry cap alone can span hours; a first blood that
// lands long after the fact is noise, not news, so past this age the job is dropped
// rather than sent.
const announceTTL = time.Hour

// WebhookPoster delivers a JSON payload to a webhook endpoint. It is the seam the tests
// replace: production POSTs over HTTP, tests point it at an httptest server or a fake so
// no real network is touched.
type WebhookPoster interface {
	Post(ctx context.Context, url string, payload []byte) error
}

// httpPoster is the production WebhookPoster.
type httpPoster struct{ client *http.Client }

// webhookTimeout bounds a hung receiver: a slow endpoint must not pin a worker slot forever.
const webhookTimeout = 10 * time.Second

// NewHTTPPoster builds the poster the worker uses in a real deployment.
//
// The client is egress-guarded because the URL it posts to is admin-settable: without the
// guard, whoever holds an admin session picks an address inside the deployment's network
// and the worker dials it for them.
func NewHTTPPoster(env *config.Env) WebhookPoster {
	return NewHTTPPosterWithPolicy(egress.Policy{Allowed: env.WebhookAllowedNetworks})
}

// NewHTTPPosterWithPolicy builds a poster against an explicit egress policy. Tests that
// stand up a receiver on loopback use it to name that loopback; production goes through
// NewHTTPPoster so the policy comes from the environment and nowhere else.
func NewHTTPPosterWithPolicy(p egress.Policy) WebhookPoster {
	return &httpPoster{client: p.Client(webhookTimeout)}
}

func (p *httpPoster) Post(ctx context.Context, url string, payload []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("jobs: build webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("jobs: post webhook: %w", err)
	}
	defer resp.Body.Close()

	// A non-2xx is a real failure the retry must see; swallowing it is a first blood
	// nobody was told about and no operator who knows why.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("jobs: webhook returned %s", resp.Status)
	}
	return nil
}

// discordEmbed and discordPayload are the shape Discord (and every Discord-compatible
// receiver) accepts on an incoming webhook.
type discordEmbed struct {
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Color       int    `json:"color,omitempty"`
	Timestamp   string `json:"timestamp,omitempty"`
}

type discordPayload struct {
	Content string         `json:"content,omitempty"`
	Embeds  []discordEmbed `json:"embeds,omitempty"`
}

// firstBloodColor is the red accent Discord renders down the side of the embed.
const firstBloodColor = 0xE74C3C

// firstBloodPayload renders a first-blood announcement to the Discord webhook JSON. It
// is pure: it derives nothing from the clock or from mutable rows, only from the job's
// own snapshot and the instance name, so it is exercised directly in a unit test.
func firstBloodPayload(ctfName string, a AnnounceFirstBlood) ([]byte, error) {
	title := "First blood"
	if ctfName != "" {
		title = ctfName + " — first blood"
	}
	p := discordPayload{
		Embeds: []discordEmbed{{
			Title:       title,
			Description: fmt.Sprintf("**%s** just got its first blood.", a.ChallengeName),
			Color:       firstBloodColor,
			Timestamp:   a.SolvedAt.UTC().Format(time.RFC3339),
		}},
	}
	b, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("jobs: marshal first-blood payload: %w", err)
	}
	return b, nil
}

// AnnounceFirstBloodWorker delivers first-blood announcements to the configured webhook.
//
// It reads the config snapshot at send time, not at boot: whether the feed is on, its
// URL, and the freeze horizon can all change during an event, and the value that matters
// is the one in force when the job runs.
type AnnounceFirstBloodWorker struct {
	river.WorkerDefaults[AnnounceFirstBlood]
	Config *config.Manager
	Poster WebhookPoster
	Log    *slog.Logger
	// now is the clock, injectable so the TTL drop is testable without waiting an hour.
	now func() time.Time
}

func (w *AnnounceFirstBloodWorker) clock() time.Time {
	if w.now != nil {
		return w.now()
	}
	return time.Now()
}

func (w *AnnounceFirstBloodWorker) Work(ctx context.Context, job *river.Job[AnnounceFirstBlood]) error {
	snap := w.Config.Current()
	a := job.Args

	// Feed off, or this event kind not selected: nothing to deliver. Not an error —
	// completing the job is correct, retrying it would be pointless.
	if !snap.WebhookEnabled || snap.WebhookURL == "" || !snap.WebhookEvents.Enabled(config.WebhookEventFirstBlood) {
		return nil
	}

	// Stale: drop rather than deliver news that is no longer news, and rather than keep
	// retrying it forever. Returning nil completes the job so River stops.
	if age := w.clock().Sub(a.SolvedAt); age > announceTTL {
		w.Log.Warn("dropping stale first-blood announcement",
			"challenge_id", a.ChallengeID, "solve_id", a.SolveID, "age", age)
		return nil
	}

	// Freeze suppression: an announcement of a solve at or after the freeze leaks exactly
	// what the frozen scoreboard hides. Drop it — quietly, because it is a correct outcome.
	if policy.SuppressedByFreeze(snap.Freeze, a.SolvedAt) {
		return nil
	}

	payload, err := firstBloodPayload(snap.CTFName, a)
	if err != nil {
		return err
	}
	if err := w.Poster.Post(ctx, snap.WebhookURL, payload); err != nil {
		// Not swallowed: River owns the retry, and a swallowed send is a first blood
		// that vanished with no trace for an operator to find.
		return fmt.Errorf("jobs: deliver first-blood announcement: %w", err)
	}
	return nil
}
