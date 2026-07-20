//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/starvy/flagfish/internal/domain/account"
)

func TestSelfServeProfilePatch(t *testing.T) {
	f := newAPI(t, account.ModeUsers)
	cookie, csrf := f.register("selfie", "selfie@example.com", "correct-horse-battery")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	// set two fields
	res, body := f.do(http.MethodPatch, "/api/v1/me", map[string]any{
		"website": "https://selfie.example", "country": "CZ",
	}, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("patch: %d (%s)", res.StatusCode, body)
	}

	// clear one with an explicit null, keep the other by omission
	res, body = f.do(http.MethodPatch, "/api/v1/me", map[string]any{"website": nil}, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("clear patch: %d (%s)", res.StatusCode, body)
	}

	var me struct {
		Website *string `json:"website"`
		Country *string `json:"country"`
	}
	res, body = f.do(http.MethodGet, "/api/v1/me", nil, withCookie(cookie))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("me: %d", res.StatusCode)
	}
	if err := json.Unmarshal(body, &me); err != nil {
		t.Fatalf("decode me: %v", err)
	}
	if me.Website != nil {
		t.Errorf("website survived the explicit null: %v", *me.Website)
	}
	if me.Country == nil || *me.Country != "CZ" {
		t.Errorf("country did not survive an unrelated patch: %v", me.Country)
	}

	// identity is not part of the profile surface: an unknown key is refused outright
	res, _ = f.do(http.MethodPatch, "/api/v1/me", map[string]any{"email": "new@example.com"}, auth...)
	if res.StatusCode == http.StatusOK {
		t.Error("PATCH /me accepted an email change")
	}
}

func TestSelfServeLanguagePreference(t *testing.T) {
	f := newAPI(t, account.ModeUsers)
	cookie, csrf := f.register("polyglot", "polyglot@example.com", "correct-horse-battery")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	readLang := func() *string {
		t.Helper()
		var me struct {
			Language *string `json:"language"`
		}
		res, body := f.do(http.MethodGet, "/api/v1/me", nil, withCookie(cookie))
		if res.StatusCode != http.StatusOK {
			t.Fatalf("me: %d (%s)", res.StatusCode, body)
		}
		if err := json.Unmarshal(body, &me); err != nil {
			t.Fatalf("decode me: %v", err)
		}
		return me.Language
	}

	// A malformed tag is refused before it touches the column.
	res, _ := f.do(http.MethodPatch, "/api/v1/me", map[string]any{"language": "not a tag!"}, auth...)
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("malformed language: got %d, want 422", res.StatusCode)
	}
	if lang := readLang(); lang != nil {
		t.Fatalf("a rejected language must not persist: %v", *lang)
	}

	// A well-formed regional tag sets and echoes.
	res, body := f.do(http.MethodPatch, "/api/v1/me", map[string]any{"language": "pt-BR"}, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("set language: %d (%s)", res.StatusCode, body)
	}
	if lang := readLang(); lang == nil || *lang != "pt-BR" {
		t.Fatalf("language did not round-trip: %v", lang)
	}

	// An unrelated patch keeps the preference; an explicit null clears it.
	res, _ = f.do(http.MethodPatch, "/api/v1/me", map[string]any{"country": "BR"}, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("unrelated patch: %d", res.StatusCode)
	}
	if lang := readLang(); lang == nil || *lang != "pt-BR" {
		t.Fatalf("omitted language must be kept, got %v", lang)
	}
	res, _ = f.do(http.MethodPatch, "/api/v1/me", map[string]any{"language": nil}, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("clear language: %d", res.StatusCode)
	}
	if lang := readLang(); lang != nil {
		t.Fatalf("explicit null must clear language, got %v", *lang)
	}
}

func TestCaptainTeamSettings(t *testing.T) {
	f := newAPI(t, account.ModeTeams)

	capCookie, capCSRF := f.register("cap", "cap@example.com", "correct-horse-battery")
	capAuth := []func(*http.Request){withCookie(capCookie), withCSRF(capCSRF)}

	// The captain creates the team through the real route, so captaincy is the enrolled kind.
	res, body := f.do(http.MethodPost, "/api/v1/teams", map[string]any{"name": "editable"}, capAuth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("create team: %d (%s)", res.StatusCode, body)
	}
	teamID := decodeID(t, body)

	memberCookie, memberCSRF := f.register("mem", "mem@example.com", "correct-horse-battery")
	f.joinTeam("mem@example.com", teamID)

	// captain sets contact + profile data
	res, body = f.do(http.MethodPatch, "/api/v1/me/team", map[string]any{
		"email": "team@ctf.test", "website": "https://editable.example",
	}, capAuth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("captain patch: %d (%s)", res.StatusCode, body)
	}
	var team struct {
		Email   *string `json:"email"`
		Website *string `json:"website"`
	}
	if err := json.Unmarshal(body, &team); err != nil {
		t.Fatalf("decode team: %v", err)
	}
	if team.Email == nil || *team.Email != "team@ctf.test" || team.Website == nil {
		t.Errorf("captain patch result: %+v", team)
	}

	// an ordinary member is refused — captaincy is the L2 guard, in the statement
	res, body = f.do(http.MethodPatch, "/api/v1/me/team", map[string]any{"website": "https://hijack.example"},
		withCookie(memberCookie), withCSRF(memberCSRF))
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("member patch: %d, want 403 (%s)", res.StatusCode, body)
	}

	// the team email is unique, case-insensitively, against every other team
	f.seedTeam("squatter") // squatter@team.test
	res, body = f.do(http.MethodPatch, "/api/v1/me/team", map[string]any{"email": "SQUATTER@team.test"}, capAuth...)
	if res.StatusCode != http.StatusConflict {
		t.Errorf("email collision: %d, want 409 (%s)", res.StatusCode, body)
	}

	// the public team page never carries the contact address
	res, body = f.do(http.MethodGet, "/api/v1/teams/"+itoa(teamID), nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("public page: %d", res.StatusCode)
	}
	var pub map[string]any
	if err := json.Unmarshal(body, &pub); err != nil {
		t.Fatalf("decode public: %v", err)
	}
	if _, leaked := pub["email"]; leaked {
		t.Error("the public team page leaks the team email")
	}
}
