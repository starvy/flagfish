//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/starvy/flagfish/internal/domain/account"
)

// createField creates a custom field via the admin API and returns its id.
func (f *apiFix) createField(auth []func(*http.Request), body map[string]any) int64 {
	f.t.Helper()
	res, rb := f.do(http.MethodPost, "/api/v1/admin/fields", body, auth...)
	if res.StatusCode != http.StatusCreated {
		f.t.Fatalf("create field: status %d: %s", res.StatusCode, rb)
	}
	return decodeID(f.t, rb)
}

// registerWith posts a registration with custom-field answers and returns the raw result.
func (f *apiFix) registerWith(name, email, password string, fields []map[string]any) (apiResp, []byte) {
	f.t.Helper()
	return f.do(http.MethodPost, "/api/v1/register", map[string]any{
		"name": name, "email": email, "password": password, "fields": fields,
	})
}

// meFields decodes the custom fields off a /me (or /me/fields) body.
type meFieldJSON struct {
	ID       int64           `json:"id"`
	Name     string          `json:"name"`
	Required bool            `json:"required"`
	Public   bool            `json:"public"`
	Editable bool            `json:"editable"`
	Value    json.RawMessage `json:"value"`
}

func decodeMeFields(t *testing.T, body []byte) []meFieldJSON {
	t.Helper()
	var v struct {
		Fields []meFieldJSON `json:"fields"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decode fields: %v (%s)", err, body)
	}
	return v.Fields
}

// TestAdminFieldCRUD covers the definition lifecycle and proves each mutation is audited on the
// fields table, attributed to the acting admin.
func TestAdminFieldCRUD(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers)
	cookie, csrf, adminID := f.admin("root", "root@example.com")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	id := f.createField(auth, map[string]any{
		"name": "Affiliation", "applies_to": "user", "field_type": "text",
		"description": "Your school or company", "required": false, "public": true, "editable": true,
	})

	// List shows it.
	res, body := f.do(http.MethodGet, "/api/v1/admin/fields", nil, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list fields: %d (%s)", res.StatusCode, body)
	}
	if got := len(decodeMeFields(t, body)); got != 1 {
		t.Fatalf("list fields = %d, want 1", got)
	}

	// Update the label.
	res, body = f.do(http.MethodPatch, "/api/v1/admin/fields/"+itoa(id),
		map[string]any{"name": "School"}, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("update field: %d (%s)", res.StatusCode, body)
	}

	// Delete it.
	res, body = f.do(http.MethodDelete, "/api/v1/admin/fields/"+itoa(id), nil, auth...)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("delete field: %d (%s)", res.StatusCode, body)
	}

	for _, act := range []string{"INSERT", "UPDATE", "DELETE"} {
		if n := f.auditCount("fields", act, adminID); n < 1 {
			t.Fatalf("audit fields %s = %d, want >= 1", act, n)
		}
	}

	// A non-existent field is a clean 404, not a 500.
	res, _ = f.do(http.MethodDelete, "/api/v1/admin/fields/99999", nil, auth...)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("delete missing field: %d, want 404", res.StatusCode)
	}
}

// TestRequiredFieldLockout is the headline: a required field must never produce an account that
// cannot pass the profile-complete gate with no remedy.
func TestRequiredFieldLockout(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers)
	cookie, csrf, _ := f.admin("root", "root@example.com")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	fieldID := f.createField(auth, map[string]any{
		"name": "Country", "applies_to": "user", "field_type": "text", "required": true,
	})

	// Registration WITHOUT the required answer is refused 422 — no half-account is created.
	res, body := f.registerWith("nofill", "nofill@example.com", "correct-horse-battery", nil)
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("register without required field: %d (%s), want 422", res.StatusCode, body)
	}
	var count int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM users WHERE email = 'nofill@example.com'`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Fatalf("a refused registration left %d user rows, want 0 (no half-account)", count)
	}

	// Registration WITH the answer succeeds, and the account clears the profile gate.
	res, body = f.registerWith("filled", "filled@example.com", "correct-horse-battery",
		[]map[string]any{{"field_id": fieldID, "value": "Norway"}})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("register with required field: %d (%s), want 200", res.StatusCode, body)
	}
	filledCookie := sessionCookie(res)
	res, _ = f.do(http.MethodGet, "/api/v1/challenges", nil, withCookie(filledCookie))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("profile-gated route for a fully-answered account: %d, want 200 (gate satisfied)", res.StatusCode)
	}

	// A user who registered BEFORE the field became required is gated — but has the /me remedy.
	// Drop the field, register plainly, then re-add it as required to reproduce the retro-active case.
	res, _ = f.do(http.MethodDelete, "/api/v1/admin/fields/"+itoa(fieldID), nil, auth...)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("delete field: %d", res.StatusCode)
	}
	legacyCookie, _ := f.register("legacy", "legacy@example.com", "correct-horse-battery")
	newFieldID := f.createField(auth, map[string]any{
		"name": "Eligibility", "applies_to": "user", "field_type": "boolean", "required": true,
	})

	// Now the legacy account is blocked by the gate...
	res, _ = f.do(http.MethodGet, "/api/v1/challenges", nil, withCookie(legacyCookie))
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("legacy account with unanswered required field: %d, want 403 (profile gate)", res.StatusCode)
	}
	// ...but can still log in and reach /me, which offers the field...
	res, meBody := f.do(http.MethodGet, "/api/v1/me", nil, withCookie(legacyCookie))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("legacy /me: %d, want 200 (login itself is never blocked)", res.StatusCode)
	}
	legacyCSRF := decodeCSRF(t, meBody)
	// ...and answering it via /me clears the gate.
	res, body = f.do(http.MethodPut, "/api/v1/me/fields",
		map[string]any{"fields": []map[string]any{{"field_id": newFieldID, "value": true}}},
		withCookie(legacyCookie), withCSRF(legacyCSRF))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("answer required field via /me: %d (%s)", res.StatusCode, body)
	}
	res, _ = f.do(http.MethodGet, "/api/v1/challenges", nil, withCookie(legacyCookie))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("gate after answering via /me: %d, want 200 (remedy works)", res.StatusCode)
	}
}

// TestFieldAnswerValidation covers type-checking and the editable/public rules on /me.
func TestFieldAnswerValidation(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers)
	cookie, csrf, _ := f.admin("root", "root@example.com")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	textField := f.createField(auth, map[string]any{
		"name": "School", "applies_to": "user", "field_type": "text", "editable": true, "public": true,
	})
	lockedField := f.createField(auth, map[string]any{
		"name": "Badge", "applies_to": "user", "field_type": "text", "editable": false, "public": false,
	})

	// A text field answered with a bool is refused at registration.
	res, body := f.registerWith("typo", "typo@example.com", "correct-horse-battery",
		[]map[string]any{{"field_id": textField, "value": true}})
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("text field answered with a bool: %d (%s), want 422", res.StatusCode, body)
	}

	// Register cleanly, then exercise the /me rules.
	userCookie, userCSRF := f.register("player", "player@example.com", "correct-horse-battery")
	userAuth := []func(*http.Request){withCookie(userCookie), withCSRF(userCSRF)}

	// Editable field: answerable via /me.
	res, body = f.do(http.MethodPut, "/api/v1/me/fields",
		map[string]any{"fields": []map[string]any{{"field_id": textField, "value": "MIT"}}}, userAuth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("edit editable field: %d (%s)", res.StatusCode, body)
	}

	// Non-editable field: refused.
	res, body = f.do(http.MethodPut, "/api/v1/me/fields",
		map[string]any{"fields": []map[string]any{{"field_id": lockedField, "value": "hax"}}}, userAuth...)
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("edit non-editable field: %d (%s), want 422", res.StatusCode, body)
	}

	// Public projection shows only the public, answered field; the non-public one never appears.
	pub, err := f.acct.PublicFields(context.Background(), f.userID("player@example.com"))
	if err != nil {
		t.Fatalf("public fields: %v", err)
	}
	if len(pub) != 1 || pub[0].ID != textField {
		t.Fatalf("public fields = %+v, want just the public School field", pub)
	}

	// /me shows both fields (self view), the editable one answered.
	res, meBody := f.do(http.MethodGet, "/api/v1/me", nil, userAuth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("/me: %d", res.StatusCode)
	}
	fields := decodeMeFields(t, meBody)
	if len(fields) != 2 {
		t.Fatalf("/me fields = %d, want 2", len(fields))
	}
	for _, fl := range fields {
		if fl.ID == textField && string(fl.Value) != `"MIT"` {
			t.Fatalf("editable field value = %s, want \"MIT\"", fl.Value)
		}
	}
}
