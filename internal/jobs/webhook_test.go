package jobs

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"maps"
	"sync"
	"testing"
	"time"

	"github.com/riverqueue/river"

	"github.com/starvy/flagfish/internal/config"
	"github.com/starvy/flagfish/internal/domain/account"
)

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// mapStore is a config.Store backed by a map, so the worker's decisions can be tested
// against a real config.Manager without a database.
type mapStore struct {
	mu   sync.Mutex
	rows map[string]string
}

func (s *mapStore) All(context.Context) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.rows), nil
}

func (s *mapStore) Replace(_ context.Context, kv map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	maps.Copy(s.rows, kv)
	return nil
}

// Mode reports no instance row: these tests do not care about the account model, and a
// pre-setup store keeps Mode at its default.
func (s *mapStore) Mode(context.Context) (account.Mode, bool, error) {
	return 0, false, nil
}

func newConfig(t *testing.T, rows map[string]string) *config.Manager {
	t.Helper()
	m, err := config.New(context.Background(), &mapStore{rows: rows}, discardLog())
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	return m
}

// recordingPoster captures every delivery so a test can assert what was (and was not) sent.
type recordingPoster struct {
	mu     sync.Mutex
	urls   []string
	bodies [][]byte
}

func (p *recordingPoster) Post(_ context.Context, url string, payload []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.urls = append(p.urls, url)
	p.bodies = append(p.bodies, payload)
	return nil
}

func (p *recordingPoster) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.urls)
}

func TestFirstBloodPayload(t *testing.T) {
	solvedAt := time.Date(2026, 7, 14, 10, 30, 0, 0, time.UTC)
	b, err := firstBloodPayload("flagfish CTF", AnnounceFirstBlood{
		ChallengeName: "heap-overflow",
		SolvedAt:      solvedAt,
	})
	if err != nil {
		t.Fatal(err)
	}

	var got discordPayload
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("payload is not valid JSON: %v", err)
	}
	if len(got.Embeds) != 1 {
		t.Fatalf("want exactly one embed, got %d", len(got.Embeds))
	}
	e := got.Embeds[0]
	if e.Title != "flagfish CTF — first blood" {
		t.Errorf("title = %q", e.Title)
	}
	if want := "**heap-overflow** just got its first blood."; e.Description != want {
		t.Errorf("description = %q, want %q", e.Description, want)
	}
	if e.Color != firstBloodColor {
		t.Errorf("color = %d, want %d", e.Color, firstBloodColor)
	}
	if e.Timestamp != solvedAt.Format(time.RFC3339) {
		t.Errorf("timestamp = %q, want the solve time", e.Timestamp)
	}
}

func TestFirstBloodPayloadWithoutCTFName(t *testing.T) {
	b, err := firstBloodPayload("", AnnounceFirstBlood{ChallengeName: "rev-1"})
	if err != nil {
		t.Fatal(err)
	}
	var got discordPayload
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.Embeds[0].Title != "First blood" {
		t.Errorf("title = %q, want the bare fallback", got.Embeds[0].Title)
	}
}

// TestAnnounceWorkerDecisions drives the worker's deliver/drop decision across the
// config gate, the freeze horizon and the staleness TTL, with the clock and the poster
// injected so no database or network is needed.
func TestAnnounceWorkerDecisions(t *testing.T) {
	freezeUnix := "1784030400" // 2026-07-14T04:00:00Z
	freezeAt := time.Unix(1784030400, 0).UTC()
	now := freezeAt.Add(-time.Hour) // "now" sits an hour before the freeze
	url := "https://discord.example/webhook"

	base := func() map[string]string {
		return map[string]string{
			"ctf_name":        "flagfish CTF",
			"webhook_enabled": "true",
			"webhook_url":     url,
			"webhook_events":  "first_blood",
			"freeze":          freezeUnix,
		}
	}

	for _, tc := range []struct {
		name          string
		mut           func(map[string]string)
		solvedAt      time.Time
		wantDelivered bool
	}{
		{
			name:          "delivers a fresh pre-freeze first blood",
			solvedAt:      now.Add(-time.Minute),
			wantDelivered: true,
		},
		{
			name:          "drops when the feed is disabled",
			mut:           func(r map[string]string) { r["webhook_enabled"] = "false" },
			solvedAt:      now.Add(-time.Minute),
			wantDelivered: false,
		},
		{
			name:          "drops when first_blood is not a selected event",
			mut:           func(r map[string]string) { r["webhook_events"] = "solve" },
			solvedAt:      now.Add(-time.Minute),
			wantDelivered: false,
		},
		{
			name:          "suppresses an event exactly at the freeze",
			solvedAt:      freezeAt,
			wantDelivered: false,
		},
		{
			name:          "suppresses an event after the freeze",
			solvedAt:      freezeAt.Add(time.Minute),
			wantDelivered: false,
		},
		{
			name:          "drops a stale event past the TTL",
			solvedAt:      now.Add(-2 * announceTTL),
			wantDelivered: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := base()
			if tc.mut != nil {
				tc.mut(rows)
			}
			poster := &recordingPoster{}
			w := &AnnounceFirstBloodWorker{
				Config: newConfig(t, rows),
				Poster: poster,
				Log:    discardLog(),
				now:    func() time.Time { return now },
			}
			job := &river.Job[AnnounceFirstBlood]{Args: AnnounceFirstBlood{
				ChallengeID:   1,
				ChallengeName: "heap-overflow",
				SolveID:       1,
				SolvedAt:      tc.solvedAt,
			}}
			if err := w.Work(context.Background(), job); err != nil {
				t.Fatalf("Work returned error: %v", err)
			}
			if got := poster.count() > 0; got != tc.wantDelivered {
				t.Errorf("delivered = %v, want %v", got, tc.wantDelivered)
			}
		})
	}
}
