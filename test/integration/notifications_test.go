//go:build integration

package integration

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
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
	"github.com/starvy/flagfish/internal/httpapi"
	"github.com/starvy/flagfish/internal/notify"
)

// notifyFix is the base API fixture plus the notifications bus, wired with a live LISTEN pump. It
// stands on its own constructor rather than the shared api_harness so the pump lifecycle is owned
// here, where the tests that assert on it live.
type notifyFix struct {
	*apiFix
}

// newNotifyAPI builds the full server with the notifications service and broadcaster, and runs the
// LISTEN pump for the duration of the test. The pump holds its own pool connection; cancelling it
// on cleanup releases that connection before the pool closes.
func newNotifyAPI(t *testing.T, mode account.Mode, mut ...func(*httpapi.Options)) *notifyFix {
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

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg, err := config.New(ctx, config.NewPGStore(pool), log)
	if err != nil {
		t.Fatalf("config: %v", err)
	}

	acct := accounts.NewService(pool, mode, log)
	svc := notify.NewService(pool)
	bc := notify.NewBroadcaster(pool, svc, log)

	opts := httpapi.Options{
		Config:      cfg,
		Auth:        acct,
		Limiter:     accounts.NewLimiter(pool, 1000, 60_000_000_000),
		Log:         log,
		Accounts:    acct,
		Gameplay:    gameplay.New(pool, stubInserter{}, mode),
		Catalog:     catalog.New(pool),
		Board:       board.New(pool, mode),
		AdminOps:    adminops.New(pool),
		Notify:      svc,
		Broadcaster: bc,
	}
	for _, m := range mut {
		m(&opts)
	}
	srv := httpapi.New(opts)

	ts := httptest.NewServer(srv.Router)
	t.Cleanup(ts.Close)

	pumpCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	var runErr error
	go func() {
		defer close(done)
		runErr = bc.Run(pumpCtx)
	}()
	// Registered after ts.Close above, so it runs first (LIFO): stop the pump and wait for it to
	// return — releasing its pooled connection — before the pool is closed.
	t.Cleanup(func() {
		cancel()
		<-done
		if runErr != nil && !errors.Is(runErr, context.Canceled) {
			t.Errorf("broadcaster pump exited: %v", runErr)
		}
	})

	return &notifyFix{apiFix: &apiFix{
		t: t, pool: pool, acct: acct, server: ts,
		client: &http.Client{
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}}
}

// sseNotification is the decoded SSE data payload. It mirrors the server's notification body; a
// separate type keeps the test independent of the unexported transport struct.
type sseNotification struct {
	ID      int64  `json:"id"`
	Title   string `json:"title"`
	Content string `json:"content"`
}

// stream is an open SSE connection with a background reader that parses `data:` lines.
type stream struct {
	res    *http.Response
	events <-chan sseNotification
}

func (s *stream) close() { _ = s.res.Body.Close() }

// next returns the next event, or fails the test if none arrives before the deadline.
func (s *stream) next(t *testing.T, within time.Duration) sseNotification {
	t.Helper()
	select {
	case ev, ok := <-s.events:
		if !ok {
			t.Fatal("stream closed before an event arrived")
		}
		return ev
	case <-time.After(within):
		t.Fatal("timed out waiting for an SSE event")
		return sseNotification{}
	}
}

// waitFor drains events until it sees the given id, tolerating replayed or earlier ones. Fails if
// the id does not arrive before the deadline.
func (s *stream) waitFor(t *testing.T, id int64, within time.Duration) sseNotification {
	t.Helper()
	deadline := time.After(within)
	for {
		select {
		case ev, ok := <-s.events:
			if !ok {
				t.Fatal("stream closed before the event arrived")
			}
			if ev.ID == id {
				return ev
			}
		case <-deadline:
			t.Fatalf("timed out waiting for event id %d", id)
			return sseNotification{}
		}
	}
}

// openStream connects to the SSE endpoint with the given cookie and starts a reader goroutine. The
// subscription is registered server-side before the 200 headers are written, so an event published
// after this returns cannot be missed.
func (nf *notifyFix) openStream(cookie string) *stream {
	nf.t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet,
		nf.server.URL+"/api/v1/notifications/stream", http.NoBody)
	if err != nil {
		nf.t.Fatalf("stream request: %v", err)
	}
	req.AddCookie(&http.Cookie{Name: accounts.SessionCookie, Value: cookie})

	res, err := nf.client.Do(req) //nolint:bodyclose // owned by the returned *stream, closed in stream.close()
	if err != nil {
		nf.t.Fatalf("open stream: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		res.Body.Close()
		nf.t.Fatalf("open stream: status %d, want 200", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		res.Body.Close()
		nf.t.Fatalf("open stream: content-type %q, want text/event-stream", ct)
	}

	events := make(chan sseNotification, 16)
	go func() {
		defer close(events)
		r := bufio.NewReader(res.Body)
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			data, ok := strings.CutPrefix(line, "data: ")
			if !ok {
				continue
			}
			var ev sseNotification
			if json.Unmarshal([]byte(strings.TrimSpace(data)), &ev) == nil {
				events <- ev
			}
		}
	}()
	return &stream{res: res, events: events}
}

// publish posts a notification through the admin endpoint and returns its id.
func (nf *notifyFix) publish(adminCookie, csrf, title, content string) int64 {
	nf.t.Helper()
	res, body := nf.do(http.MethodPost, "/api/v1/admin/notifications",
		map[string]any{"title": title, "content": content},
		withCookie(adminCookie), withCSRF(csrf))
	if res.StatusCode != http.StatusCreated {
		nf.t.Fatalf("publish: status %d: %s", res.StatusCode, body)
	}
	return decodeID(nf.t, body)
}

func TestNotificationStreamReceivesPublished(t *testing.T) {
	nf := newNotifyAPI(t, account.ModeUsers)
	playerCookie, _ := nf.register("player", "player@example.com", "correct-horse-battery")
	adminCookie, adminCSRF, _ := nf.admin("root", "root@example.com")

	s := nf.openStream(playerCookie)
	defer s.close()

	id := nf.publish(adminCookie, adminCSRF, "Heads up", "the CTF starts soon")

	ev := s.next(t, 3*time.Second)
	if ev.ID != id {
		t.Fatalf("received id %d, want %d", ev.ID, id)
	}
	if ev.Title != "Heads up" || ev.Content != "the CTF starts soon" {
		t.Fatalf("received %+v, want the published notification", ev)
	}
}

func TestNotificationPersistedAndListed(t *testing.T) {
	nf := newNotifyAPI(t, account.ModeUsers)
	playerCookie, _ := nf.register("player", "player@example.com", "correct-horse-battery")
	adminCookie, adminCSRF, _ := nf.admin("root", "root@example.com")

	id := nf.publish(adminCookie, adminCSRF, "Persisted", "this must be readable via the list")

	res, body := nf.do(http.MethodGet, "/api/v1/notifications", nil, withCookie(playerCookie))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list: status %d: %s", res.StatusCode, body)
	}
	var out struct {
		Notifications []sseNotification `json:"notifications"`
		Total         int64             `json:"total"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode list: %v (%s)", err, body)
	}
	if out.Total != 1 || len(out.Notifications) != 1 {
		t.Fatalf("list returned total=%d items=%d, want one persisted notification", out.Total, len(out.Notifications))
	}
	if out.Notifications[0].ID != id || out.Notifications[0].Title != "Persisted" {
		t.Fatalf("list returned %+v, want the published row", out.Notifications[0])
	}
}

func TestNotificationReplayOnConnect(t *testing.T) {
	nf := newNotifyAPI(t, account.ModeUsers)
	playerCookie, _ := nf.register("player", "player@example.com", "correct-horse-battery")
	adminCookie, adminCSRF, _ := nf.admin("root", "root@example.com")

	// Published BEFORE the stream opens: it must arrive as replay, not as a live event.
	id := nf.publish(adminCookie, adminCSRF, "Earlier", "published before the client connected")

	s := nf.openStream(playerCookie)
	defer s.close()

	ev := s.next(t, 3*time.Second)
	if ev.ID != id || ev.Title != "Earlier" {
		t.Fatalf("replay delivered %+v, want the earlier notification", ev)
	}
}

func TestNotificationUnauthorizedStreamRejected(t *testing.T) {
	nf := newNotifyAPI(t, account.ModeUsers)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet,
		nf.server.URL+"/api/v1/notifications/stream", http.NoBody)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	res, err := nf.client.Do(req)
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

// A disconnected subscriber must be unregistered, and its departure must not wedge the pump: a
// later publish still reaches a fresh subscriber, and the goroutine count does not grow per
// connection.
func TestNotificationSubscriberCleanup(t *testing.T) {
	nf := newNotifyAPI(t, account.ModeUsers)
	playerCookie, _ := nf.register("player", "player@example.com", "correct-horse-battery")
	adminCookie, adminCSRF, _ := nf.admin("root", "root@example.com")

	before := runtime.NumGoroutine()

	// Open and immediately drop a batch of streams.
	for range 10 {
		s := nf.openStream(playerCookie)
		s.close()
	}
	// Give the handlers time to observe the disconnect and unregister.
	time.Sleep(200 * time.Millisecond)

	// A publish after every subscriber has left must not panic (send on a closed channel) and must
	// still commit.
	id := nf.publish(adminCookie, adminCSRF, "After drop", "the pump survived the disconnects")

	// And a fresh subscriber still receives a subsequent publish — the broadcaster kept working.
	// The fresh stream replays earlier notifications on connect, so wait past those for the live one.
	s := nf.openStream(playerCookie)
	defer s.close()
	next := nf.publish(adminCookie, adminCSRF, "Live again", "delivered to a new subscriber")
	s.waitFor(t, next, 3*time.Second)
	_ = id

	// The dropped streams must not have leaked goroutines. Allow slack for the reader goroutines
	// and runtime workers; a per-connection leak of ten would blow well past this.
	time.Sleep(200 * time.Millisecond)
	if after := runtime.NumGoroutine(); after > before+8 {
		t.Fatalf("goroutine count grew from %d to %d: a subscriber likely leaked", before, after)
	}
}
