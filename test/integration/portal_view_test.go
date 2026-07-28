//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/starvy/flagfish/internal/domain/account"
)

func (f *apiFix) instancePortalView() string {
	f.t.Helper()
	res, body := f.do(http.MethodGet, "/api/v1/instance", nil)
	if res.StatusCode != http.StatusOK {
		f.t.Fatalf("instance: got %d (%s)", res.StatusCode, body)
	}
	var out struct {
		PortalView string `json:"portal_view"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		f.t.Fatalf("decode instance: %v (%s)", err, body)
	}
	return out.PortalView
}

func (f *apiFix) adminConfigPortalView(auth ...func(*http.Request)) string {
	f.t.Helper()
	res, body := f.do(http.MethodGet, "/api/v1/admin/config", nil, auth...)
	if res.StatusCode != http.StatusOK {
		f.t.Fatalf("admin config: got %d (%s)", res.StatusCode, body)
	}
	var out struct {
		PortalView string `json:"portal_view"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		f.t.Fatalf("decode admin config: %v (%s)", err, body)
	}
	return out.PortalView
}

// The SPA decides which board to draw before anyone logs in, so the view rides on the anonymous
// branding endpoint. An instance that never set one serves the standard board.
func TestPortalViewDefaultsAndIsPubliclyVisible(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers)
	cookie, csrf, _ := f.admin("root", "root@example.com")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	if got := f.instancePortalView(); got != "standard" {
		t.Fatalf("default /instance portal_view = %q, want standard", got)
	}
	if got := f.adminConfigPortalView(auth...); got != "standard" {
		t.Fatalf("default admin config portal_view = %q, want standard", got)
	}
}

func TestPortalViewRoundTripsThroughAdminConfig(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers)
	cookie, csrf, _ := f.admin("root", "root@example.com")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	res, body := f.do(http.MethodPatch, "/api/v1/admin/config", map[string]any{"portal_view": "globe"}, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("patch portal_view: got %d (%s)", res.StatusCode, body)
	}
	var patched struct {
		PortalView string `json:"portal_view"`
	}
	if err := json.Unmarshal(body, &patched); err != nil {
		t.Fatalf("decode patch response: %v (%s)", err, body)
	}
	if patched.PortalView != "globe" {
		t.Fatalf("patch echoed portal_view = %q, want globe", patched.PortalView)
	}

	// The anonymous endpoint is the one the client actually reads, so that is where it has to land.
	if got := f.instancePortalView(); got != "globe" {
		t.Fatalf("/instance portal_view = %q after the patch, want globe", got)
	}

	// And back again: switching views is not a one-way door.
	if res, body = f.do(http.MethodPatch, "/api/v1/admin/config",
		map[string]any{"portal_view": "standard"}, auth...); res.StatusCode != http.StatusOK {
		t.Fatalf("patch back: got %d (%s)", res.StatusCode, body)
	}
	if got := f.instancePortalView(); got != "standard" {
		t.Fatalf("/instance portal_view = %q after switching back, want standard", got)
	}
}

// A view names client code. An unknown one is refused at the write, so an operator finds out
// immediately instead of shipping a portal that renders nothing.
func TestPortalViewRejectsUnknownValues(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers)
	cookie, csrf, _ := f.admin("root", "root@example.com")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	for _, bad := range []string{"parabola", "Globe", "GLOBE", "", "globe,standard"} {
		t.Run(bad, func(t *testing.T) {
			res, body := f.do(http.MethodPatch, "/api/v1/admin/config", map[string]any{"portal_view": bad}, auth...)
			if res.StatusCode != http.StatusUnprocessableEntity {
				t.Fatalf("patch portal_view=%q: got %d, want 422 (%s)", bad, res.StatusCode, body)
			}
		})
	}

	// None of the refusals moved the served value.
	if got := f.instancePortalView(); got != "standard" {
		t.Fatalf("/instance portal_view = %q after rejected writes, want standard", got)
	}
}
