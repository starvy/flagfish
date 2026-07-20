package exporter

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"strconv"
	"time"
)

// CSV export of the two artifacts organisers reach for outside the platform: the final standings (for
// prizes) and the account roster (for eligibility). These are projections of the standings and account
// tables — no new facts, no schema.
//
// Three properties are load-bearing:
//
//   - encoding/csv owns the quoting. A team name with a comma, a double-quote, or a newline is a
//     routine hostile input on a CTF, and hand-rolled CSV mangles all three. Never format a field by
//     hand here.
//   - The standings export is the ADMIN board, not the public one: hidden and banned rows are present
//     and flagged. The export is an organiser's working artifact for awarding prizes and vetting
//     eligibility, so it must show a disqualified-but-not-yet-removed entrant rather than silently drop
//     it — the human decides who is eligible, the export does not decide for them.
//   - Users and teams stream row-by-row straight off the pool: a thousands-row roster is written to the
//     response as it is scanned, never assembled in memory first. The standings writer takes an
//     already-ranked slice because that ranking (decay-stamped values, the score/last-event/id
//     tiebreak) belongs to the one standings query and must be reused, not reimplemented here.

func newCSV(w io.Writer) *csv.Writer { return csv.NewWriter(w) }

// StandingRow is one final-standings line. Rank is positional (the caller supplies the slice already
// ordered by the standings query). Bracket is the division name, empty when the account is in none.
type StandingRow struct {
	Rank      int
	AccountID int64
	Name      string
	Bracket   string
	Score     int64
	LastEvent time.Time
	Hidden    bool
	Banned    bool
}

var standingsHeader = []string{
	"rank", "account_id", "name", "bracket", "score", "last_event", "hidden", "banned",
}

// WriteStandings writes the ranked standings as CSV. The rows arrive in the rank order the standings
// query produced; this function only formats and quotes them.
func WriteStandings(w io.Writer, rows []StandingRow) error {
	cw := newCSV(w)
	if err := cw.Write(standingsHeader); err != nil {
		return fmt.Errorf("csv standings header: %w", err)
	}
	for _, r := range rows {
		if err := cw.Write([]string{
			strconv.Itoa(r.Rank),
			strconv.FormatInt(r.AccountID, 10),
			r.Name,
			r.Bracket,
			strconv.FormatInt(r.Score, 10),
			formatTime(r.LastEvent),
			strconv.FormatBool(r.Hidden),
			strconv.FormatBool(r.Banned),
		}); err != nil {
			return fmt.Errorf("csv standings row: %w", err)
		}
	}
	cw.Flush()
	return cw.Error()
}

var usersHeader = []string{
	"id", "name", "email", "role", "verified", "banned", "hidden",
	"team_id", "bracket", "website", "affiliation", "country", "created_at",
}

// The account roster query pins its own column order rather than SELECT *, so the CSV layout is a
// contract the caller can rely on and a new users column does not silently shift it.
const usersExportSQL = `
SELECT u.id, u.name, u.email, u.role, u.verified, u.banned, u.hidden,
       u.team_id, b.name AS bracket, u.website, u.affiliation, u.country, u.created_at
  FROM users u
  LEFT JOIN brackets b ON b.id = u.bracket_id
 ORDER BY u.id`

// WriteUsers streams every user as CSV, row-by-row off q. It is an admin export: banned and hidden
// accounts are included and flagged, and email — searchable nowhere public — is present for eligibility.
func WriteUsers(ctx context.Context, q querier, w io.Writer) error {
	rows, err := q.Query(ctx, usersExportSQL)
	if err != nil {
		return fmt.Errorf("csv users query: %w", err)
	}
	defer rows.Close()

	cw := newCSV(w)
	if err := cw.Write(usersHeader); err != nil {
		return fmt.Errorf("csv users header: %w", err)
	}
	for rows.Next() {
		var (
			id                                     int64
			name, email, role                      string
			verified, banned, hidden               bool
			teamID                                 *int64
			bracket, website, affiliation, country *string
			createdAt                              time.Time
		)
		if err := rows.Scan(&id, &name, &email, &role, &verified, &banned, &hidden,
			&teamID, &bracket, &website, &affiliation, &country, &createdAt); err != nil {
			return fmt.Errorf("csv users scan: %w", err)
		}
		if err := cw.Write([]string{
			strconv.FormatInt(id, 10), name, email, role,
			strconv.FormatBool(verified), strconv.FormatBool(banned), strconv.FormatBool(hidden),
			formatIntPtr(teamID), derefStr(bracket), derefStr(website), derefStr(affiliation),
			derefStr(country), formatTime(createdAt),
		}); err != nil {
			return fmt.Errorf("csv users row: %w", err)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("csv users iterate: %w", err)
	}
	cw.Flush()
	return cw.Error()
}

var teamsHeader = []string{
	"id", "name", "email", "website", "affiliation", "country",
	"bracket", "captain_id", "member_count", "hidden", "banned", "created_at",
}

const teamsExportSQL = `
SELECT t.id, t.name, t.email, t.website, t.affiliation, t.country,
       b.name AS bracket, t.captain_id,
       (SELECT count(*) FROM users u WHERE u.team_id = t.id)::bigint AS member_count,
       t.hidden, t.banned, t.created_at
  FROM teams t
  LEFT JOIN brackets b ON b.id = t.bracket_id
 ORDER BY t.id`

// WriteTeams streams every team as CSV, row-by-row off q, with the same admin-visibility rule as
// WriteUsers. member_count is counted in the same round trip so the roster and its size never disagree.
func WriteTeams(ctx context.Context, q querier, w io.Writer) error {
	rows, err := q.Query(ctx, teamsExportSQL)
	if err != nil {
		return fmt.Errorf("csv teams query: %w", err)
	}
	defer rows.Close()

	cw := newCSV(w)
	if err := cw.Write(teamsHeader); err != nil {
		return fmt.Errorf("csv teams header: %w", err)
	}
	for rows.Next() {
		var (
			id                                            int64
			name                                          string
			email, website, affiliation, country, bracket *string
			captainID                                     *int64
			memberCount                                   int64
			hidden, banned                                bool
			createdAt                                     time.Time
		)
		if err := rows.Scan(&id, &name, &email, &website, &affiliation, &country,
			&bracket, &captainID, &memberCount, &hidden, &banned, &createdAt); err != nil {
			return fmt.Errorf("csv teams scan: %w", err)
		}
		if err := cw.Write([]string{
			strconv.FormatInt(id, 10), name, derefStr(email), derefStr(website),
			derefStr(affiliation), derefStr(country), derefStr(bracket), formatIntPtr(captainID),
			strconv.FormatInt(memberCount, 10), strconv.FormatBool(hidden),
			strconv.FormatBool(banned), formatTime(createdAt),
		}); err != nil {
			return fmt.Errorf("csv teams row: %w", err)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("csv teams iterate: %w", err)
	}
	cw.Flush()
	return cw.Error()
}

// formatTime renders an instant as RFC3339 in UTC; a zero time (an account with no scoring event yet,
// or a NULL) becomes an empty cell rather than the year-1 sentinel.
func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func formatIntPtr(n *int64) string {
	if n == nil {
		return ""
	}
	return strconv.FormatInt(*n, 10)
}
