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
	stdmail "net/mail"
	"net/url"
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

// Modelled reports whether a key has a typed setter. A key that is not modelled is
// still valid: it is preserved verbatim in raw. The importer uses this only to name
// preserved keys in its report, never to decide whether to keep one.
func Modelled(key string) bool {
	_, ok := registry[key]
	return ok
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

// registry is the declared schema. A key in here is typed; a key not in here is a
// string in raw.
var registry = map[string]setter{
	"ctf_name":        func(s *Snapshot, v string) error { s.CTFName = v; return nil },
	"ctf_description": func(s *Snapshot, v string) error { s.CTFDescription = v; return nil },
	"ctf_theme":       func(s *Snapshot, v string) error { s.Theme = v; return nil },
	"theme_tokens":    themeTokensSetter,

	// user_mode is deliberately absent: the account model lives in the instance
	// singleton, not here. A stray config user_mode key is preserved verbatim in raw
	// and checked for agreement in Build, never used to source Mode.
	"setup": boolSetter(func(s *Snapshot, b bool) { s.SetupDone = b }),

	"challenge_visibility":    visSetter(policy.VisChallenge, func(s *Snapshot, v policy.Vis) { s.ChallengeVis = v }),
	"score_visibility":        visSetter(policy.VisScore, func(s *Snapshot, v policy.Vis) { s.ScoreVis = v }),
	"account_visibility":      visSetter(policy.VisAccount, func(s *Snapshot, v policy.Vis) { s.AccountVis = v }),
	"registration_visibility": visSetter(policy.VisRegistration, func(s *Snapshot, v policy.Vis) { s.RegistrationVis = v }),

	"verify_emails":  boolSetter(func(s *Snapshot, b bool) { s.VerifyEmails = b }),
	"view_after_ctf": boolSetter(func(s *Snapshot, b bool) { s.ViewAfterCTF = b }),
	"paused":         boolSetter(func(s *Snapshot, b bool) { s.Paused = b }),
	"team_creation":  boolSetter(func(s *Snapshot, b bool) { s.TeamCreation = b }),

	"start":  timeSetter(func(s *Snapshot, t *time.Time) { s.Start = t }),
	"end":    timeSetter(func(s *Snapshot, t *time.Time) { s.End = t }),
	"freeze": timeSetter(func(s *Snapshot, t *time.Time) { s.Freeze = t }),

	"num_users": intSetter(func(s *Snapshot, n int) { s.NumUsers = n }),
	"num_teams": intSetter(func(s *Snapshot, n int) { s.NumTeams = n }),
	"team_size": intSetter(func(s *Snapshot, n int) { s.TeamSize = n }),

	"mail_server":   func(s *Snapshot, v string) error { s.MailServer = v; return nil },
	"mail_port":     intSetter(func(s *Snapshot, n int) { s.MailPort = n }),
	"mail_username": func(s *Snapshot, v string) error { s.MailUsername = v; return nil },
	"mail_password": func(s *Snapshot, v string) error { s.MailPassword = v; return nil },
	"mail_tls":      boolSetter(func(s *Snapshot, b bool) { s.MailTLS = b }),
	"mailfrom_addr": func(s *Snapshot, v string) error {
		if _, err := stdmail.ParseAddress(v); err != nil {
			return fmt.Errorf("want an email address, got %q: %w", v, err)
		}
		s.MailFrom = v
		return nil
	},

	"webhook_url":     webhookURLSetter,
	"webhook_enabled": boolSetter(func(s *Snapshot, b bool) { s.WebhookEnabled = b }),
	"webhook_events":  webhookEventsSetter,
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

		set, known := registry[key]
		if !known {
			continue // an unknown key is a string, preserved. Not an error.
		}
		if strings.TrimSpace(value) == "" {
			continue // an empty row means "unset": take the default.
		}
		if err := set(&snap, value); err != nil {
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

// validate checks the things that are only wrong in combination — the ones no
// single key's parser can see.
func (s *Snapshot) validate() error {
	var problems []error

	if s.Start != nil && s.End != nil && !s.End.After(*s.Start) {
		problems = append(problems, fmt.Errorf("end (%s) must be after start (%s)", s.End, s.Start))
	}
	if s.Freeze != nil && s.Start != nil && s.Freeze.Before(*s.Start) {
		problems = append(problems, fmt.Errorf("freeze (%s) is before start (%s): the whole event would be frozen", s.Freeze, s.Start))
	}
	if s.Freeze != nil && s.End != nil && s.Freeze.After(*s.End) {
		problems = append(problems, fmt.Errorf("freeze (%s) is after end (%s): it would never take effect", s.Freeze, s.End))
	}
	if s.Mode == account.ModeUsers && s.TeamSize > 0 {
		problems = append(problems, errors.New("team_size is set but user_mode is \"users\": one of these is a mistake"))
	}

	// Mail settings are only wrong in combination: half an SMTP config would boot fine
	// and then fail on the first verification email of the event.
	if s.MailServer != "" {
		if s.MailPort < 1 || s.MailPort > 65535 {
			problems = append(problems, fmt.Errorf("mail_server is set but mail_port is %d: want 1-65535", s.MailPort))
		}
		if s.MailFrom == "" {
			problems = append(problems, errors.New("mail_server is set but mailfrom_addr is not"))
		}
	}
	if s.MailPassword != "" && s.MailUsername == "" {
		problems = append(problems, errors.New("mail_password is set but mail_username is not"))
	}

	// An enabled feed with no endpoint would boot fine and then fail on the first
	// first-blood of the event; refuse the half-configuration up front.
	if s.WebhookEnabled && s.WebhookURL == "" {
		problems = append(problems, errors.New("webhook_enabled is true but webhook_url is not set"))
	}

	if len(problems) > 0 {
		return fmt.Errorf("invalid configuration (refusing to start):\n%w", errors.Join(problems...))
	}
	return nil
}
