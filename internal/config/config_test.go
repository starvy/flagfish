package config_test

import (
	"context"
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
	} {
		rows := sane()
		tc.mut(rows)
		if _, err := config.Build(rows, nil); err == nil {
			t.Errorf("%s: accepted, want a boot failure", tc.name)
		}
	}
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
