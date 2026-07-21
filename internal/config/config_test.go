package config_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"maps"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/starvy/flagfish/internal/config"
	"github.com/starvy/flagfish/internal/domain/account"
	"github.com/starvy/flagfish/internal/domain/policy"
)

// mapStore is a Store for tests. The PGStore is exercised against a real Postgres
// in the integration suite — a mock cannot fail the way a database can, and this
// one is here only to test the parsing, which is where the bugs are.
type mapStore struct {
	mu   sync.Mutex
	rows map[string]string
	// mode is the instance singleton's account model. nil models a not-yet-set-up
	// instance, where there is no instance row and Mode is not yet meaningful.
	mode *account.Mode
}

func (s *mapStore) All(context.Context) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.rows), nil
}

func (s *mapStore) Mode(context.Context) (account.Mode, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mode == nil {
		return 0, false, nil
	}
	return *s.mode, true, nil
}

func modep(m account.Mode) *account.Mode { return &m }

func (s *mapStore) Replace(_ context.Context, kv map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	maps.Copy(s.rows, kv)
	return nil
}

func sane() map[string]string {
	return map[string]string{
		"setup":                "true",
		"ctf_name":             "flagfish CTF",
		"user_mode":            "teams",
		"challenge_visibility": "private",
		"score_visibility":     "public",
		"verify_emails":        "false",
		"start":                "1784030400",
		"end":                  "1784116800",
		"freeze":               "1784102400",
	}
}

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestBuildTypesEverythingOnce(t *testing.T) {
	snap, err := config.Build(sane(), modep(account.ModeTeams))
	if err != nil {
		t.Fatal(err)
	}

	if snap.CTFName != "flagfish CTF" {
		t.Errorf("CTFName = %q", snap.CTFName)
	}
	if snap.Mode != account.ModeTeams {
		t.Errorf("Mode = %v", snap.Mode)
	}
	if !snap.SetupDone {
		t.Error("SetupDone = false")
	}
	if snap.ChallengeVis != policy.VisPrivate {
		t.Errorf("ChallengeVis = %v", snap.ChallengeVis)
	}
	if snap.Freeze == nil || !snap.Freeze.Equal(time.Unix(1784102400, 0).UTC()) {
		t.Errorf("Freeze = %v", snap.Freeze)
	}
	// A default survives an absent row.
	if snap.Theme != "core" {
		t.Errorf("Theme = %q, want the default", snap.Theme)
	}
	if !snap.TeamCreation {
		t.Error("team_creation defaults to true")
	}
}

// Coercing on read (all-digit -> int, "true"/"false" -> bool) makes a value that
// is legitimately the string "12345" come back as an int at every call site.
// Here types come from the declared schema, never from the shape of the value:
// a string key holding digits stays a string.
func TestNoCoercionOnRead(t *testing.T) {
	rows := sane()
	rows["ctf_name"] = "12345" // a CTF really can be called that
	rows["ctf_description"] = "true"

	snap, err := config.Build(rows, nil)
	if err != nil {
		t.Fatal(err)
	}
	if snap.CTFName != "12345" {
		t.Errorf("CTFName = %q — the value's SHAPE must not decide its type", snap.CTFName)
	}
	if snap.CTFDescription != "true" {
		t.Errorf("CTFDescription = %q", snap.CTFDescription)
	}
}

// A malformed value fails at boot, with the key named. It does not become a
// default, and it does not become the wrong type.
func TestMalformedValueFailsAtBoot(t *testing.T) {
	for _, tc := range []struct {
		key, value, wantIn string
	}{
		{"user_mode", "solo", "user_mode"},
		{"verify_emails", "yes", "verify_emails"}, // not a bool spelling we accept
		{"verify_emails", "1", "verify_emails"},   // nor this one
		{"num_users", "lots", "num_users"},
		{"num_users", "-5", "num_users"},
		{"start", "2026-07-14", "start"}, // not a unix timestamp
		{"challenge_visibility", "invisible", "challenge_visibility"},
		// hidden is score-only; mlc is registration-only. Upstream, storing one on
		// the wrong key 500s at request time. Here it cannot be stored.
		{"challenge_visibility", "hidden", "hidden"},
		{"account_visibility", "hidden", "hidden"},
		{"score_visibility", "mlc", "mlc"},
		// theme_tokens is the admin rebrand blob: only a JSON object of strings. A
		// malformed one fails here rather than reaching a browser that drops it.
		{"theme_tokens", "{not json", "theme_tokens"},
		{"theme_tokens", "[1,2,3]", "theme_tokens"},
		{"theme_tokens", `{"color-bg":1}`, "theme_tokens"},
		// The webhook endpoint must be an http(s) URL, and the event list only names
		// events we know how to deliver. A typo in either fails at the write.
		{"webhook_url", "ftp://discord.example/hook", "webhook_url"},
		{"webhook_url", "not a url", "webhook_url"},
		{"webhook_events", "first_blood,bogus", "webhook_events"},
	} {
		rows := sane()
		rows[tc.key] = tc.value

		_, err := config.Build(rows, nil)
		if err == nil {
			t.Errorf("%s=%q was accepted; it must fail at boot", tc.key, tc.value)
			continue
		}
		if !strings.Contains(err.Error(), tc.wantIn) {
			t.Errorf("%s=%q: error %q does not mention %q — an operator cannot act on it",
				tc.key, tc.value, err, tc.wantIn)
		}
	}
}

// A well-formed rebrand blob is stored verbatim for the SPA to apply.
func TestThemeTokensAcceptsAJSONObject(t *testing.T) {
	rows := sane()
	rows["ctf_theme"] = "terminal"
	rows["theme_tokens"] = `{"color-accent":"#ff00ff","radius":"0px"}`

	snap, err := config.Build(rows, nil)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Theme != "terminal" {
		t.Errorf("Theme = %q", snap.Theme)
	}
	if snap.ThemeTokens != `{"color-accent":"#ff00ff","radius":"0px"}` {
		t.Errorf("ThemeTokens = %q — the blob must round-trip verbatim", snap.ThemeTokens)
	}
}

// Every problem, in one boot. Not one per restart.
func TestBuildReportsEveryProblemAtOnce(t *testing.T) {
	rows := sane()
	rows["user_mode"] = "solo"
	rows["num_users"] = "many"
	rows["paused"] = "maybe"

	_, err := config.Build(rows, nil)
	if err == nil {
		t.Fatal("want an error")
	}
	for _, key := range []string{"user_mode", "num_users", "paused"} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("the error does not mention %q; an operator would fix these one restart at a time", key)
		}
	}
}

// Cross-key validation: things that are only wrong in combination.
func TestCombinationsThatCannotBeRight(t *testing.T) {
	for _, tc := range []struct {
		name string
		mut  func(map[string]string)
	}{
		{"end before start", func(r map[string]string) { r["end"] = "1784000000" }},
		{"freeze before start", func(r map[string]string) { r["freeze"] = "1784000000" }},
		{"freeze after end", func(r map[string]string) { r["freeze"] = "1785000000" }},
		{"team_size in users mode", func(r map[string]string) { r["user_mode"] = "users"; r["team_size"] = "4" }},
		{"webhook enabled without a url", func(r map[string]string) { r["webhook_enabled"] = "true" }},
		{"verify_emails without a mailer", func(r map[string]string) { r["verify_emails"] = "true" }},
	} {
		rows := sane()
		tc.mut(rows)
		if _, err := config.Build(rows, nil); err == nil {
			t.Errorf("%s: accepted, want a boot failure", tc.name)
		}
	}
}

// verify_emails is the one toggle that can lock out every player at once: it gates
// every gameplay route on a flag that only a delivered email can clear. Without a
// mailer the whole player base registers into a 403 and nobody finds out until the
// complaints arrive, so the config must not boot.
func TestVerifyEmailsRequiresAMailer(t *testing.T) {
	withMailer := func(r map[string]string) {
		r["mail_server"] = "smtp.example.com"
		r["mail_port"] = "587"
		r["mailfrom_addr"] = "ctf@example.com"
	}

	for _, tc := range []struct {
		name string
		mut  func(map[string]string)
		// wantIn is a key the boot failure must name. Empty means the config is legitimate.
		wantIn string
	}{
		{name: "on with no mail at all", wantIn: "mail_server", mut: func(r map[string]string) {
			r["verify_emails"] = "true"
		}},
		{name: "on with a blank mail_server row", wantIn: "mail_server", mut: func(r map[string]string) {
			withMailer(r)
			r["verify_emails"] = "true"
			r["mail_server"] = "   " // an empty row means "unset", and unset is not a mailer
		}},
		// Half a mailer is still no mailer, and the operator gets both halves in one boot.
		{name: "on with a server but no from address", wantIn: "mailfrom_addr", mut: func(r map[string]string) {
			r["verify_emails"] = "true"
			r["mail_server"] = "smtp.example.com"
			r["mail_port"] = "587"
		}},
		// The posture of most instances. It must stay valid, mail or no mail.
		{name: "off with no mail at all", mut: func(map[string]string) {}},
		{name: "off with a full mailer", mut: withMailer},
		{name: "on with a full mailer", mut: func(r map[string]string) {
			withMailer(r)
			r["verify_emails"] = "true"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := sane()
			tc.mut(rows)

			_, err := config.Build(rows, modep(account.ModeTeams))
			switch {
			case tc.wantIn == "":
				if err != nil {
					t.Fatalf("a legitimate config was refused: %v", err)
				}
			case err == nil:
				t.Fatal("accepted: every registration would land in a 403 no email can clear")
			case !strings.Contains(err.Error(), tc.wantIn):
				t.Errorf("error %q does not name %q — an operator cannot act on it", err, tc.wantIn)
			}
		})
	}
}

// The same lockout, one step earlier: enabling verification without a mailer is a
// write nobody can undo from the inside, so the write path refuses it for the same
// reason boot does — and the repair stays expressible in a single write.
func TestSetRefusesEmailVerificationWithoutAMailer(t *testing.T) {
	ctx := context.Background()
	mailer := map[string]string{
		"mail_server":   "smtp.example.com",
		"mail_port":     "587",
		"mailfrom_addr": "ctf@example.com",
	}

	t.Run("turning it on with no mailer is refused whole", func(t *testing.T) {
		store := &mapStore{rows: sane(), mode: modep(account.ModeTeams)}
		m, err := config.New(ctx, store, discard())
		if err != nil {
			t.Fatal(err)
		}

		err = m.Set(ctx, map[string]string{"verify_emails": "true"})
		if !errors.Is(err, config.ErrRejected) {
			t.Fatalf("verify_emails without a mailer: got %v, want ErrRejected", err)
		}
		if !strings.Contains(err.Error(), "mail_server") {
			t.Errorf("the refusal does not name the missing key: %v", err)
		}
		if m.Current().VerifyEmails {
			t.Error("the live snapshot changed despite the write being rejected")
		}
		rows, allErr := store.All(ctx)
		if allErr != nil {
			t.Fatal(allErr)
		}
		if rows["verify_emails"] != "false" {
			t.Error("the refused write reached the store")
		}
	})

	t.Run("pulling the mailer out from under it is refused too", func(t *testing.T) {
		rows := sane()
		rows["verify_emails"] = "true"
		maps.Copy(rows, mailer)
		m, err := config.New(ctx, &mapStore{rows: rows, mode: modep(account.ModeTeams)}, discard())
		if err != nil {
			t.Fatal(err)
		}

		if err := m.Set(ctx, map[string]string{"mail_server": ""}); !errors.Is(err, config.ErrRejected) {
			t.Fatalf("clearing mail_server under verify_emails: got %v, want ErrRejected", err)
		}
		if m.Current().MailServer == "" {
			t.Error("the live snapshot lost the mailer despite the write being rejected")
		}
	})

	t.Run("one write turns it on and configures the mailer", func(t *testing.T) {
		store := &mapStore{rows: sane(), mode: modep(account.ModeTeams)}
		m, err := config.New(ctx, store, discard())
		if err != nil {
			t.Fatal(err)
		}

		kv := maps.Clone(mailer)
		kv["verify_emails"] = "true"
		if err := m.Set(ctx, kv); err != nil {
			t.Fatalf("turning verification on together with its mailer was refused: %v", err)
		}
		if snap := m.Current(); !snap.VerifyEmails || snap.MailServer != "smtp.example.com" {
			t.Errorf("the write did not land: verify=%v server=%q", snap.VerifyEmails, snap.MailServer)
		}
		if got := m.Problems(); len(got) != 0 {
			t.Errorf("Problems() = %q, want none", got)
		}
	})

	t.Run("an instance seeded broken is repairable by writing the mailer", func(t *testing.T) {
		store := &mapStore{rows: sane(), mode: modep(account.ModeTeams)}
		m, err := config.New(ctx, store, discard())
		if err != nil {
			t.Fatal(err)
		}
		store.rows["verify_emails"] = "true" // out of band: after boot, behind the manager's back

		if err := m.Set(ctx, map[string]string{"ctf_name": "locked out"}); err != nil {
			t.Fatalf("a disjoint write was refused over incoherence it did not touch: %v", err)
		}
		if problems := strings.Join(m.Problems(), "\n"); !strings.Contains(problems, "mail_server") {
			t.Fatalf("Problems() does not name the lockout: %q", problems)
		}
		if err := m.Set(ctx, mailer); err != nil {
			t.Fatalf("the repair was refused: %v", err)
		}
		if got := m.Problems(); len(got) != 0 {
			t.Errorf("Problems() = %q after the repair, want none", got)
		}
	})

	// The other repair, and the one an organizer reaches for mid-event: a locked-out
	// instance must always be able to drop the gate without first finding an SMTP server.
	t.Run("turning it back off unbricks an instance with no mailer", func(t *testing.T) {
		store := &mapStore{rows: sane(), mode: modep(account.ModeTeams)}
		m, err := config.New(ctx, store, discard())
		if err != nil {
			t.Fatal(err)
		}
		store.rows["verify_emails"] = "true" // out of band: after boot, behind the manager's back

		if err := m.Set(ctx, map[string]string{"verify_emails": "false"}); err != nil {
			t.Fatalf("turning verification off was refused, leaving the lockout in place: %v", err)
		}
		if m.Current().VerifyEmails {
			t.Error("the gate is still up")
		}
		if got := m.Problems(); len(got) != 0 {
			t.Errorf("Problems() = %q after the gate came down, want none", got)
		}
	})
}

// An unknown key is a row, not an error and not a migration. This is what lets the
// importer ingest arbitrary plugin keys from an imported archive, and it is the
// reason storage stayed EAV.
func TestUnknownKeysArePreservedVerbatim(t *testing.T) {
	rows := sane()
	rows["ctftime_plugin_secret"] = "12345"
	rows["some_future_key"] = "true"

	snap, err := config.Build(rows, nil)
	if err != nil {
		t.Fatalf("an unknown key must not fail the boot: %v", err)
	}

	v, ok := snap.Raw("ctftime_plugin_secret")
	if !ok || v != "12345" {
		t.Errorf("Raw(ctftime_plugin_secret) = %q, %v — unknown keys must round-trip verbatim, as strings", v, ok)
	}
	if _, ok := snap.Raw("nope"); ok {
		t.Error("Raw invented a key")
	}
}

// Secret is what a shareable export consults, so the answer for a key nobody declared has to be
// "withhold it". The key space is open — the importer carries foreign plugin keys in verbatim — and
// a denylist over an open key space leaks the moment someone invents the next key.
func TestSecretIsDefaultDeny(t *testing.T) {
	secret := []string{
		"mail_password", "mail_username", "mail_server", "webhook_url",
		"ctftime_plugin_secret", "some_future_key", "",
	}
	public := []string{
		"ctf_name", "ctf_description", "ctf_theme", "theme_tokens", "user_mode", "setup",
		"challenge_visibility", "score_visibility", "account_visibility", "registration_visibility",
		"verify_emails", "view_after_ctf", "paused", "team_creation",
		"start", "end", "freeze", "num_users", "num_teams", "team_size",
		"mail_port", "mail_tls", "mailfrom_addr", "webhook_enabled", "webhook_events",
	}
	for _, key := range secret {
		if !config.Secret(key) {
			t.Errorf("Secret(%q) = false, want true", key)
		}
	}
	for _, key := range public {
		if config.Secret(key) {
			t.Errorf("Secret(%q) = true: withholding it would silently drop the operator's data", key)
		}
	}
}

func TestEmptyRowMeansDefault(t *testing.T) {
	rows := sane()
	rows["ctf_theme"] = ""
	rows["team_creation"] = "" // not false: an unset row is unset, not disabled

	snap, err := config.Build(rows, nil)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Theme != "core" {
		t.Errorf("Theme = %q, want the default", snap.Theme)
	}
	if !snap.TeamCreation {
		t.Error("an empty row must fall back to the default (true), not to the zero value (false)")
	}
}

// The snapshot is swapped wholesale, and a read is a pointer dereference.
func TestManagerSwapsTheSnapshotWholesale(t *testing.T) {
	ctx := context.Background()
	store := &mapStore{rows: sane(), mode: modep(account.ModeTeams)}

	m, err := config.New(ctx, store, discard())
	if err != nil {
		t.Fatal(err)
	}

	before := m.Current()
	if before.Paused {
		t.Fatal("not paused yet")
	}

	if err := m.Set(ctx, map[string]string{"paused": "true"}); err != nil {
		t.Fatal(err)
	}

	after := m.Current()
	if !after.Paused {
		t.Error("the write is not visible to the writer's own next read")
	}
	if before.Paused {
		t.Error("the OLD snapshot was mutated. It must be immutable: a reader holding it sees a " +
			"consistent world, or it sees nothing")
	}
	if before == after {
		t.Error("the snapshot pointer did not change; it must be swapped, not patched")
	}
}

// A write that would not survive a boot is refused at the write — the same
// principle as failing at boot, moved one step earlier — and the live snapshot is
// left alone.
func TestSetRejectsAValueThatWouldNotBoot(t *testing.T) {
	ctx := context.Background()
	store := &mapStore{rows: sane(), mode: modep(account.ModeTeams)}

	m, err := config.New(ctx, store, discard())
	if err != nil {
		t.Fatal(err)
	}

	if setErr := m.Set(ctx, map[string]string{"user_mode": "solo"}); setErr == nil {
		t.Fatal("a value that fails validation must not be storable")
	}
	if m.Current().Mode != account.ModeTeams {
		t.Error("the live snapshot changed despite the write being rejected")
	}
	got, err := store.All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got["user_mode"] != "teams" {
		t.Error("the rejected value reached the store")
	}
}

// A broken config at boot is fatal. There is no fallback to defaults, because
// running on a config nobody validated is the failure this package exists to
// prevent.
func TestNewRefusesToStartOnABrokenConfig(t *testing.T) {
	rows := sane()
	rows["score_visibility"] = "nonsense"

	if _, err := config.New(context.Background(), &mapStore{rows: rows}, discard()); err == nil {
		t.Fatal("the process must not start on a config that does not parse")
	}
}

// Set judges coherence rule by rule: a write is refused only for incoherence
// among the keys it touches. Anything else — incoherence seeded out of band,
// which boot would have refused — is tolerated loudly, or the config API of a
// running instance is bricked by a table no route can repair.
func TestSetJudgesCoherenceOnlyAmongWrittenKeys(t *testing.T) {
	ctx := context.Background()

	// Half an SMTP config: mail_server with no mailfrom_addr and no mail_port.
	seedIncoherentMail := func(s *mapStore) {
		s.rows["mail_server"] = "smtp.seeded.example"
	}

	t.Run("a disjoint write over pre-existing incoherence is accepted and logged", func(t *testing.T) {
		store := &mapStore{rows: sane(), mode: modep(account.ModeTeams)}
		var logbuf strings.Builder
		m, err := config.New(ctx, store, slog.New(slog.NewTextHandler(&logbuf, nil)))
		if err != nil {
			t.Fatal(err)
		}
		seedIncoherentMail(store) // out of band: after boot, behind the manager's back

		if err := m.Set(ctx, map[string]string{"ctf_name": "still alive"}); err != nil {
			t.Fatalf("a write to ctf_name was refused over mail incoherence it did not touch: %v", err)
		}
		if m.Current().CTFName != "still alive" {
			t.Error("the write did not land")
		}
		if !strings.Contains(logbuf.String(), "incoheren") {
			t.Error("tolerated incoherence must be logged loudly, not waved through in silence")
		}
		problems := m.Problems()
		if len(problems) == 0 {
			t.Fatal("Problems() is empty: the operator who can repair the table is never told it is broken")
		}
		if !strings.Contains(strings.Join(problems, "\n"), "mailfrom_addr") {
			t.Errorf("Problems() does not name the missing key: %q", problems)
		}
	})

	t.Run("a write touching a violated rule's keys is refused whole", func(t *testing.T) {
		store := &mapStore{rows: sane(), mode: modep(account.ModeTeams)}
		m, err := config.New(ctx, store, discard())
		if err != nil {
			t.Fatal(err)
		}

		err = m.Set(ctx, map[string]string{"mail_server": "smtp.example.com", "mail_port": "587"})
		if !errors.Is(err, config.ErrRejected) {
			t.Fatalf("mail_server without mailfrom_addr: got %v, want ErrRejected", err)
		}
		rows, allErr := store.All(ctx)
		if allErr != nil {
			t.Fatal(allErr)
		}
		if _, ok := rows["mail_server"]; ok {
			t.Error("the refused write reached the store")
		}
	})

	t.Run("a repairing write is accepted and clears the problems", func(t *testing.T) {
		store := &mapStore{rows: sane(), mode: modep(account.ModeTeams)}
		m, err := config.New(ctx, store, discard())
		if err != nil {
			t.Fatal(err)
		}
		seedIncoherentMail(store)

		if err := m.Set(ctx, map[string]string{"mailfrom_addr": "ops@example.com", "mail_port": "587"}); err != nil {
			t.Fatalf("the repair was refused: %v", err)
		}
		if got := m.Problems(); len(got) != 0 {
			t.Errorf("Problems() = %q after the repair, want none", got)
		}
	})

	t.Run("boot on an incoherent table still fails whole", func(t *testing.T) {
		rows := sane()
		rows["mail_server"] = "smtp.seeded.example"
		if _, err := config.New(ctx, &mapStore{rows: rows, mode: modep(account.ModeTeams)}, discard()); err == nil {
			t.Fatal("a process must not start on half an SMTP config; the gentler write-path rule is for running instances only")
		}
	})
}

// The projection onto the policy layer, including the phase derivation.
func TestSnapshotProjectsOntoThePolicyEvent(t *testing.T) {
	snap, err := config.Build(sane(), modep(account.ModeTeams))
	if err != nil {
		t.Fatal(err)
	}

	start := time.Unix(1784030400, 0).UTC()
	end := time.Unix(1784116800, 0).UTC()

	for _, tc := range []struct {
		now  time.Time
		want policy.Phase
	}{
		{start.Add(-time.Hour), policy.PhaseBeforeStart},
		{start, policy.PhaseBeforeStart}, // strict: exactly at start is not started
		{start.Add(time.Hour), policy.PhaseRunning},
		{end, policy.PhaseRunning}, // strict: exactly at end is still running
		{end.Add(time.Second), policy.PhaseEnded},
	} {
		if got := snap.Event(tc.now).Phase; got != tc.want {
			t.Errorf("at %s: phase = %s, want %s", tc.now, got, tc.want)
		}
	}

	e := snap.Event(start.Add(time.Hour))
	if e.Mode != account.ModeTeams || e.ChallengeVis != policy.VisPrivate || e.FreezeAt == nil {
		t.Errorf("the projection dropped a field: %+v", e)
	}
}
