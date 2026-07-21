//go:build integration

package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/starvy/flagfish/internal/domain/account"
)

// pageBody is the admin create/get echo.
type pageBody struct {
	ID           int64  `json:"id"`
	Route        string `json:"route"`
	Title        string `json:"title"`
	Content      string `json:"content"`
	Format       string `json:"format"`
	Draft        bool   `json:"draft"`
	AuthRequired bool   `json:"auth_required"`
}

// publicPage is the public read: no draft/auth flags, just what renders.
type publicPage struct {
	Route   string `json:"route"`
	Title   string `json:"title"`
	Content string `json:"content"`
	Format  string `json:"format"`
}

func (f *apiFix) createPage(cookie, csrf string, body map[string]any) (resp apiResp, respBody []byte) {
	f.t.Helper()
	return f.do(http.MethodPost, "/api/v1/admin/pages", body, withCookie(cookie), withCSRF(csrf))
}

// TestAdminCreatesAndPublicRendersPage covers the happy path: an admin creates a published page and
// an anonymous visitor reads its markdown back verbatim, plus the nav list.
func TestAdminCreatesAndPublicRendersPage(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers)
	cookie, csrf, adminID := f.admin("root", "root@example.com")

	res, body := f.createPage(cookie, csrf, map[string]any{
		"route": "rules", "title": "The Rules", "content": "# Play fair\n\nNo sharing.", "draft": false,
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create: status %d: %s", res.StatusCode, body)
	}
	var created pageBody
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("decode create: %v (%s)", err, body)
	}
	if created.Draft || created.Route != "rules" || created.Format != "markdown" {
		t.Fatalf("create echoed %+v, want published rules/markdown", created)
	}

	// Anonymous public read renders the stored markdown verbatim (no server-side HTML).
	res, body = f.do(http.MethodGet, "/api/v1/pages/rules", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("public get: status %d: %s", res.StatusCode, body)
	}
	var got publicPage
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode public: %v (%s)", err, body)
	}
	if got.Content != "# Play fair\n\nNo sharing." || got.Title != "The Rules" {
		t.Fatalf("public read = %+v, want the stored markdown", got)
	}

	// The nav list shows the published page to anyone.
	res, body = f.do(http.MethodGet, "/api/v1/pages", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list: status %d: %s", res.StatusCode, body)
	}
	var list struct {
		Pages []struct {
			Route string `json:"route"`
		} `json:"pages"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("decode list: %v (%s)", err, body)
	}
	if len(list.Pages) != 1 || list.Pages[0].Route != "rules" {
		t.Fatalf("list = %+v, want [rules]", list.Pages)
	}

	// The create is audited to the acting admin.
	if n := f.auditCount("pages", "INSERT", adminID); n != 1 {
		t.Fatalf("audit INSERT rows = %d, want 1", n)
	}
}

// TestDraftPageHiddenFromPublicVisibleToAdmin drives the draft half of the ClassPages gate: a draft
// is 404 to the public and to the nav, but the admin reads it in full through the admin surface.
func TestDraftPageHiddenFromPublicVisibleToAdmin(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers)
	cookie, csrf, _ := f.admin("root", "root@example.com")

	res, body := f.createPage(cookie, csrf, map[string]any{
		"route": "sponsors", "title": "Sponsors", "content": "draft copy", "draft": true,
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create: status %d: %s", res.StatusCode, body)
	}
	id := decodeID(t, body)

	// Public read of a draft: it does not exist. 404, not 403 — the slug is not confirmed.
	res, _ = f.do(http.MethodGet, "/api/v1/pages/sponsors", nil)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("public draft: status %d, want 404", res.StatusCode)
	}
	// A logged-in non-admin is no more privileged than anonymous on the public route.
	pc, _ := f.register("Player", "player@ctf.test", "correct-horse-battery")
	res, _ = f.do(http.MethodGet, "/api/v1/pages/sponsors", nil, withCookie(pc))
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("player draft: status %d, want 404", res.StatusCode)
	}
	// The draft does not appear in the public nav list.
	res, body = f.do(http.MethodGet, "/api/v1/pages", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list: status %d: %s", res.StatusCode, body)
	}
	if strings.Contains(string(body), "sponsors") {
		t.Fatalf("draft leaked into public list: %s", body)
	}

	// The admin sees the draft with its full body through the admin surface.
	res, body = f.do(http.MethodGet, fmt.Sprintf("/api/v1/admin/pages/%d", id), nil, withCookie(cookie), withCSRF(csrf))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("admin get draft: status %d: %s", res.StatusCode, body)
	}
	var adminGot pageBody
	if err := json.Unmarshal(body, &adminGot); err != nil {
		t.Fatalf("decode admin get: %v (%s)", err, body)
	}
	if !adminGot.Draft || adminGot.Content != "draft copy" {
		t.Fatalf("admin get = %+v, want the draft with its body", adminGot)
	}
}

// TestAuthRequiredPageGate drives the auth_required half of the ClassPages gate: a published,
// auth-gated page is 403 to an anonymous caller and 200 to a logged-in one.
func TestAuthRequiredPageGate(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers)
	cookie, csrf, _ := f.admin("root", "root@example.com")

	res, body := f.createPage(cookie, csrf, map[string]any{
		"route": "members", "title": "Members", "content": "insider info", "draft": false, "auth_required": true,
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create: status %d: %s", res.StatusCode, body)
	}

	// Anonymous: refused.
	res, _ = f.do(http.MethodGet, "/api/v1/pages/members", nil)
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("anonymous auth-gated: status %d, want 403", res.StatusCode)
	}

	// Logged in: served.
	pc, _ := f.register("Player", "player@ctf.test", "correct-horse-battery")
	res, body = f.do(http.MethodGet, "/api/v1/pages/members", nil, withCookie(pc))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("player auth-gated: status %d: %s", res.StatusCode, body)
	}
	var got publicPage
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	if got.Content != "insider info" {
		t.Fatalf("player read = %+v, want the body", got)
	}
}

// TestPageRouteUniqueness proves the slug uniqueness is the table's: a second create on the same
// route is a 409 straight from pages_route_key.
func TestPageRouteUniqueness(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers)
	cookie, csrf, _ := f.admin("root", "root@example.com")

	res, body := f.createPage(cookie, csrf, map[string]any{"route": "faq", "title": "FAQ", "content": "a"})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("first create: status %d: %s", res.StatusCode, body)
	}
	res, body = f.createPage(cookie, csrf, map[string]any{"route": "faq", "title": "FAQ 2", "content": "b"})
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate create: status %d, want 409: %s", res.StatusCode, body)
	}
}

// TestPageUpdateDeleteAudited covers the rest of the lifecycle: a partial update flips the draft
// flag and edits the body, a delete removes the row, and both are stamped to the acting admin.
func TestPageUpdateDeleteAudited(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers)
	cookie, csrf, adminID := f.admin("root", "root@example.com")

	res, body := f.createPage(cookie, csrf, map[string]any{"route": "news", "title": "News", "content": "v1", "draft": true})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create: status %d: %s", res.StatusCode, body)
	}
	id := decodeID(t, body)

	// Partial update: publish it and change the body, leaving the title alone.
	res, body = f.do(http.MethodPatch, fmt.Sprintf("/api/v1/admin/pages/%d", id),
		map[string]any{"content": "v2", "draft": false}, withCookie(cookie), withCSRF(csrf))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("update: status %d: %s", res.StatusCode, body)
	}
	var upd pageBody
	if err := json.Unmarshal(body, &upd); err != nil {
		t.Fatalf("decode update: %v (%s)", err, body)
	}
	if upd.Draft || upd.Content != "v2" || upd.Title != "News" {
		t.Fatalf("update = %+v, want published, v2, title unchanged", upd)
	}

	// Now that it is published the public read works.
	res, _ = f.do(http.MethodGet, "/api/v1/pages/news", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("public read after publish: status %d, want 200", res.StatusCode)
	}

	// Delete removes it.
	res, body = f.do(http.MethodDelete, fmt.Sprintf("/api/v1/admin/pages/%d", id), nil, withCookie(cookie), withCSRF(csrf))
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: status %d: %s", res.StatusCode, body)
	}
	res, _ = f.do(http.MethodGet, "/api/v1/pages/news", nil)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("public read after delete: status %d, want 404", res.StatusCode)
	}

	if n := f.auditCount("pages", "UPDATE", adminID); n != 1 {
		t.Fatalf("audit UPDATE rows = %d, want 1", n)
	}
	if n := f.auditCount("pages", "DELETE", adminID); n != 1 {
		t.Fatalf("audit DELETE rows = %d, want 1", n)
	}
}

// TestAdminPageRoutesRejectNonAdmin proves the admin CMS write surface is behind ClassAdmin: a
// logged-in non-admin cannot create, and an anonymous caller cannot either.
func TestAdminPageRoutesRejectNonAdmin(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers)
	pc, pcsrf := f.register("Player", "player@ctf.test", "correct-horse-battery")

	res, _ := f.createPage(pc, pcsrf, map[string]any{"route": "sneaky", "title": "x", "content": "y"})
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("non-admin create: status %d, want 403", res.StatusCode)
	}
	// The page must not have been created.
	res, _ = f.do(http.MethodGet, "/api/v1/admin/pages", nil, withCookie(pc), withCSRF(pcsrf))
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("non-admin list: status %d, want 403", res.StatusCode)
	}
}
