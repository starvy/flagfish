//go:build integration

package integration

import (
	"context"
	"encoding/csv"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/starvy/flagfish/internal/domain/account"
)

// getRaw issues a GET and returns the status, headers, and undrained-into-JSON body. The CSV exports
// are not JSON, so the shared do() helper's decode path is the wrong tool.
func (f *apiFix) getRaw(path string, mut ...func(*http.Request)) (int, http.Header, string) {
	f.t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, f.server.URL+path, http.NoBody)
	if err != nil {
		f.t.Fatalf("request: %v", err)
	}
	for _, m := range mut {
		m(req)
	}
	res, err := f.client.Do(req)
	if err != nil {
		f.t.Fatalf("get %s: %v", path, err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		f.t.Fatalf("read body: %v", err)
	}
	return res.StatusCode, res.Header, string(b)
}

// parseCSV parses a CSV export into header + rows, failing the test on a malformed document. Parsing
// with encoding/csv is the point: a name carrying a comma, a quote, or a newline only round-trips if
// the writer quoted it correctly, so a clean parse IS the quoting assertion.
func parseCSV(t *testing.T, body string) (header []string, rows [][]string) {
	t.Helper()
	r := csv.NewReader(strings.NewReader(body))
	records, err := r.ReadAll()
	if err != nil {
		t.Fatalf("parse csv: %v\n%s", err, body)
	}
	if len(records) == 0 {
		t.Fatal("empty csv: not even a header row")
	}
	return records[0], records[1:]
}

// seedScoringUser inserts a user and, when value != 0, a solve worth that many points, so it lands on
// the standings. Names carrying CSV metacharacters are the hostile input the export must survive.
func (f *apiFix) seedScoringUser(name, email string, solveValue, awardValue int, hidden, banned bool) int64 {
	f.t.Helper()
	ctx := context.Background()
	var id int64
	if err := f.pool.QueryRow(ctx,
		`INSERT INTO users (name, email, hidden, banned, verified) VALUES ($1,$2,$3,$4,true) RETURNING id`,
		name, email, hidden, banned).Scan(&id); err != nil {
		f.t.Fatalf("seed user %q: %v", email, err)
	}
	if solveValue != 0 {
		chID := f.seedChallenge("chal-"+email, "misc", solveValue)
		if _, err := f.pool.Exec(ctx,
			`INSERT INTO solves (challenge_id, user_id, value) VALUES ($1,$2,$3)`, chID, id, solveValue); err != nil {
			f.t.Fatalf("seed solve for %q: %v", email, err)
		}
	}
	if awardValue != 0 {
		if _, err := f.pool.Exec(ctx,
			`INSERT INTO awards (user_id, type, name, value) VALUES ($1,'standard','bonus',$2)`, id, awardValue); err != nil {
			f.t.Fatalf("seed award for %q: %v", email, err)
		}
	}
	return id
}

func TestAdminExportStandingsAndUsers(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers)
	cookie, _, _ := f.admin("root", "root@example.com")
	adminCookie := withCookie(cookie)

	// A comma, a double-quote, and a literal newline — one hostile CSV metacharacter each.
	commaName := "Alice, the Bold"
	quoteName := `Bob "Quotes" Smith`
	newlineName := "Carol\nMultiline"

	// Scores: Carol 500 (hidden), Alice 300+50=350, Bob 200 (banned), Dave 0 (no solve → off the board).
	f.seedScoringUser(newlineName, "carol@example.com", 500, 0, true, false)
	aliceID := f.seedScoringUser(commaName, "alice@example.com", 300, 50, false, false)
	f.seedScoringUser(quoteName, "bob@example.com", 200, 0, false, true)
	f.seedScoringUser("Dave NoScore", "dave@example.com", 0, 0, false, false)

	// --- standings.csv ---
	status, hdr, body := f.getRaw("/api/v1/admin/export/standings.csv", adminCookie)
	if status != http.StatusOK {
		t.Fatalf("standings.csv: got %d, want 200\n%s", status, body)
	}
	if ct := hdr.Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Errorf("standings.csv Content-Type = %q, want text/csv", ct)
	}
	if cd := hdr.Get("Content-Disposition"); !strings.Contains(cd, "standings.csv") {
		t.Errorf("standings.csv Content-Disposition = %q, want an attachment filename", cd)
	}

	header, rows := parseCSV(t, body)
	wantHeader := []string{"rank", "account_id", "name", "bracket", "score", "last_event", "hidden", "banned"}
	if strings.Join(header, ",") != strings.Join(wantHeader, ",") {
		t.Errorf("standings header = %v, want %v", header, wantHeader)
	}
	if len(rows) != 3 {
		t.Fatalf("standings rows = %d, want 3 (Dave has no scoring event)\n%s", len(rows), body)
	}
	// Rank order by score: Carol 500, Alice 350, Bob 200.
	if rows[0][2] != newlineName || rows[0][4] != "500" || rows[0][6] != "true" {
		t.Errorf("rank 1 = %v, want the hidden 500-point Carol flagged hidden", rows[0])
	}
	if rows[1][2] != commaName || rows[1][4] != "350" {
		t.Errorf("rank 2 = %v, want Alice at 350 (300 solve + 50 award)", rows[1])
	}
	if rows[2][2] != quoteName || rows[2][4] != "200" || rows[2][7] != "true" {
		t.Errorf("rank 3 = %v, want the banned 200-point Bob flagged banned", rows[2])
	}
	for i, r := range rows {
		if got := r[0]; got != []string{"1", "2", "3"}[i] {
			t.Errorf("row %d rank column = %q, want %d", i, got, i+1)
		}
	}

	// --- users.csv ---
	status, hdr, body = f.getRaw("/api/v1/admin/export/users.csv", adminCookie)
	if status != http.StatusOK {
		t.Fatalf("users.csv: got %d, want 200\n%s", status, body)
	}
	if ct := hdr.Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Errorf("users.csv Content-Type = %q, want text/csv", ct)
	}
	header, rows = parseCSV(t, body)
	wantUsersHeader := []string{
		"id", "name", "email", "role", "verified", "banned", "hidden",
		"team_id", "bracket", "website", "affiliation", "country", "created_at",
	}
	if strings.Join(header, ",") != strings.Join(wantUsersHeader, ",") {
		t.Errorf("users header = %v, want %v", header, wantUsersHeader)
	}
	// root + 4 seeded users = 5. Every user is present, including the banned and hidden ones.
	if len(rows) != 5 {
		t.Fatalf("users rows = %d, want 5\n%s", len(rows), body)
	}
	byEmail := map[string][]string{}
	for _, r := range rows {
		byEmail[r[2]] = r
	}
	if r, ok := byEmail["alice@example.com"]; !ok || r[1] != commaName {
		t.Errorf("alice row = %v, want name %q intact through the CSV", r, commaName)
	}
	if r, ok := byEmail["bob@example.com"]; !ok || r[5] != "true" {
		t.Errorf("bob row = %v, want banned=true", r)
	}
	if r, ok := byEmail["carol@example.com"]; !ok || r[6] != "true" || r[1] != newlineName {
		t.Errorf("carol row = %v, want hidden=true and the multiline name intact", r)
	}
	if r := byEmail["root@example.com"]; r == nil || r[3] != "admin" {
		t.Errorf("root row = %v, want role=admin", r)
	}
	_ = aliceID

	// --- non-admin is refused on every export route ---
	userCookie, _ := f.register("mallory", "mallory@example.com", "correct-horse-battery")
	for _, path := range []string{
		"/api/v1/admin/export/standings.csv",
		"/api/v1/admin/export/users.csv",
		"/api/v1/admin/export/teams.csv",
	} {
		if st, _, _ := f.getRaw(path, withCookie(userCookie)); st != http.StatusForbidden {
			t.Errorf("%s as non-admin: got %d, want 403", path, st)
		}
		if st, _, _ := f.getRaw(path); st != http.StatusForbidden {
			t.Errorf("%s anonymous: got %d, want 403", path, st)
		}
	}
}

func TestAdminExportTeams(t *testing.T) {
	f := newAdminAPI(t, account.ModeTeams)
	cookie, _, _ := f.admin("root", "root@example.com")
	adminCookie := withCookie(cookie)

	ctx := context.Background()
	// A team name with a comma exercises the quoting on the teams export too.
	var teamID int64
	if err := f.pool.QueryRow(ctx,
		`INSERT INTO teams (name, hidden, banned) VALUES ($1,false,true) RETURNING id`,
		"Comma, Inc").Scan(&teamID); err != nil {
		t.Fatalf("seed team: %v", err)
	}

	status, hdr, body := f.getRaw("/api/v1/admin/export/teams.csv", adminCookie)
	if status != http.StatusOK {
		t.Fatalf("teams.csv: got %d, want 200\n%s", status, body)
	}
	if cd := hdr.Get("Content-Disposition"); !strings.Contains(cd, "teams.csv") {
		t.Errorf("teams.csv Content-Disposition = %q", cd)
	}
	header, rows := parseCSV(t, body)
	wantHeader := []string{
		"id", "name", "email", "website", "affiliation", "country",
		"bracket", "captain_id", "member_count", "hidden", "banned", "created_at",
	}
	if strings.Join(header, ",") != strings.Join(wantHeader, ",") {
		t.Errorf("teams header = %v, want %v", header, wantHeader)
	}
	if len(rows) != 1 {
		t.Fatalf("teams rows = %d, want 1\n%s", len(rows), body)
	}
	// name intact through quoting; the banned team is present and flagged (admin export).
	if rows[0][1] != "Comma, Inc" || rows[0][10] != "true" || rows[0][8] != "0" {
		t.Errorf("team row = %v, want name %q, banned=true, member_count=0", rows[0], "Comma, Inc")
	}
}
