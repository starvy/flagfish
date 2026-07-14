//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/starvy/flagfish/internal/accounts"
	"github.com/starvy/flagfish/internal/adminops"
	"github.com/starvy/flagfish/internal/board"
	"github.com/starvy/flagfish/internal/catalog"
	"github.com/starvy/flagfish/internal/config"
	"github.com/starvy/flagfish/internal/domain/account"
	filesvc "github.com/starvy/flagfish/internal/files"
	"github.com/starvy/flagfish/internal/gameplay"
	"github.com/starvy/flagfish/internal/httpapi"
	"github.com/starvy/flagfish/internal/storage"
)

// testS3Config reads the MinIO connection the suite runs against. It is path-style over plain HTTP,
// matching compose.yaml's minio-test.
func testS3Config(t *testing.T) storage.S3Config {
	t.Helper()
	ep := os.Getenv("TEST_S3_ENDPOINT")
	if ep == "" {
		t.Fatal("TEST_S3_ENDPOINT is not set — run `task test-integration` (brings up minio-test)")
	}
	return storage.S3Config{
		Endpoint:  ep,
		Bucket:    envOrDefault("TEST_S3_BUCKET", "flagfish-test"),
		Region:    "us-east-1",
		AccessKey: envOrDefault("TEST_S3_ACCESS_KEY", "flagfishtest"),
		SecretKey: envOrDefault("TEST_S3_SECRET_KEY", "flagfishtest123"),
		UseSSL:    false,
		PathStyle: true,
	}
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// filesFix is the base harness wired with the object-storage-backed files service, which the shared
// harness deliberately leaves out.
type filesFix struct {
	*apiFix
	store storage.Store
	s3    storage.S3Config
}

func newFilesAPI(t *testing.T, mode account.Mode) *filesFix {
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

	s3cfg := testS3Config(t)
	ensureBucketReady(t, ctx, s3cfg)
	store, err := storage.NewS3Store(s3cfg)
	if err != nil {
		t.Fatalf("store: %v", err)
	}

	acct := accounts.NewService(pool, mode, log)
	srv := httpapi.New(httpapi.Options{
		Config:   cfg,
		Auth:     acct,
		Limiter:  accounts.NewLimiter(pool, 1000, 60_000_000_000),
		Log:      log,
		Accounts: acct,
		Gameplay: gameplay.New(pool, stubInserter{}, mode),
		Catalog:  catalog.New(pool),
		Board:    board.New(pool, mode),
		AdminOps: adminops.New(pool),
		Files:    filesvc.New(pool, store, log),
	})

	ts := httptest.NewServer(srv.Router)
	t.Cleanup(ts.Close)

	f := &apiFix{
		t: t, pool: pool, acct: acct, server: ts,
		client: &http.Client{
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
	return &filesFix{apiFix: f, store: store, s3: s3cfg}
}

// ensureBucketReady creates the test bucket, retrying while MinIO finishes coming up.
func ensureBucketReady(t *testing.T, ctx context.Context, cfg storage.S3Config) {
	t.Helper()
	var err error
	for range 40 {
		if err = storage.EnsureBucket(ctx, cfg); err == nil {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("object storage never became ready: %v", err)
}

// upload posts a multipart file to the given challenge and returns the drained response and body.
func (f *apiFix) upload(challengeID int64, field, filename string, content []byte, mut ...func(*http.Request)) (resp apiResp, respBody []byte) {
	f.t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if field != "" {
		part, err := w.CreateFormFile(field, filename)
		if err != nil {
			f.t.Fatalf("form file: %v", err)
		}
		if _, err := part.Write(content); err != nil {
			f.t.Fatalf("write part: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		f.t.Fatalf("close writer: %v", err)
	}

	path := fmt.Sprintf("/api/v1/admin/challenges/%d/files", challengeID)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, f.server.URL+path, &buf)
	if err != nil {
		f.t.Fatalf("request: %v", err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	for _, m := range mut {
		m(req)
	}
	res, err := f.client.Do(req)
	if err != nil {
		f.t.Fatalf("do upload: %v", err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		f.t.Fatalf("read body: %v", err)
	}
	return apiResp{StatusCode: res.StatusCode, Cookies: res.Cookies()}, body
}

// rawResp exposes the response headers a download must be checked against, which the JSON-oriented
// apiResp does not carry.
type rawResp struct {
	StatusCode int
	Header     http.Header
}

// doRaw issues a bodyless request and returns the status, headers, and drained body — enough to
// assert a download's bytes and its Content-Disposition.
func (f *apiFix) doRaw(method, path string, mut ...func(*http.Request)) (resp rawResp, respBody []byte) {
	f.t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, f.server.URL+path, http.NoBody)
	if err != nil {
		f.t.Fatalf("request: %v", err)
	}
	for _, m := range mut {
		m(req)
	}
	res, err := f.client.Do(req)
	if err != nil {
		f.t.Fatalf("do: %v", err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		f.t.Fatalf("read body: %v", err)
	}
	return rawResp{StatusCode: res.StatusCode, Header: res.Header}, body
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestFileUploadDownloadRoundTrip(t *testing.T) {
	f := newFilesAPI(t, account.ModeUsers)
	cookie, csrf, adminID := f.admin("root", "root@files.test")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	chID := f.seedChallenge("pwn", "binary", 100)
	content := []byte("the exact bytes of a challenge artifact \x00\x01\x02 payload")

	res, body := f.upload(chID, "file", "artifact.bin", content, auth...)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("upload: got %d, want 201 (%s)", res.StatusCode, body)
	}
	fileID := decodeID(t, body)

	// The upload and its audit row commit together.
	if got := f.auditCount("files", "INSERT", adminID); got == 0 {
		t.Error("no INSERT audit row for the uploaded file")
	}

	// Download round-trips the exact bytes.
	dl, dlBody := f.doRaw(http.MethodGet, fmt.Sprintf("/api/v1/files/%d", fileID), withCookie(cookie))
	if dl.StatusCode != http.StatusOK {
		t.Fatalf("download: got %d, want 200", dl.StatusCode)
	}
	if !bytes.Equal(dlBody, content) {
		t.Fatalf("download bytes differ: got %d bytes, want %d", len(dlBody), len(content))
	}
	if cd := dl.Header.Get("Content-Disposition"); cd == "" || !bytes.Contains([]byte(cd), []byte("artifact.bin")) {
		t.Errorf("Content-Disposition missing filename: %q", cd)
	}
}

func TestFileDownloadHiddenChallengeIs404ForNonAdmin(t *testing.T) {
	f := newFilesAPI(t, account.ModeUsers)
	cookie, csrf, _ := f.admin("root", "root@files.test")
	adminAuth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	chID := f.seedHiddenChallenge("secret", "misc", 100)
	content := []byte("hidden challenge artifact bytes")
	res, body := f.upload(chID, "file", "secret.zip", content, adminAuth...)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("upload to hidden challenge: got %d, want 201 (%s)", res.StatusCode, body)
	}
	fileID := decodeID(t, body)

	// A plain user cannot see the file of a hidden challenge — it 404s, not 403, so the file's
	// existence is not disclosed.
	userCookie, _ := f.register("mallory", "mallory@files.test", "correct-horse-battery")
	dl, _ := f.doRaw(http.MethodGet, fmt.Sprintf("/api/v1/files/%d", fileID), withCookie(userCookie))
	if dl.StatusCode != http.StatusNotFound {
		t.Fatalf("hidden file for non-admin: got %d, want 404", dl.StatusCode)
	}

	// The admin can.
	dlAdmin, adminBody := f.doRaw(http.MethodGet, fmt.Sprintf("/api/v1/files/%d", fileID), withCookie(cookie))
	if dlAdmin.StatusCode != http.StatusOK {
		t.Fatalf("hidden file for admin: got %d, want 200", dlAdmin.StatusCode)
	}
	if !bytes.Equal(adminBody, content) {
		t.Error("admin download of hidden file returned wrong bytes")
	}
}

func TestFileUploadDedupesInBucket(t *testing.T) {
	f := newFilesAPI(t, account.ModeUsers)
	cookie, csrf, _ := f.admin("root", "root@files.test")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	chID := f.seedChallenge("dedupe", "misc", 100)
	content := []byte("identical content uploaded twice under two names")
	sha := sha256Hex(content)

	res1, body1 := f.upload(chID, "file", "first.bin", content, auth...)
	if res1.StatusCode != http.StatusCreated {
		t.Fatalf("first upload: got %d (%s)", res1.StatusCode, body1)
	}
	res2, body2 := f.upload(chID, "file", "second.bin", content, auth...)
	if res2.StatusCode != http.StatusCreated {
		t.Fatalf("second upload: got %d (%s)", res2.StatusCode, body2)
	}

	// Two distinct rows (different names), but one object in the bucket — the content is deduped.
	if id1, id2 := decodeID(t, body1), decodeID(t, body2); id1 == id2 {
		t.Errorf("expected distinct file rows for distinct names, both were %d", id1)
	}
	keys, err := storage.ObjectKeys(context.Background(), f.s3, sha)
	if err != nil {
		t.Fatalf("list objects: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("expected exactly one stored object for the content, got %d: %v", len(keys), keys)
	}

	// The same content under the same name is fully idempotent: one row.
	res3, body3 := f.upload(chID, "file", "first.bin", content, auth...)
	if res3.StatusCode != http.StatusCreated {
		t.Fatalf("repeat upload: got %d (%s)", res3.StatusCode, body3)
	}
	if decodeID(t, body3) != decodeID(t, body1) {
		t.Error("identical name+content did not dedupe to the same row")
	}
}

func TestFileDeleteRemovesObject(t *testing.T) {
	f := newFilesAPI(t, account.ModeUsers)
	cookie, csrf, adminID := f.admin("root", "root@files.test")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	chID := f.seedChallenge("delete", "misc", 100)
	content := []byte("bytes that will be deleted from the bucket")
	sha := sha256Hex(content)

	res, body := f.upload(chID, "file", "gone.bin", content, auth...)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("upload: got %d (%s)", res.StatusCode, body)
	}
	fileID := decodeID(t, body)

	if exists, err := f.store.Stat(context.Background(), sha); err != nil || !exists {
		t.Fatalf("object should exist before delete (exists=%v err=%v)", exists, err)
	}

	del, delBody := f.do(http.MethodDelete, fmt.Sprintf("/api/v1/admin/files/%d", fileID), nil, auth...)
	if del.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: got %d, want 204 (%s)", del.StatusCode, delBody)
	}
	if got := f.auditCount("files", "DELETE", adminID); got == 0 {
		t.Error("no DELETE audit row for the deleted file")
	}

	// The last row referencing the object is gone, so the object is gone too.
	if exists, err := f.store.Stat(context.Background(), sha); err != nil || exists {
		t.Fatalf("object should be removed after delete (exists=%v err=%v)", exists, err)
	}
	// And it 404s.
	dl, _ := f.doRaw(http.MethodGet, fmt.Sprintf("/api/v1/files/%d", fileID), withCookie(cookie))
	if dl.StatusCode != http.StatusNotFound {
		t.Errorf("download after delete: got %d, want 404", dl.StatusCode)
	}
}

func TestFileUploadRejectsNonAdmin(t *testing.T) {
	f := newFilesAPI(t, account.ModeUsers)
	f.admin("root", "root@files.test") // an admin exists, but mallory is not it
	chID := f.seedChallenge("guarded", "misc", 100)

	cookie, csrf := f.register("mallory", "mallory@files.test", "correct-horse-battery")
	res, _ := f.upload(chID, "file", "x.bin", []byte("nope"), withCookie(cookie), withCSRF(csrf))
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("non-admin upload: got %d, want 403", res.StatusCode)
	}
}
