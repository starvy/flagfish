//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"github.com/starvy/flagfish/internal/domain/account"
)

func TestTokenLifecycle(t *testing.T) {
	f := newAPI(t, account.ModeUsers)
	cookie, csrf := f.register("Ada", "ada@ctf.test", "correct horse battery")

	// Mint a token; the plaintext comes back exactly once, here.
	res, body := f.do(http.MethodPost, "/api/v1/tokens", map[string]any{
		"description": "ci runner",
	}, withCookie(cookie), withCSRF(csrf))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("create token: status %d: %s", res.StatusCode, body)
	}
	var created struct {
		ID    int64  `json:"id"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("decode created token: %v (%s)", err, body)
	}
	if created.Token == "" {
		t.Fatal("create token returned an empty plaintext")
	}

	// The plaintext is a working Bearer credential — and token auth is CSRF-exempt.
	res, body = f.do(http.MethodGet, "/api/v1/me", nil, withToken(created.Token))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("me via bearer token: status %d: %s", res.StatusCode, body)
	}

	// The token shows up in the caller's own listing.
	res, body = f.do(http.MethodGet, "/api/v1/tokens", nil, withCookie(cookie))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list tokens: status %d: %s", res.StatusCode, body)
	}
	var listed struct {
		Tokens []struct {
			ID int64 `json:"id"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(body, &listed); err != nil {
		t.Fatalf("decode token list: %v (%s)", err, body)
	}
	// The listing is metadata only: the plaintext is never stored, so it can never be echoed back.
	if bytes.Contains(body, []byte(created.Token)) {
		t.Fatalf("token listing leaked the plaintext secret: %s", body)
	}
	found := false
	for _, tok := range listed.Tokens {
		if tok.ID == created.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("listing does not contain created token %d: %s", created.ID, body)
	}

	// Revoke it, then a second revoke is a 404 — the row is gone.
	path := "/api/v1/tokens/" + strconv.FormatInt(created.ID, 10)
	res, body = f.do(http.MethodDelete, path, nil, withCookie(cookie), withCSRF(csrf))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("delete token: status %d: %s", res.StatusCode, body)
	}
	res, _ = f.do(http.MethodDelete, path, nil, withCookie(cookie), withCSRF(csrf))
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("second delete must be 404, got %d", res.StatusCode)
	}
}

func TestCreateTokenNeedsCSRF(t *testing.T) {
	f := newAPI(t, account.ModeUsers)
	cookie, _ := f.register("Ada", "ada@ctf.test", "correct horse battery")

	res, _ := f.do(http.MethodPost, "/api/v1/tokens", map[string]any{
		"description": "no csrf",
	}, withCookie(cookie))
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("cookie write without CSRF must be 403, got %d", res.StatusCode)
	}
}
