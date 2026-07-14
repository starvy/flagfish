//go:build e2e

package e2e

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/oapi-codegen/runtime/types"

	"github.com/starvy/flagfish/e2e/client"
)

// Wiring resolved once in TestMain from the environment.
var (
	baseURL   string // e.g. http://localhost:8000
	apiBase   string // baseURL + /api/v1
	adminBase string // baseURL + /api/v1/admin
	dbURL     string // out-of-band setup only (never assertions)
	userMode  string // users | teams — the mode the target instance was booted in

	// admin is registered and SQL-promoted once; there is no admin-bootstrap endpoint.
	admin *user

	// uniq keeps generated emails and names distinct within a run.
	uniq atomic.Int64
)

func TestMain(m *testing.M) {
	baseURL = envOr("E2E_BASE_URL", "http://localhost:8000")
	apiBase = baseURL + "/api/v1"
	adminBase = apiBase + "/admin"
	dbURL = os.Getenv("E2E_DATABASE_URL")
	userMode = envOr("E2E_USER_MODE", "users")

	ctx := context.Background()
	if err := waitHealthy(ctx, 90*time.Second); err != nil {
		die(fmt.Errorf("server never became healthy at %s: %w", baseURL, err))
	}
	if dbURL == "" {
		die(errors.New("E2E_DATABASE_URL is required: the suite marks the instance set up and promotes the first admin out of band"))
	}
	if err := bootstrap(ctx); err != nil {
		die(fmt.Errorf("bootstrap: %w", err))
	}
	os.Exit(m.Run())
}

func die(err error) {
	fmt.Fprintln(os.Stderr, "e2e:", err)
	os.Exit(1)
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

// newJar builds a cookie jar; the only error path is invalid options, which nil is not.
func newJar() http.CookieJar {
	jar, err := cookiejar.New(nil)
	if err != nil {
		die(fmt.Errorf("cookiejar: %w", err))
	}
	return jar
}

// ---------------------------------------------------------------- bootstrap

// bootstrap marks the fresh instance set up and creates the first admin. Both are
// out-of-band by necessity: there is no self-serve setup route and no admin-bootstrap
// endpoint. Config is cached behind an atomic snapshot the running server only refreshes
// on NOTIFY, so seedInstance signals config_changed and register is retried until the
// server has reloaded and setup=true has taken effect.
func bootstrap(ctx context.Context) error {
	if err := seedInstance(ctx); err != nil {
		return err
	}

	a := newUser("admin")
	// Registration is gated behind setup; poll until the config reload has landed.
	deadline := time.Now().Add(30 * time.Second)
	for {
		err := a.register(ctx)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("admin register never succeeded (setup not propagating?): %w", err)
		}
		time.Sleep(500 * time.Millisecond)
	}

	if err := execSQL(ctx, `UPDATE users SET role = 'admin' WHERE email = $1`, a.email); err != nil {
		return fmt.Errorf("promote admin: %w", err)
	}
	admin = a
	return nil
}

func seedInstance(ctx context.Context) error {
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer conn.Close(ctx)

	upserts := []struct {
		sql string
		arg string
	}{
		{`INSERT INTO instance (user_mode, version) VALUES ($1, 'e2e') ON CONFLICT (id) DO NOTHING`, userMode},
		{`INSERT INTO config (key, value) VALUES ('user_mode', $1) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, userMode},
		{`INSERT INTO config (key, value) VALUES ('setup', $1) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, "true"},
	}
	for _, u := range upserts {
		if _, err := conn.Exec(ctx, u.sql, u.arg); err != nil {
			return fmt.Errorf("seed: %w", err)
		}
	}
	// Wake the running server's config watcher so setup=true takes effect without a restart.
	if _, err := conn.Exec(ctx, `SELECT pg_notify('config_changed', '')`); err != nil {
		return fmt.Errorf("notify: %w", err)
	}
	return nil
}

func execSQL(ctx context.Context, sql string, args ...any) error {
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, sql, args...); err != nil {
		return fmt.Errorf("exec: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------- user

// user is one browser: a cookie jar, the generated client, and the session's CSRF token.
// A user with a token set authenticates by bearer instead and carries no cookies.
type user struct {
	name, email, password string
	userID                int64
	csrf                  string
	token                 string
	httpc                 *http.Client
	api                   *client.ClientWithResponses
}

func newUser(prefix string) *user {
	u := &user{httpc: &http.Client{Jar: newJar(), Timeout: 30 * time.Second}}
	n := uniq.Add(1)
	u.name = fmt.Sprintf("%s_%d_%d", prefix, time.Now().UnixNano(), n)
	u.email = u.name + "@e2e.test"
	u.password = "correct-horse-battery-staple"
	api, err := client.NewClientWithResponses(apiBase,
		client.WithHTTPClient(u.httpc),
		client.WithRequestEditorFn(u.edit))
	if err != nil {
		die(fmt.Errorf("build client: %w", err))
	}
	u.api = api
	return u
}

// edit injects the credential the request needs: a bearer token for token auth, and the
// double-submit CSRF header for cookie-authenticated writes.
func (u *user) edit(_ context.Context, req *http.Request) error {
	if u.token != "" {
		req.Header.Set("Authorization", "Bearer "+u.token)
	}
	if u.csrf != "" && !safeMethod(req.Method) {
		req.Header.Set("CSRF-Token", u.csrf)
	}
	return nil
}

func safeMethod(m string) bool {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	return false
}

func (u *user) register(ctx context.Context) error {
	resp, err := u.api.RegisterWithResponse(ctx, client.RegisterInputBody{
		Name: u.name, Email: types.Email(u.email), Password: u.password,
	})
	if err != nil {
		return err
	}
	if resp.JSON200 == nil {
		return fmt.Errorf("register: status %d: %s", resp.StatusCode(), resp.Body)
	}
	u.userID = resp.JSON200.UserId
	u.csrf = resp.JSON200.CsrfToken
	return nil
}

// mustRegister returns a fresh, logged-in normal user.
func mustRegister(t *testing.T) *user {
	t.Helper()
	u := newUser("user")
	if err := u.register(context.Background()); err != nil {
		t.Fatalf("register: %v", err)
	}
	return u
}

// anonUser is a client with a cookie jar but no identity yet — for login/registration flows.
func anonUser() *user {
	u := &user{httpc: &http.Client{Jar: newJar(), Timeout: 30 * time.Second}}
	api, err := client.NewClientWithResponses(apiBase,
		client.WithHTTPClient(u.httpc),
		client.WithRequestEditorFn(u.edit))
	if err != nil {
		die(fmt.Errorf("build client: %w", err))
	}
	u.api = api
	return u
}

func (u *user) login(ctx context.Context, email, password string) error {
	resp, err := u.api.LoginWithResponse(ctx, client.LoginInputBody{
		Email: types.Email(email), Password: password,
	})
	if err != nil {
		return err
	}
	if resp.JSON200 == nil {
		return fmt.Errorf("login: status %d: %s", resp.StatusCode(), resp.Body)
	}
	u.userID = resp.JSON200.UserId
	u.csrf = resp.JSON200.CsrfToken
	u.email, u.password = email, password
	return nil
}

// tokenClient is a bearer-authenticated client for the given token: no cookies, no CSRF.
func tokenClient(t *testing.T, token string) *user {
	t.Helper()
	u := &user{token: token, httpc: &http.Client{Timeout: 30 * time.Second}}
	api, err := client.NewClientWithResponses(apiBase,
		client.WithHTTPClient(u.httpc),
		client.WithRequestEditorFn(u.edit))
	if err != nil {
		t.Fatalf("build token client: %v", err)
	}
	u.api = api
	return u
}

// ---------------------------------------------------------------- raw requests

// apiResp is a decoded raw HTTP response, used for the admin API and other endpoints the
// generated public client does not cover.
type apiResp struct {
	code int
	body []byte
	hdr  http.Header
}

func (u *user) do(ctx context.Context, method, url string, body any) (*apiResp, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, r)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if eerr := u.edit(ctx, req); eerr != nil {
		return nil, eerr
	}
	resp, err := u.httpc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return &apiResp{code: resp.StatusCode, body: b, hdr: resp.Header}, nil
}

// adminReq calls the admin API (/api/v1/admin + path) as this user.
func (u *user) adminReq(t *testing.T, method, path string, body any) *apiResp {
	t.Helper()
	r, err := u.do(context.Background(), method, adminBase+path, body)
	if err != nil {
		t.Fatalf("admin %s %s: %v", method, path, err)
	}
	return r
}

// publicReq calls the public API (/api/v1 + path) as this user, for the handful of public
// endpoints outside the generated client's typed surface.
func (u *user) publicReq(t *testing.T, method, path string, body any) *apiResp {
	t.Helper()
	r, err := u.do(context.Background(), method, apiBase+path, body)
	if err != nil {
		t.Fatalf("public %s %s: %v", method, path, err)
	}
	return r
}

func (r *apiResp) require(t *testing.T, want int) *apiResp {
	t.Helper()
	if r.code != want {
		t.Fatalf("want status %d, got %d: %s", want, r.code, r.body)
	}
	return r
}

func (r *apiResp) decode(t *testing.T, out any) {
	t.Helper()
	if err := json.Unmarshal(r.body, out); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, r.body)
	}
}

// problemType reads the policy reason off an error body. Raw chi denials carry it as the
// `type` suffix (…/errors/banned); Huma operation denials carry it in `detail`.
func (r *apiResp) problemType(t *testing.T) string {
	t.Helper()
	var p struct {
		Type   string `json:"type"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(r.body, &p); err != nil {
		return ""
	}
	if i := strings.LastIndex(p.Type, "/"); i >= 0 && p.Type != "about:blank" {
		return p.Type[i+1:]
	}
	return p.Detail
}

// ---------------------------------------------------------------- admin fixtures

func adminCreateChallenge(t *testing.T, name, category string, value int) int64 {
	t.Helper()
	var out struct {
		ID    int64  `json:"id"`
		State string `json:"state"`
	}
	admin.adminReq(t, http.MethodPost, "/challenges", map[string]any{
		"name": name, "category": category, "value": value,
	}).require(t, http.StatusCreated).decode(t, &out)
	return out.ID
}

func adminAddStaticFlag(t *testing.T, chalID int64, content string) int64 {
	t.Helper()
	var out struct {
		ID int64 `json:"id"`
	}
	admin.adminReq(t, http.MethodPost, fmt.Sprintf("/challenges/%d/flags", chalID), map[string]any{
		"type": "static", "content": content,
	}).require(t, http.StatusCreated).decode(t, &out)
	return out.ID
}

func adminAddHint(t *testing.T, chalID int64, content string, cost int) int64 {
	t.Helper()
	var out struct {
		ID int64 `json:"id"`
	}
	admin.adminReq(t, http.MethodPost, fmt.Sprintf("/challenges/%d/hints", chalID), map[string]any{
		"content": content, "cost": cost,
	}).require(t, http.StatusCreated).decode(t, &out)
	return out.ID
}

// solveChallenge submits the given flag and returns the attempt outcome.
func (u *user) solve(t *testing.T, chalID int64, flag string) *client.AttemptOutputBody {
	t.Helper()
	resp, err := u.api.AttemptWithResponse(context.Background(), chalID, client.AttemptInputBody{Flag: flag})
	if err != nil {
		t.Fatalf("attempt: %v", err)
	}
	if resp.JSON200 == nil {
		t.Fatalf("attempt: status %d: %s", resp.StatusCode(), resp.Body)
	}
	return resp.JSON200
}

// ---------------------------------------------------------------- misc helpers

func waitHealthy(ctx context.Context, within time.Duration) error {
	deadline := time.Now().Add(within)
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/healthz", http.NoBody)
		if err != nil {
			return err
		}
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		if time.Now().After(deadline) {
			if err != nil {
				return err
			}
			return errors.New("timed out")
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// setConfig PATCHes admin config and returns the resulting config body. It goes through the
// API on purpose: a direct table write would not wake the server's cached snapshot.
func setConfig(t *testing.T, patch map[string]any) map[string]any {
	t.Helper()
	var out map[string]any
	admin.adminReq(t, http.MethodPatch, "/config", patch).require(t, http.StatusOK).decode(t, &out)
	return out
}

func path(format string, a ...any) string { return fmt.Sprintf(format, a...) }

// uploadFile posts a multipart file to a challenge as admin and returns the raw response.
func uploadFile(t *testing.T, chalID int64, filename string, content []byte) *apiResp {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("multipart: %v", err)
	}
	if _, err = fw.Write(content); err != nil {
		t.Fatalf("multipart write: %v", err)
	}
	if err = w.Close(); err != nil {
		t.Fatalf("multipart close: %v", err)
	}

	url := fmt.Sprintf("%s/challenges/%d/files", adminBase, chalID)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, &buf)
	if err != nil {
		t.Fatalf("upload request: %v", err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	if admin.csrf != "" {
		req.Header.Set("CSRF-Token", admin.csrf)
	}
	resp, err := admin.httpc.Do(req)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("upload read: %v", err)
	}
	return &apiResp{code: resp.StatusCode, body: b, hdr: resp.Header}
}

// sseNotification opens the SSE stream and waits for a notification whose title matches,
// returning its JSON content. It fails the test on timeout.
func (u *user) sseNotification(t *testing.T, wantTitle string, within time.Duration) NotificationEvent {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), within)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBase+"/notifications/stream", http.NoBody)
	if err != nil {
		t.Fatalf("sse request: %v", err)
	}
	if eerr := u.edit(ctx, req); eerr != nil {
		t.Fatalf("sse edit: %v", eerr)
	}
	resp, err := u.httpc.Do(req)
	if err != nil {
		t.Fatalf("sse connect: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body) //nolint:errcheck // best-effort body for the failure message
		t.Fatalf("sse status %d: %s", resp.StatusCode, b)
	}

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		var ev NotificationEvent
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			continue
		}
		if ev.Title == wantTitle {
			return ev
		}
	}
	t.Fatalf("sse: did not receive notification %q within %s (err=%v)", wantTitle, within, sc.Err())
	return NotificationEvent{}
}

// NotificationEvent mirrors the SSE `data:` payload (the notificationBody the server writes).
type NotificationEvent struct {
	ID      int64     `json:"id"`
	Title   string    `json:"title"`
	Content string    `json:"content"`
	Date    time.Time `json:"date"`
}
