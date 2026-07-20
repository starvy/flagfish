// Package config is the one typed door in front of the EAV config table.
//
// Three properties, each deleting a class of bug:
//
//  1. EAV storage, typed accessor. The storage shape stays open because the
//     importer must ingest arbitrary config keys from imported archives —
//     including plugin-authored keys it has never heard of. Under a typed table
//     an unknown key is a migration or a data-loss event; here it is a row. The
//     EAV shape does real work at the boundary and never leaks past it.
//
//  2. Parse and validate once, at load. A malformed value fails at boot with a
//     clear error; there is no coercion on read. Inferring a value's type on
//     every read (all-digit -> int, "true"/"false" -> bool) means the string
//     "12345" comes back as an int — we never guess.
//
//  3. The whole table lives in memory behind an atomic.Pointer. It is ~100 rows
//     that change a few times per event, so a read is a pointer dereference: no
//     cache, no TTL, no invalidation graph, nothing to get wrong.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	stdmail "net/mail"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/starvy/flagfish/internal/domain/account"
	"github.com/starvy/flagfish/internal/domain/policy"
)

// A Snapshot is the entire config table, parsed, validated, and immutable.
//
// Never construct one by hand outside this package: Build is what guarantees the
// fields agree with each other.
type Snapshot struct {
	CTFName        string
	CTFDescription string
	Theme          string

	// ThemeTokens is an admin-set JSON object of semantic-token overrides that lets an
	// org rebrand without shipping code. Stored and served verbatim; the SPA validates
	// and applies it. Empty means no override.
	ThemeTokens string

	// Mode is the account model, and its one home is the instance singleton — the
	// same row the gameplay SQL keys on — read once at load. The config table is not
	// its source; a config user_mode key is legacy and only tolerated when it agrees.
	// account.AssertModeAtBoot is what earns the right to trust it.
	Mode      account.Mode
	SetupDone bool

	ChallengeVis    policy.Vis
	ScoreVis        policy.Vis
	AccountVis      policy.Vis
	RegistrationVis policy.Vis

	VerifyEmails bool
	ViewAfterCTF bool
	Paused       bool
	TeamCreation bool

	Start  *time.Time
	End    *time.Time
	Freeze *time.Time

	// Caps. Zero means unlimited, which is what an absent/empty row means upstream.
	NumUsers int
	NumTeams int
	TeamSize int

	// SMTP settings. MailServer empty means mail is not configured; the mailer built
	// from that state fails every send loudly rather than pretending to deliver.
	MailServer   string
	MailPort     int
	MailUsername string
	MailPassword string
	MailTLS      bool // STARTTLS
	MailFrom     string

	// Webhook (Discord-compatible) announcement feed. WebhookURL empty or
	// WebhookEnabled false means nothing is delivered; the worker treats that as "off",
	// not as an error. The URL is a credential and lives in the config table, never on
	// disk.
	WebhookURL     string
	WebhookEnabled bool
	WebhookEvents  WebhookEventSet

	// raw holds every key, including the ones this struct has never heard of:
	// plugin keys, keys from an imported archive. They are preserved verbatim so
	// an import round-trips, and are reachable only through Raw() — a caller that
	// wants one has to say so, and gets a string, not a guess.
	raw map[string]string
}

// Raw returns an unmodelled config value exactly as stored. No coercion. If you
// find yourself parsing the result of this at a call site, the key belongs in the
// typed struct instead.
func (s *Snapshot) Raw(key string) (string, bool) {
	v, ok := s.raw[key]
	return v, ok
}

// WebhookEvent is a kind of event the announcement feed can be told to deliver.
type WebhookEvent string

const (
	WebhookEventFirstBlood WebhookEvent = "first_blood"
	WebhookEventSolve      WebhookEvent = "solve"
)

// ParseWebhookEvent rejects any spelling that is not a known event, so a typo in
// webhook_events fails at the write instead of silently disabling an announcement.
func ParseWebhookEvent(s string) (WebhookEvent, error) {
	switch WebhookEvent(s) {
	case WebhookEventFirstBlood, WebhookEventSolve:
		return WebhookEvent(s), nil
	default:
		return "", fmt.Errorf("unknown webhook event %q (want %q or %q)", s, WebhookEventFirstBlood, WebhookEventSolve)
	}
}

// WebhookEventSet is the set of events the feed delivers. A nil set delivers nothing.
type WebhookEventSet map[WebhookEvent]bool

// Enabled reports whether the feed is configured to deliver this event.
func (s WebhookEventSet) Enabled(e WebhookEvent) bool { return s[e] }

// Keys returns every registered key name, sorted. It is how a sweep outside this
// package walks the registry; whether a key is typed or disclosable is still
// Modelled's and Secret's to answer.
func Keys() []string {
	return slices.Sorted(maps.Keys(registry))
}

// Modelled reports whether a key has a typed setter. A key that is not modelled is
// still valid: it is preserved verbatim in raw. The importer uses this only to name
// preserved keys in its report, never to decide whether to keep one.
func Modelled(key string) bool {
	def, ok := registry[key]
	return ok && def.set != nil
}

// Secret reports whether a key's value must never leave the instance in a shareable
// artifact. It is default-deny: only a key this package declares public is public, so
// an unknown key — a plugin key, a key carried in from a foreign archive, a key some
// future version adds — is a secret until someone here says otherwise. A leak then
// requires a deliberate act, not an omission.
func Secret(key string) bool {
	def, ok := registry[key]
	return !ok || def.secret
}

// Event projects the snapshot onto the policy layer's view of the world. `now`
// decides the phase, and it is passed in rather than read from the clock so the
// policy layer stays testable and this stays a pure projection.
func (s *Snapshot) Event(now time.Time) policy.Event {
	return policy.Event{
		Mode:            s.Mode,
		SetupDone:       s.SetupDone,
		ChallengeVis:    s.ChallengeVis,
		ScoreVis:        s.ScoreVis,
		AccountVis:      s.AccountVis,
		RegistrationVis: s.RegistrationVis,
		VerifyEmails:    s.VerifyEmails,
		ViewAfterCTF:    s.ViewAfterCTF,
		Phase:           policy.PhaseAt(now, s.Start, s.End),
		Paused:          s.Paused,
		FreezeAt:        s.Freeze,
		TeamCreation:    s.TeamCreation,
	}
}

// defaults are the values a key takes when its row is absent or empty, so an
// instance with an empty config table is a sane instance rather than an undefined one.
func defaults() Snapshot {
	return Snapshot{
		Theme:           "core",
		Mode:            account.ModeUsers,
		ChallengeVis:    policy.VisPrivate,
		ScoreVis:        policy.VisPublic,
		AccountVis:      policy.VisPublic,
		RegistrationVis: policy.VisPublic,
		TeamCreation:    true,
		// First blood is the announcement worth having; default the set so turning on
		// webhook_enabled with a URL just works, without a second key to remember.
		WebhookEvents: WebhookEventSet{WebhookEventFirstBlood: true, WebhookEventSolve: false},
	}
}

// setter parses one key into the snapshot. A setter never guesses: it either
// understands the value or it returns an error that names the key and the value.
type setter func(*Snapshot, string) error

// keyDef is what this package knows about one config key: how to parse it, and
// whether its value may leave the instance. Sensitivity is declared here because this
// is the only place that knows what a key MEANS — a substring guess elsewhere ("does
// it contain 'token'?") both misses webhook_url, a live bearer capability, and
// withholds theme_tokens, which is branding.
type keyDef struct {
	// set is nil for a key we recognise but do not source from the config table.
	set    setter
	secret bool
}

// public and secret declare a key's sensitivity at its definition, so adding a key is
// a choice about disclosure, not an oversight. Anything absent from the registry is
// secret by default — see Secret.
func public(s setter) keyDef { return keyDef{set: s} }
func secret(s setter) keyDef { return keyDef{set: s, secret: true} }

// registry is the declared schema. A key in here is typed; a key not in here is a
// string in raw.
var registry = map[string]keyDef{
	"ctf_name":        public(func(s *Snapshot, v string) error { s.CTFName = v; return nil }),
	"ctf_description": public(func(s *Snapshot, v string) error { s.CTFDescription = v; return nil }),
	"ctf_theme":       public(func(s *Snapshot, v string) error { s.Theme = v; return nil }),
	"theme_tokens":    public(themeTokensSetter),

	// user_mode has no setter: the account model lives in the instance singleton, not
	// here. A stray config user_mode key is preserved verbatim in raw and checked for
	// agreement in Build, never used to source Mode. It is listed only to say it is not
	// a secret; without an entry, default-deny would withhold it.
	"user_mode": public(nil),

	"setup": public(boolSetter(func(s *Snapshot, b bool) { s.SetupDone = b })),

	"challenge_visibility":    public(visSetter(policy.VisChallenge, func(s *Snapshot, v policy.Vis) { s.ChallengeVis = v })),
	"score_visibility":        public(visSetter(policy.VisScore, func(s *Snapshot, v policy.Vis) { s.ScoreVis = v })),
	"account_visibility":      public(visSetter(policy.VisAccount, func(s *Snapshot, v policy.Vis) { s.AccountVis = v })),
	"registration_visibility": public(visSetter(policy.VisRegistration, func(s *Snapshot, v policy.Vis) { s.RegistrationVis = v })),

	"verify_emails":  public(boolSetter(func(s *Snapshot, b bool) { s.VerifyEmails = b })),
	"view_after_ctf": public(boolSetter(func(s *Snapshot, b bool) { s.ViewAfterCTF = b })),
	"paused":         public(boolSetter(func(s *Snapshot, b bool) { s.Paused = b })),
	"team_creation":  public(boolSetter(func(s *Snapshot, b bool) { s.TeamCreation = b })),

	"start":  public(timeSetter(func(s *Snapshot, t *time.Time) { s.Start = t })),
	"end":    public(timeSetter(func(s *Snapshot, t *time.Time) { s.End = t })),
	"freeze": public(timeSetter(func(s *Snapshot, t *time.Time) { s.Freeze = t })),

	"num_users": public(intSetter(func(s *Snapshot, n int) { s.NumUsers = n })),
	"num_teams": public(intSetter(func(s *Snapshot, n int) { s.NumTeams = n })),
	"team_size": public(intSetter(func(s *Snapshot, n int) { s.TeamSize = n })),

	// The SMTP triple is one credential: the host it authenticates to is as much a part
	// of it as the password, and a username alone is half a login.
	"mail_server":   secret(func(s *Snapshot, v string) error { s.MailServer = v; return nil }),
	"mail_username": secret(func(s *Snapshot, v string) error { s.MailUsername = v; return nil }),
	"mail_password": secret(func(s *Snapshot, v string) error { s.MailPassword = v; return nil }),
	"mail_port":     public(intSetter(func(s *Snapshot, n int) { s.MailPort = n })),
	"mail_tls":      public(boolSetter(func(s *Snapshot, b bool) { s.MailTLS = b })),
	"mailfrom_addr": public(func(s *Snapshot, v string) error {
		if _, err := stdmail.ParseAddress(v); err != nil {
			return fmt.Errorf("want an email address, got %q: %w", v, err)
		}
		s.MailFrom = v
		return nil
	}),

	// The webhook URL embeds its token in the path: whoever holds the URL can post as
	// the CTF. It is a credential that happens to be spelled like a link.
	"webhook_url":     secret(webhookURLSetter),
	"webhook_enabled": public(boolSetter(func(s *Snapshot, b bool) { s.WebhookEnabled = b })),
	"webhook_events":  public(webhookEventsSetter),
}

// webhookURLSetter accepts only an absolute http(s) URL. A malformed endpoint fails
// at the write rather than becoming a delivery that errors on every retry for the
// whole event.
func webhookURLSetter(s *Snapshot, v string) error {
	u, err := url.Parse(v)
	if err != nil {
		return fmt.Errorf("want an http(s) URL, got %q: %w", v, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("want an http(s) URL, got scheme %q", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("want a URL with a host, got %q", v)
	}
	s.WebhookURL = v
	return nil
}

// webhookEventsSetter parses a comma-separated list of event kinds. An unknown token
// fails loudly: an operator who misspells "first_blood" should find out here, not by
// wondering why the feed is silent.
func webhookEventsSetter(s *Snapshot, v string) error {
	set := WebhookEventSet{}
	for _, tok := range strings.Split(v, ",") {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		e, err := ParseWebhookEvent(tok)
		if err != nil {
			return err
		}
		set[e] = true
	}
	s.WebhookEvents = set
	return nil
}

// themeTokensSetter accepts only a JSON object whose every value is a string: the
// SPA reads it as semantic-token overrides. A malformed blob fails here, at the
// write, rather than reaching a browser that would silently drop it.
func themeTokensSetter(s *Snapshot, v string) error {
	var obj map[string]string
	dec := json.NewDecoder(strings.NewReader(v))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&obj); err != nil {
		return fmt.Errorf("want a JSON object of string values: %w", err)
	}
	s.ThemeTokens = v
	return nil
}

func boolSetter(assign func(*Snapshot, bool)) setter {
	return func(s *Snapshot, v string) error {
		// Exactly two spellings. Not "1", not "yes", not "True" — because accepting
		// them means some spelling is being rejected silently somewhere, and the
		// operator who typed it will never find out which.
		switch v {
		case "true":
			assign(s, true)
		case "false":
			assign(s, false)
		default:
			return fmt.Errorf("want \"true\" or \"false\", got %q", v)
		}
		return nil
	}
}

func intSetter(assign func(*Snapshot, int)) setter {
	return func(s *Snapshot, v string) error {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("want an integer, got %q", v)
		}
		if n < 0 {
			return fmt.Errorf("want a non-negative integer, got %d", n)
		}
		assign(s, n)
		return nil
	}
}

// timeSetter parses a unix timestamp, the stored form of start/end/freeze.
func timeSetter(assign func(*Snapshot, *time.Time)) setter {
	return func(s *Snapshot, v string) error {
		secs, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return fmt.Errorf("want a unix timestamp in seconds, got %q", v)
		}
		t := time.Unix(secs, 0).UTC()
		assign(s, &t)
		return nil
	}
}

// visSetter enforces which visibility values are legal for which key. `hidden` is
// score-only and `mlc` is registration-only; upstream, setting either one on the
// wrong key falls through the decorator and 500s at request time. Here it cannot
// be stored.
func visSetter(kind policy.VisKind, assign func(*Snapshot, policy.Vis)) setter {
	return func(s *Snapshot, v string) error {
		vis, err := policy.ParseVis(v)
		if err != nil {
			return err
		}
		switch {
		case vis == policy.VisHidden && kind != policy.VisScore:
			return errors.New(`"hidden" is only a legal value for score_visibility`)
		case vis == policy.VisMLC && kind != policy.VisRegistration:
			return errors.New(`"mlc" is only a legal value for registration_visibility`)
		}
		assign(s, vis)
		return nil
	}
}

// Build parses and validates the whole table, once.
//
// instanceMode is the authoritative account model, read from the instance singleton
// — the same row the gameplay SQL keys on. It is nil before setup, when there is no
// instance row yet and the mode is not meaningful; Mode then keeps its default. This
// is what makes the Go side and the SQL agree by construction: one fact, one home.
//
// It reports every problem it finds, not the first: an operator fixing a broken
// config should get the whole list in one boot, not discover the next one on the
// next restart.
func Build(rows map[string]string, instanceMode *account.Mode) (*Snapshot, error) {
	snap := defaults()
	snap.raw = make(map[string]string, len(rows))
	if instanceMode != nil {
		snap.Mode = *instanceMode
	}

	var problems []error
	for key, value := range rows {
		snap.raw[key] = value

		def, known := registry[key]
		if !known || def.set == nil {
			continue // an unknown or unsourced key is a string, preserved. Not an error.
		}
		if strings.TrimSpace(value) == "" {
			continue // an empty row means "unset": take the default.
		}
		if err := def.set(&snap, value); err != nil {
			problems = append(problems, fmt.Errorf("config key %q: %w", key, err))
		}
	}
	if err := checkModeAgreement(rows, instanceMode); err != nil {
		problems = append(problems, err)
	}

	if len(problems) > 0 {
		return nil, fmt.Errorf("invalid configuration (refusing to start):\n%w", errors.Join(problems...))
	}
	if err := snap.validate(); err != nil {
		return nil, err
	}
	return &snap, nil
}

// checkModeAgreement guards the one fact that, for backward compatibility, can still
// appear in two places. The account model is the instance's, full stop; a config
// user_mode key never sources it. But a config user_mode that contradicts the
// instance — or does not even parse — is the fingerprint of a corrupted instance or
// an archive imported into the wrong one, and a corrupted instance must not boot.
func checkModeAgreement(rows map[string]string, instanceMode *account.Mode) error {
	v, ok := rows["user_mode"]
	if !ok || strings.TrimSpace(v) == "" {
		return nil
	}
	m, err := account.ParseMode(v)
	if err != nil {
		return fmt.Errorf("config key %q: %w", "user_mode", err)
	}
	if instanceMode != nil && m != *instanceMode {
		return fmt.Errorf("config key %q is %q but the instance is set up as %q: the account "+
			"model lives in the instance, and a config user_mode that disagrees means a corrupted "+
			"instance or an archive imported into the wrong one", "user_mode", m, *instanceMode)
	}
	return nil
}

// A coherenceRule is one cross-key check: a constraint no single key's parser can
// see. keys names every config key the check reads, so a write can be judged
// against exactly the rules it could have changed.
type coherenceRule struct {
	keys  []string
	check func(*Snapshot) error
}

var coherenceRules = []coherenceRule{
	{keys: []string{"start", "end"}, check: func(s *Snapshot) error {
		if s.Start != nil && s.End != nil && !s.End.After(*s.Start) {
			return fmt.Errorf("end (%s) must be after start (%s)", s.End, s.Start)
		}
		return nil
	}},
	{keys: []string{"freeze", "start"}, check: func(s *Snapshot) error {
		if s.Freeze != nil && s.Start != nil && s.Freeze.Before(*s.Start) {
			return fmt.Errorf("freeze (%s) is before start (%s): the whole event would be frozen", s.Freeze, s.Start)
		}
		return nil
	}},
	{keys: []string{"freeze", "end"}, check: func(s *Snapshot) error {
		if s.Freeze != nil && s.End != nil && s.Freeze.After(*s.End) {
			return fmt.Errorf("freeze (%s) is after end (%s): it would never take effect", s.Freeze, s.End)
		}
		return nil
	}},
	// user_mode is listed even though Mode is sourced from the instance singleton:
	// the rule reads it, and team_size is the half a write can actually change.
	{keys: []string{"team_size", "user_mode"}, check: func(s *Snapshot) error {
		if s.Mode == account.ModeUsers && s.TeamSize > 0 {
			return errors.New("team_size is set but user_mode is \"users\": one of these is a mistake")
		}
		return nil
	}},
	// Mail settings are only wrong in combination: half an SMTP config would boot fine
	// and then fail on the first verification email of the event.
	{keys: []string{"mail_server", "mail_port"}, check: func(s *Snapshot) error {
		if s.MailServer != "" && (s.MailPort < 1 || s.MailPort > 65535) {
			return fmt.Errorf("mail_server is set but mail_port is %d: want 1-65535", s.MailPort)
		}
		return nil
	}},
	{keys: []string{"mail_server", "mailfrom_addr"}, check: func(s *Snapshot) error {
		if s.MailServer != "" && s.MailFrom == "" {
			return errors.New("mail_server is set but mailfrom_addr is not")
		}
		return nil
	}},
	{keys: []string{"mail_password", "mail_username"}, check: func(s *Snapshot) error {
		if s.MailPassword != "" && s.MailUsername == "" {
			return errors.New("mail_password is set but mail_username is not")
		}
		return nil
	}},
	// An enabled feed with no endpoint would boot fine and then fail on the first
	// first-blood of the event; refuse the half-configuration up front.
	{keys: []string{"webhook_enabled", "webhook_url"}, check: func(s *Snapshot) error {
		if s.WebhookEnabled && s.WebhookURL == "" {
			return errors.New("webhook_enabled is true but webhook_url is not set")
		}
		return nil
	}},
}

// A violation pairs a violated rule with what it has to say. The rule index is
// what lets a caller ask "was this same rule already violated before the write".
type violation struct {
	rule int
	err  error
}

// violations runs every coherence rule and reports each one the snapshot breaks.
func (s *Snapshot) violations() []violation {
	var out []violation
	for i, r := range coherenceRules {
		if err := r.check(s); err != nil {
			out = append(out, violation{rule: i, err: err})
		}
	}
	return out
}

// validate checks the things that are only wrong in combination — the ones no
// single key's parser can see.
func (s *Snapshot) validate() error {
	var problems []error
	for _, v := range s.violations() {
		problems = append(problems, v.err)
	}
	if len(problems) > 0 {
		return fmt.Errorf("invalid configuration (refusing to start):\n%w", errors.Join(problems...))
	}
	return nil
}
