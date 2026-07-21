//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/starvy/flagfish/internal/accounts"
	"github.com/starvy/flagfish/internal/adminops"
	"github.com/starvy/flagfish/internal/anticheat"
	"github.com/starvy/flagfish/internal/board"
	"github.com/starvy/flagfish/internal/catalog"
	"github.com/starvy/flagfish/internal/config"
	"github.com/starvy/flagfish/internal/domain/account"
	"github.com/starvy/flagfish/internal/gameplay"
	"github.com/starvy/flagfish/internal/httpapi"
)

// newAnticheatAPI mirrors newAdminAPI but also wires the anticheat service, so the read-only
// /api/v1/admin/anticheat routes are registered.
func newAnticheatAPI(t *testing.T, mode account.Mode) *apiFix {
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
	srv := httpapi.New(httpapi.Options{
		Config:    cfg,
		Auth:      acct,
		Limiter:   accounts.NewLimiter(pool, 1000, 60_000_000_000),
		Log:       log,
		Accounts:  acct,
		Gameplay:  gameplay.New(pool, stubInserter{}, mode),
		Catalog:   catalog.New(pool),
		Board:     board.New(pool, mode),
		AdminOps:  adminops.New(pool),
		Anticheat: anticheat.New(pool),
	})

	ts := httptest.NewServer(srv.Router)
	t.Cleanup(ts.Close)

	return &apiFix{
		t: t, pool: pool, acct: acct, server: ts,
		client: &http.Client{
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

// seedSubmission inserts one attempt directly. attributedTo <= 0 leaves attribution NULL, matching a
// wrong answer or a non-unique correct one.
func (f *apiFix) seedSubmission(challengeID, userID int64, kind, ip string, attributedTo int64) {
	f.t.Helper()
	var attr *int64
	if attributedTo > 0 {
		attr = &attributedTo
	}
	if _, err := f.pool.Exec(context.Background(),
		`INSERT INTO submissions (challenge_id, user_id, type, provided, ip, attributed_account_id)
		 VALUES ($1, $2, $3, 'x', $4, $5)`,
		challengeID, userID, kind, ip, attr); err != nil {
		f.t.Fatalf("seed submission: %v", err)
	}
}

// acReport is a loose decode of the report bodies — only the fields the assertions read.
type acReport struct {
	Pairs []struct {
		IssuedTo        int64   `json:"issued_to"`
		IssuedToName    string  `json:"issued_to_name"`
		Submitter       int64   `json:"submitter"`
		SubmitterName   string  `json:"submitter_name"`
		SubmissionCount int64   `json:"submission_count"`
		ChallengeCount  int64   `json:"challenge_count"`
		ChallengeIDs    []int64 `json:"challenge_ids"`
	} `json:"pairs"`
	Clusters []struct {
		IP           string   `json:"ip"`
		AccountCount int64    `json:"account_count"`
		AccountIDs   []int64  `json:"account_ids"`
		AccountNames []string `json:"account_names"`
	} `json:"clusters"`
	AccountName string `json:"account_name"`
	Sharing     []struct {
		Direction        string `json:"direction"`
		Counterparty     int64  `json:"counterparty"`
		CounterpartyName string `json:"counterparty_name"`
	} `json:"sharing"`
	IPOverlap []struct {
		IP               string `json:"ip"`
		OtherAccountID   int64  `json:"other_account_id"`
		OtherAccountName string `json:"other_account_name"`
	} `json:"ip_overlap"`
	Solves []struct {
		AccountID int64  `json:"account_id"`
		UserID    int64  `json:"user_id"`
		UserName  string `json:"user_name"`
		TeamID    *int64 `json:"team_id"`
		TeamName  string `json:"team_name"`
	} `json:"solves"`
	Total int64 `json:"total"`
}

// clusterName resolves an account's display name from the IP-overlap clusters in r, relying on
// account_ids and account_names being index-aligned.
func clusterName(r *acReport, id int64) string {
	for _, c := range r.Clusters {
		for i, aid := range c.AccountIDs {
			if aid == id && i < len(c.AccountNames) {
				return c.AccountNames[i]
			}
		}
	}
	return ""
}

func decodeAC(t *testing.T, body []byte) acReport {
	t.Helper()
	var r acReport
	if err := json.Unmarshal(body, &r); err != nil {
		t.Fatalf("decode anticheat report: %v (%s)", err, body)
	}
	return r
}

// TestAnticheatDetectors plants exactly three shapes — a clean account, a flag-sharing pair, and an
// IP-overlap cluster — and asserts each detector finds its planted case and leaves the clean one out.
func TestAnticheatDetectors(t *testing.T) {
	f := newAnticheatAPI(t, account.ModeUsers)
	cookie, csrf, _ := f.admin("root", "root@example.com")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	// alice=issuer, bob=sharer, carol=IP neighbour, dave=clean.
	f.register("alice", "alice@example.com", "correct-horse-battery")
	f.register("bob", "bob@example.com", "correct-horse-battery")
	f.register("carol", "carol@example.com", "correct-horse-battery")
	f.register("dave", "dave@example.com", "correct-horse-battery")
	alice := f.userID("alice@example.com")
	bob := f.userID("bob@example.com")
	carol := f.userID("carol@example.com")
	dave := f.userID("dave@example.com")

	c1 := f.seedChallenge("c1", "misc", 100)
	c2 := f.seedChallenge("c2", "misc", 100)

	// Flag sharing: bob submits correct flags attributed to alice, on two challenges.
	f.seedSubmission(c1, bob, "correct", "203.0.113.9", alice)
	f.seedSubmission(c2, bob, "correct", "203.0.113.9", alice)
	// Alice's own correct submission, attributed to herself — legitimate, must not flag.
	f.seedSubmission(c1, alice, "correct", "198.51.100.1", alice)

	// IP overlap: alice, bob, carol all submit from one shared address.
	f.seedSubmission(c1, alice, "incorrect", "192.0.2.50", 0)
	f.seedSubmission(c2, bob, "incorrect", "192.0.2.50", 0)
	f.seedSubmission(c1, carol, "incorrect", "192.0.2.50", 0)

	// Dave is clean: a wrong answer from an address nobody else uses, no attribution.
	f.seedSubmission(c1, dave, "incorrect", "192.0.2.200", 0)

	t.Run("flag-sharing", func(t *testing.T) {
		res, body := f.do(http.MethodGet, "/api/v1/admin/anticheat/flag-sharing", nil, auth...)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("flag-sharing: got %d, want 200 (%s)", res.StatusCode, body)
		}
		r := decodeAC(t, body)
		if len(r.Pairs) != 1 {
			t.Fatalf("want exactly 1 sharing pair, got %d (%s)", len(r.Pairs), body)
		}
		p := r.Pairs[0]
		if p.IssuedTo != alice || p.Submitter != bob {
			t.Errorf("pair mismatch: issued_to=%d submitter=%d, want %d/%d", p.IssuedTo, p.Submitter, alice, bob)
		}
		// The whole point of this screen: a human reads a name, not a bare id.
		if p.IssuedToName != "alice" || p.SubmitterName != "bob" {
			t.Errorf("pair names: issued_to_name=%q submitter_name=%q, want alice/bob", p.IssuedToName, p.SubmitterName)
		}
		if p.SubmissionCount != 2 || p.ChallengeCount != 2 {
			t.Errorf("counts: submissions=%d challenges=%d, want 2/2", p.SubmissionCount, p.ChallengeCount)
		}
		if len(p.ChallengeIDs) != 2 {
			t.Errorf("challenge_ids: %v, want two", p.ChallengeIDs)
		}
		// The clean account and alice's self-attributed solve are absent by construction.
		for _, q := range r.Pairs {
			if q.Submitter == dave || q.Submitter == alice {
				t.Errorf("clean/legit account %d flagged as sharer", q.Submitter)
			}
		}
	})

	t.Run("ip-overlap", func(t *testing.T) {
		res, body := f.do(http.MethodGet, "/api/v1/admin/anticheat/ip-overlap?min_accounts=2", nil, auth...)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("ip-overlap: got %d, want 200 (%s)", res.StatusCode, body)
		}
		r := decodeAC(t, body)
		if len(r.Clusters) != 1 {
			t.Fatalf("want exactly 1 cluster, got %d (%s)", len(r.Clusters), body)
		}
		c := r.Clusters[0]
		if c.IP != "192.0.2.50" {
			t.Errorf("cluster ip: %q, want 192.0.2.50", c.IP)
		}
		if c.AccountCount != 3 {
			t.Errorf("account_count: %d, want 3", c.AccountCount)
		}
		if !containsAll(c.AccountIDs, alice, bob, carol) {
			t.Errorf("account_ids %v missing one of %d/%d/%d", c.AccountIDs, alice, bob, carol)
		}
		if len(c.AccountNames) != len(c.AccountIDs) {
			t.Fatalf("account_names %v not aligned with account_ids %v", c.AccountNames, c.AccountIDs)
		}
		// Names must be index-aligned with ids, and each must resolve to the real display name.
		for id, want := range map[int64]string{alice: "alice", bob: "bob", carol: "carol"} {
			if got := clusterName(&r, id); got != want {
				t.Errorf("cluster name for %d: %q, want %q (names %v ids %v)", id, got, want, c.AccountNames, c.AccountIDs)
			}
		}
		for _, id := range c.AccountIDs {
			if id == dave {
				t.Error("clean account dave appeared in the IP cluster")
			}
		}
	})

	t.Run("ip-overlap-threshold-excludes-pairs", func(t *testing.T) {
		// Raise the threshold above the cluster size: the signal must disappear, not degrade.
		res, body := f.do(http.MethodGet, "/api/v1/admin/anticheat/ip-overlap?min_accounts=4", nil, auth...)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("ip-overlap: got %d (%s)", res.StatusCode, body)
		}
		r := decodeAC(t, body)
		if len(r.Clusters) != 0 {
			t.Errorf("threshold 4 should exclude the 3-account cluster, got %d", len(r.Clusters))
		}
	})

	t.Run("per-account-sharer", func(t *testing.T) {
		res, body := f.do(http.MethodGet, "/api/v1/admin/anticheat/accounts/"+itoa(bob), nil, auth...)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("account report: got %d (%s)", res.StatusCode, body)
		}
		r := decodeAC(t, body)
		if r.AccountName != "bob" {
			t.Errorf("report subject name: %q, want bob (%s)", r.AccountName, body)
		}
		var submitted bool
		for _, e := range r.Sharing {
			if e.Direction == "submitted" && e.Counterparty == alice {
				submitted = true
				if e.CounterpartyName != "alice" {
					t.Errorf("counterparty name: %q, want alice", e.CounterpartyName)
				}
			}
		}
		if !submitted {
			t.Errorf("bob's report missing the submitted->alice edge (%s)", body)
		}
		// Bob shared the 192.0.2.50 address with alice and carol.
		if !neighborsInclude(&r, carol) || !neighborsInclude(&r, alice) {
			t.Errorf("bob's ip_overlap %v missing alice/carol", r.IPOverlap)
		}
		for _, n := range r.IPOverlap {
			if (n.OtherAccountID == alice && n.OtherAccountName != "alice") ||
				(n.OtherAccountID == carol && n.OtherAccountName != "carol") {
				t.Errorf("ip neighbour %d name %q not resolved", n.OtherAccountID, n.OtherAccountName)
			}
		}
	})

	t.Run("per-account-issuer-sees-reverse-direction", func(t *testing.T) {
		res, body := f.do(http.MethodGet, "/api/v1/admin/anticheat/accounts/"+itoa(alice), nil, auth...)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("account report: got %d (%s)", res.StatusCode, body)
		}
		r := decodeAC(t, body)
		var issued bool
		for _, e := range r.Sharing {
			if e.Direction == "issued_to" && e.Counterparty == bob {
				issued = true
			}
		}
		if !issued {
			t.Errorf("alice's report missing the issued_to<-bob edge (%s)", body)
		}
	})

	t.Run("per-account-clean-is-empty", func(t *testing.T) {
		res, body := f.do(http.MethodGet, "/api/v1/admin/anticheat/accounts/"+itoa(dave), nil, auth...)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("account report: got %d (%s)", res.StatusCode, body)
		}
		r := decodeAC(t, body)
		if len(r.Sharing) != 0 || len(r.IPOverlap) != 0 {
			t.Errorf("clean account dave has evidence: sharing=%v ip=%v", r.Sharing, r.IPOverlap)
		}
	})
}

// TestAnticheatRejectsNonAdmin proves every anti-cheat endpoint is behind the admin wall, for both an
// authenticated non-admin and an anonymous caller.
func TestAnticheatRejectsNonAdmin(t *testing.T) {
	f := newAnticheatAPI(t, account.ModeUsers)
	cookie, csrf := f.register("mallory", "mallory@example.com", "correct-horse-battery")

	paths := []string{
		"/api/v1/admin/anticheat/flag-sharing",
		"/api/v1/admin/anticheat/ip-overlap",
		"/api/v1/admin/anticheat/accounts/1",
	}
	for _, p := range paths {
		res, body := f.do(http.MethodGet, p, nil, withCookie(cookie), withCSRF(csrf))
		if res.StatusCode != http.StatusForbidden {
			t.Errorf("GET %s as user: got %d, want 403 (%s)", p, res.StatusCode, body)
		}
		resA, _ := f.do(http.MethodGet, p, nil)
		if resA.StatusCode != http.StatusForbidden {
			t.Errorf("GET %s anonymous: got %d, want 403", p, resA.StatusCode)
		}
	}
}

// seedUserOnTeam inserts a user already enrolled on a team, bypassing the enrollment flow.
func (f *apiFix) seedUserOnTeam(email string, teamID int64) int64 {
	f.t.Helper()
	var id int64
	if err := f.pool.QueryRow(context.Background(),
		`INSERT INTO users (name, email, team_id) VALUES ($1, $1, $2) RETURNING id`,
		email, teamID).Scan(&id); err != nil {
		f.t.Fatalf("seed user on team: %v", err)
	}
	return id
}

// seedTeamSubmission inserts an attempt in team mode, stamping both the acting user and the team.
func (f *apiFix) seedTeamSubmission(challengeID, userID, teamID int64, kind, ip string, attributedTeam int64) {
	f.t.Helper()
	var attr *int64
	if attributedTeam > 0 {
		attr = &attributedTeam
	}
	if _, err := f.pool.Exec(context.Background(),
		`INSERT INTO submissions (challenge_id, user_id, team_id, type, provided, ip, attributed_account_id)
		 VALUES ($1, $2, $3, $4, 'x', $5, $6)`,
		challengeID, userID, teamID, kind, ip, attr); err != nil {
		f.t.Fatalf("seed team submission: %v", err)
	}
}

// TestAnticheatTeamsMode proves the account unit is the team: sharing and IP overlap fire across
// teams, and two members of one team behind one address are invisible — that is what a team is.
func TestAnticheatTeamsMode(t *testing.T) {
	f := newAnticheatAPI(t, account.ModeTeams)
	cookie, csrf, _ := f.admin("root", "root@example.com")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	t1 := f.seedTeam("red")
	t2 := f.seedTeam("blue")
	// Two humans on the same team, plus one on the other.
	u1a := f.seedUserOnTeam("u1a@example.com", t1)
	u1b := f.seedUserOnTeam("u1b@example.com", t1)
	u2 := f.seedUserOnTeam("u2@example.com", t2)

	c1 := f.seedChallenge("c1", "misc", 100)

	// Cross-team sharing: a member of team2 submits a flag issued to team1.
	f.seedTeamSubmission(c1, u2, t2, "correct", "203.0.113.9", t1)

	// Intra-team IP overlap: two members of team1 from one address. Same account (the team), so it
	// must NOT surface as an overlap.
	f.seedTeamSubmission(c1, u1a, t1, "incorrect", "192.0.2.77", 0)
	f.seedTeamSubmission(c1, u1b, t1, "incorrect", "192.0.2.77", 0)
	// Cross-team IP overlap: team2 also uses that address. Now two distinct teams share it.
	f.seedTeamSubmission(c1, u2, t2, "incorrect", "192.0.2.77", 0)

	t.Run("cross-team-sharing-fires", func(t *testing.T) {
		res, body := f.do(http.MethodGet, "/api/v1/admin/anticheat/flag-sharing", nil, auth...)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("flag-sharing: got %d (%s)", res.StatusCode, body)
		}
		r := decodeAC(t, body)
		if len(r.Pairs) != 1 || r.Pairs[0].IssuedTo != t1 || r.Pairs[0].Submitter != t2 {
			t.Fatalf("want one pair t2->t1, got %+v (%s)", r.Pairs, body)
		}
		// In teams mode the account is the team, so the name shown is the team name.
		if r.Pairs[0].IssuedToName != "red" || r.Pairs[0].SubmitterName != "blue" {
			t.Errorf("team names: issued_to=%q submitter=%q, want red/blue", r.Pairs[0].IssuedToName, r.Pairs[0].SubmitterName)
		}
	})

	t.Run("overlap-is-teams-not-members", func(t *testing.T) {
		res, body := f.do(http.MethodGet, "/api/v1/admin/anticheat/ip-overlap?min_accounts=2", nil, auth...)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("ip-overlap: got %d (%s)", res.StatusCode, body)
		}
		r := decodeAC(t, body)
		if len(r.Clusters) != 1 {
			t.Fatalf("want one cluster, got %d (%s)", len(r.Clusters), body)
		}
		// Two distinct teams, not three users: intra-team submissions collapse to one account.
		if r.Clusters[0].AccountCount != 2 || !containsAll(r.Clusters[0].AccountIDs, t1, t2) {
			t.Errorf("cluster should be the two teams, got count=%d ids=%v", r.Clusters[0].AccountCount, r.Clusters[0].AccountIDs)
		}
		if clusterName(&r, t1) != "red" || clusterName(&r, t2) != "blue" {
			t.Errorf("cluster names: t1=%q t2=%q, want red/blue (names %v ids %v)",
				clusterName(&r, t1), clusterName(&r, t2), r.Clusters[0].AccountNames, r.Clusters[0].AccountIDs)
		}
	})
}

func containsAll(ids []int64, want ...int64) bool {
	set := make(map[int64]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	for _, w := range want {
		if !set[w] {
			return false
		}
	}
	return true
}

func neighborsInclude(r *acReport, id int64) bool {
	for _, n := range r.IPOverlap {
		if n.OtherAccountID == id {
			return true
		}
	}
	return false
}
