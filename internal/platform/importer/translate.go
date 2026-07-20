package importer

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/netip"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/starvy/flagfish/internal/config"
	"github.com/starvy/flagfish/internal/db"
	"github.com/starvy/flagfish/internal/domain/account"
	"github.com/starvy/flagfish/internal/domain/flags"
)

// Options tune a translation. The zero value is the supported, safe default: reject unknown
// revisions and unknown challenge types rather than guessing their semantics.
type Options struct {
	Version                   string // stamped into instance.version
	AssumeRevision            string // translate an unknown revision as this known one (loud, unsupported)
	ForceUnknownChallengeType string // import an unknown challenge type as this ('standard'); empty = hard fail
	Caps                      Caps
}

// Plan is the fully translated archive: slices ready to stream through COPY, plus the two
// instance-level facts the loader needs before it can restore. It carries no database handle and no
// I/O — Translate is pure, so every mapping decision is unit-testable without Postgres.
type Plan struct {
	UserMode string
	Version  string

	Brackets    []db.ImportBracketsParams
	Teams       []db.ImportTeamsParams
	Users       []db.ImportUsersParams
	Challenges  []db.ImportChallengesParams
	Files       []db.ImportFilesParams
	Flags       []db.ImportFlagsParams
	Tags        []db.ImportTagsParams
	Hints       []db.ImportHintsParams
	Submissions []db.ImportSubmissionsParams
	Solves      []db.ImportSolvesParams
	Awards      []db.ImportAwardsParams
	Config      []db.ImportConfigParams
}

// mappedTables are consumed directly by Translate; the rest of the archive is classified for the
// report so nothing disappears without a line naming it.
var mappedTables = map[string]bool{
	"alembic_version": true, "brackets": true, "teams": true, "users": true,
	"challenges": true, "dynamic_challenge": true, "files": true, "tags": true,
	"flags": true, "hints": true, "config": true, "submissions": true,
	"solves": true, "awards": true,
}

// deferredTables are known subsystems we do not model in v1. Their rows are counted, named, and
// dropped — an admin will ask about exactly these.
var deferredTables = map[string]bool{
	"notifications": true, "tracking": true, "fields": true, "field_entries": true,
	"unlocks": true, "comments": true, "pages": true, "page_files": true, "ratings": true,
	"topics": true, "challenge_topics": true, "solutions": true, "solution_files": true,
	"audiences": true, "audience_members": true, "modules": true, "module_audience_access": true,
}

// Translate maps an archive onto the schema. It returns a Plan and a Report; a hard failure (an
// unsolvable flag, an unknown challenge scoring rule, corrupt solve provenance, or config that would
// not survive a boot) is an error, not a silent drop.
func Translate(a *Archive, opts Options) (*Plan, *Report, error) {
	rep := newReport()
	rep.SourceRevision = a.Revision
	rep.SourceOrdinal = a.Ordinal
	rep.SourceHint = a.CTFdHint

	plan := &Plan{Version: opts.Version}
	if plan.Version == "" {
		plan.Version = "imported"
	}

	classifyTables(a, rep)

	// The two value-carrying stages hand results forward through these closures. Ordering is
	// FK-topological for the report's sake; the loader loads under suppressed foreign keys.
	var challengeValue map[int64]int32
	var subDates map[int64]pgtype.Timestamptz
	stages := []func() error{
		func() error { return translateConfig(a, plan, rep) },
		func() error { return translateBrackets(a, plan, rep) },
		func() error { return translateTeams(a, plan, rep) },
		func() error { return translateUsers(a, plan, rep) },
		func() error {
			var err error
			challengeValue, err = translateChallenges(a, plan, rep, opts)
			return err
		},
		func() error { return translateFlags(a, plan, rep) },
		func() error { return translateTags(a, plan, rep) },
		func() error { return translateHints(a, plan, rep) },
		func() error { return translateFiles(a, plan, rep) },
		func() error {
			var err error
			subDates, err = translateSubmissions(a, plan, rep)
			return err
		},
		func() error { return translateSolves(a, plan, rep, challengeValue, subDates) },
		func() error { return translateAwards(a, plan, rep) },
	}
	for _, stage := range stages {
		if err := stage(); err != nil {
			return nil, rep, err
		}
	}
	return plan, rep, nil
}

func classifyTables(a *Archive, rep *Report) {
	names := make([]string, 0, len(a.tables))
	for name := range a.tables {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		env := a.tables[name]
		switch {
		case mappedTables[name]:
			// consumed by a translate* below
		case name == "tokens":
			rep.note(SeverityInfo, CodeTokensDropped, "tokens", len(env.Results),
				"plaintext credentials are never imported")
		case deferredTables[name]:
			rep.note(SeverityInfo, CodeDeferredTable, name, len(env.Results),
				"subsystem not modelled in this version")
		default:
			rep.note(SeverityWarning, CodeUnmappedTable, name, len(env.Results),
				"no counterpart in the schema; there is no plugin system")
		}
	}
}

func translateConfig(a *Archive, plan *Plan, rep *Report) error {
	rows, err := decodeTable[ctfdConfig](a, "config")
	if err != nil {
		return err
	}
	rep.read("config", len(rows))

	// Duplicate keys are structurally possible in the source (no UNIQUE(key)). Resolve last-writer-
	// wins by id and report every collision.
	type winner struct {
		id  int64
		val *string
	}
	final := map[string]winner{}
	for _, c := range rows {
		if w, ok := final[c.Key]; ok {
			rep.note(SeverityWarning, CodeConfigDuplicate, "config", 0,
				fmt.Sprintf("key %q appears more than once; keeping the highest id", c.Key))
			if c.ID < w.id {
				continue
			}
		}
		final[c.Key] = winner{id: c.ID, val: c.Value}
	}

	keys := make([]string, 0, len(final))
	for k := range final {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// Validate the resolved config through the typed layer so a value that would fail at boot fails
	// here instead — loud, at import time. Unknown keys are preserved verbatim, not dropped.
	validate := map[string]string{}
	for _, k := range keys {
		if w := final[k]; w.val != nil {
			validate[k] = *w.val
		}
		if !config.Modelled(k) {
			rep.note(SeverityInfo, CodeConfigPreserved, "config", 0,
				fmt.Sprintf("key %q kept verbatim via the raw config seam", k))
		}
	}
	// The account model is the instance's; the archive's config user_mode is the input
	// that seeds it. Resolve it first, then validate the config against that same mode
	// so the boot check sees exactly what this instance will be set up as.
	plan.UserMode = "users"
	if m, ok := validate["user_mode"]; ok && strings.TrimSpace(m) != "" {
		plan.UserMode = m
	}
	mode, merr := account.ParseMode(plan.UserMode)
	if merr != nil {
		return fmt.Errorf("archive config: %w", merr)
	}
	if _, verr := config.Build(validate, &mode); verr != nil {
		return fmt.Errorf("archive config would not boot: %w", verr)
	}

	for _, k := range keys {
		plan.Config = append(plan.Config, db.ImportConfigParams{Key: k, Value: final[k].val})
	}
	rep.wrote("config", len(plan.Config))
	return nil
}

func translateBrackets(a *Archive, plan *Plan, rep *Report) error {
	rows, err := decodeTable[ctfdBracket](a, "brackets")
	if err != nil {
		return err
	}
	rep.read("brackets", len(rows))
	for _, b := range rows {
		if b.Type != "users" && b.Type != "teams" {
			return fmt.Errorf("bracket %d has unsupported type %q", b.ID, b.Type)
		}
		plan.Brackets = append(plan.Brackets, db.ImportBracketsParams{
			ID: b.ID, Name: b.Name, Description: b.Description, AppliesTo: b.Type,
		})
	}
	rep.wrote("brackets", len(plan.Brackets))
	return nil
}

func translateTeams(a *Archive, plan *Plan, rep *Report) error {
	rows, err := decodeTable[ctfdTeam](a, "teams")
	if err != nil {
		return err
	}
	rep.read("teams", len(rows))
	for _, t := range rows {
		// secret has no reader anywhere here, but it is carried verbatim so a round-tripped
		// instance loses nothing: import fidelity is the property, not a feature.
		plan.Teams = append(plan.Teams, db.ImportTeamsParams{
			ID: t.ID, Name: t.Name, Email: t.Email, PasswordHash: t.Password, Secret: t.Secret,
			Website: t.Website, Affiliation: t.Affiliation, Country: t.Country,
			BracketID: t.BracketID, CaptainID: t.CaptainID, Hidden: t.Hidden, Banned: t.Banned,
			CreatedAt: nowTS(),
		})
	}
	rep.wrote("teams", len(plan.Teams))
	return nil
}

func translateUsers(a *Archive, plan *Plan, rep *Report) error {
	rows, err := decodeTable[ctfdUser](a, "users")
	if err != nil {
		return err
	}
	rep.read("users", len(rows))
	for i := range rows {
		u := &rows[i]
		if strings.TrimSpace(u.Email) == "" {
			return fmt.Errorf("user %d has no email, which the schema requires", u.ID)
		}
		role := "user"
		if u.Type == "admin" {
			role = "admin"
		}
		plan.Users = append(plan.Users, db.ImportUsersParams{
			ID: u.ID, Name: u.Name, Email: u.Email, PasswordHash: u.Password, Role: role, Secret: u.Secret,
			Website: u.Website, Affiliation: u.Affiliation, Country: u.Country, Language: u.Language,
			BracketID: u.BracketID, TeamID: u.TeamID, Hidden: u.Hidden, Banned: u.Banned,
			Verified: u.Verified, MustChangePassword: false, CreatedAt: nowTS(),
		})
	}
	rep.wrote("users", len(plan.Users))
	return nil
}

// translateChallenges returns the current value stamped onto each challenge, keyed by id — the number
// every solve of that challenge is worth under the stamp-current-value reconstruction.
func translateChallenges(a *Archive, plan *Plan, rep *Report, opts Options) (map[int64]int32, error) {
	rows, err := decodeTable[ctfdChallenge](a, "challenges")
	if err != nil {
		return nil, err
	}
	dynRows, err := decodeTable[ctfdDynamicChallenge](a, "dynamic_challenge")
	if err != nil {
		return nil, err
	}
	dyn := map[int64]ctfdDynamicChallenge{}
	for _, d := range dynRows {
		dyn[d.ID] = d
	}

	rep.read("challenges", len(rows))
	values := map[int64]int32{}
	for i := range rows {
		c := &rows[i]
		kind := c.Type
		switch kind {
		case "standard", "dynamic":
		default:
			if opts.ForceUnknownChallengeType == "" {
				return nil, fmt.Errorf("challenge %d has unknown type %q: its scoring rule is unknown, "+
					"and a wrong value is a wrong scoreboard", c.ID, c.Type)
			}
			rep.note(SeverityWarning, CodeUnknownChalType, "challenges", 1,
				fmt.Sprintf("challenge %d type %q forced to %q", c.ID, c.Type, opts.ForceUnknownChallengeType))
			kind = opts.ForceUnknownChallengeType
		}

		state := "visible"
		if c.State == "hidden" {
			state = "hidden"
		}

		p := db.ImportChallengesParams{
			ID: c.ID, Name: c.Name, Category: c.Category, Description: c.Description,
			Attribution: c.Attribution, ConnectionInfo: c.ConnectionInfo, Type: kind, State: state,
			MaxAttempts: derefOr(c.MaxAttempts, 0), Logic: "any", Position: 0, NextID: c.NextID,
			Requirements: peelRequirements(c.Requirements, rep), FlagMode: "static",
			FirstBlood: "none", Function: "static", CreatedAt: nowTS(), UpdatedAt: nowTS(),
		}

		if kind == "dynamic" {
			d := dyn[c.ID]
			initial := firstNonNil(d.Initial, c.Initial)
			minimum := firstNonNil(d.Minimum, c.Minimum)
			decay := firstNonNil(d.Decay, c.Decay)
			fn := firstNonNilStr(d.Function, c.Function)
			if initial == nil || minimum == nil || decay == nil {
				return nil, fmt.Errorf("dynamic challenge %d is missing initial/minimum/decay", c.ID)
			}
			if *decay <= 0 || *minimum < 0 || *initial < *minimum {
				return nil, fmt.Errorf("dynamic challenge %d has invalid scoring params "+
					"(initial=%d minimum=%d decay=%d)", c.ID, *initial, *minimum, *decay)
			}
			function := "logarithmic"
			if fn != "" {
				function = fn
			}
			if function != "linear" && function != "logarithmic" {
				return nil, fmt.Errorf("dynamic challenge %d has unknown decay function %q", c.ID, function)
			}
			p.Function, p.Initial, p.Minimum, p.Decay = function, initial, minimum, decay
			p.Value = firstNonNilInt(d.Value, c.Value, initial)
		} else {
			p.Value = derefOr(c.Value, 0)
		}

		values[c.ID] = p.Value
		plan.Challenges = append(plan.Challenges, p)
	}
	rep.wrote("challenges", len(plan.Challenges))
	return values, nil
}

func translateFlags(a *Archive, plan *Plan, rep *Report) error {
	rows, err := decodeTable[ctfdFlag](a, "flags")
	if err != nil {
		return err
	}
	rep.read("flags", len(rows))
	for _, f := range rows {
		if f.Type != "static" && f.Type != "regex" {
			// Dropping a flag makes a challenge unsolvable; refuse the whole import instead.
			return fmt.Errorf("flag %d on challenge %d has unsupported type %q "+
				"(only static and regex are supported)", f.ID, f.ChallengeID, f.Type)
		}
		content := strings.TrimSpace(f.Content)
		caseInsensitive := f.Data == "case_insensitive"
		typ := flags.TypeStatic
		if f.Type == "regex" {
			typ = flags.TypeRegex
		}
		// A regex that only fails at submit time would surface as a 500 on a player's guess.
		if verr := (flags.Flag{Type: typ, Content: content, CaseInsensitive: caseInsensitive}).Validate(); verr != nil {
			return fmt.Errorf("flag %d on challenge %d: %w", f.ID, f.ChallengeID, verr)
		}
		plan.Flags = append(plan.Flags, db.ImportFlagsParams{
			ID: f.ID, ChallengeID: f.ChallengeID, Type: f.Type, Content: content,
			CaseInsensitive: caseInsensitive,
		})
	}
	rep.wrote("flags", len(plan.Flags))
	return nil
}

func translateTags(a *Archive, plan *Plan, rep *Report) error {
	rows, err := decodeTable[ctfdTag](a, "tags")
	if err != nil {
		return err
	}
	rep.read("tags", len(rows))
	// The schema forbids duplicate (challenge_id, value); fold them here with a report line rather
	// than letting the COPY abort on a source that allowed them.
	seen := map[string]bool{}
	for _, t := range rows {
		key := fmt.Sprintf("%d\x00%s", t.ChallengeID, t.Value)
		if seen[key] {
			rep.dropped("tags", 1)
			rep.note(SeverityInfo, CodeDeferredTable, "tags", 1,
				fmt.Sprintf("duplicate tag %q on challenge %d folded", t.Value, t.ChallengeID))
			continue
		}
		seen[key] = true
		plan.Tags = append(plan.Tags, db.ImportTagsParams{ID: t.ID, ChallengeID: t.ChallengeID, Value: t.Value})
	}
	rep.wrote("tags", len(plan.Tags))
	return nil
}

func translateHints(a *Archive, plan *Plan, rep *Report) error {
	rows, err := decodeTable[ctfdHint](a, "hints")
	if err != nil {
		return err
	}
	rep.read("hints", len(rows))
	for _, h := range rows {
		if h.Cost < 0 {
			return fmt.Errorf("hint %d has negative cost %d", h.ID, h.Cost)
		}
		plan.Hints = append(plan.Hints, db.ImportHintsParams{
			ID: h.ID, ChallengeID: h.ChallengeID, Title: h.Title, Content: h.Content,
			Cost: h.Cost, Requirements: peelRequirements(h.Requirements, rep), Position: 0,
		})
	}
	rep.wrote("hints", len(plan.Hints))
	return nil
}

func translateFiles(a *Archive, plan *Plan, rep *Report) error {
	rows, err := decodeTable[ctfdFile](a, "files")
	if err != nil {
		return err
	}
	rep.read("files", len(rows))
	referenced := map[string]bool{}
	for _, f := range rows {
		switch f.Type {
		case "challenge", "standard":
		default:
			// page/solution files belong to deferred subsystems; drop with the owner.
			rep.dropped("files", 1)
			rep.note(SeverityInfo, CodeDeferredTable, "files", 1,
				fmt.Sprintf("file %d of type %q belongs to a deferred subsystem", f.ID, f.Type))
			continue
		}
		blob, ok := a.uploads[f.Location]
		if !ok {
			rep.dropped("files", 1)
			rep.note(SeverityWarning, CodeFileNoBlob, "files", 1,
				fmt.Sprintf("file %d location %q has no upload in the archive", f.ID, f.Location))
			continue
		}
		referenced[f.Location] = true
		sum := sha256.Sum256(blob)
		var challengeID *int64
		if f.Type == "challenge" {
			challengeID = f.ChallengeID
		}
		plan.Files = append(plan.Files, db.ImportFilesParams{
			ID: f.ID, Location: f.Location, Sha256sum: sum[:], SizeBytes: int64(len(blob)),
			ChallengeID: challengeID, CreatedAt: nowTS(),
		})
	}
	rep.wrote("files", len(plan.Files))

	var orphanBlobs int
	for loc := range a.uploads {
		if !referenced[loc] {
			orphanBlobs++
		}
	}
	if orphanBlobs > 0 {
		rep.note(SeverityInfo, CodeBlobNoFile, "files", orphanBlobs, "uploads with no owning file row")
	}
	return nil
}

// translateSubmissions returns the date of every correct submission by id, which the joined-table
// solves rows need (they carry no date of their own).
func translateSubmissions(a *Archive, plan *Plan, rep *Report) (map[int64]pgtype.Timestamptz, error) {
	rows, err := decodeTable[ctfdSubmission](a, "submissions")
	if err != nil {
		return nil, err
	}
	rep.read("submissions", len(rows))
	mode := a.mode(plan.UserMode)
	correctDates := map[int64]pgtype.Timestamptz{}
	for _, s := range rows {
		if s.UserID == nil {
			return nil, fmt.Errorf("submission %d has no user_id, which the schema requires", s.ID)
		}
		switch s.Type {
		case "correct", "incorrect", "partial", "discard", "ratelimited":
		default:
			return nil, fmt.Errorf("submission %d has unknown type %q", s.ID, s.Type)
		}
		ts, terr := parseArchiveTime(s.Date)
		if terr != nil {
			return nil, fmt.Errorf("submission %d date: %w", s.ID, terr)
		}
		var attributed *int64
		if s.Type == "correct" {
			attributed = accountID(mode, *s.UserID, s.TeamID)
		}
		plan.Submissions = append(plan.Submissions, db.ImportSubmissionsParams{
			ID: s.ID, ChallengeID: s.ChallengeID, UserID: *s.UserID, TeamID: s.TeamID,
			Type: s.Type, Provided: s.Provided, Ip: parseIP(s.IP), Date: ts,
			AttributedAccountID: attributed,
		})
		if s.Type == "correct" {
			correctDates[s.ID] = ts
		}
	}
	rep.wrote("submissions", len(plan.Submissions))
	return correctDates, nil
}

func translateSolves(a *Archive, plan *Plan, rep *Report, values map[int64]int32, correctDates map[int64]pgtype.Timestamptz) error {
	rows, err := decodeTable[ctfdSolve](a, "solves")
	if err != nil {
		return err
	}
	rep.read("solves", len(rows))

	solveIDs := map[int64]bool{}
	for _, s := range rows {
		solveIDs[s.ID] = true
	}
	// A correct submission with no solve, or a solve with no correct submission, is source corruption:
	// importing it silently would produce a scoreboard that does not match the source.
	for id := range correctDates {
		if !solveIDs[id] {
			return fmt.Errorf("submission %d is correct but has no matching solve row", id)
		}
	}

	var stamped int
	for _, s := range rows {
		if s.UserID == nil {
			return fmt.Errorf("solve %d has no user_id, which the schema requires", s.ID)
		}
		date, ok := correctDates[s.ID]
		if !ok {
			return fmt.Errorf("solve %d has no matching correct submission", s.ID)
		}
		val := values[s.ChallengeID]
		subID := s.ID
		plan.Solves = append(plan.Solves, db.ImportSolvesParams{
			ID: s.ID, SubmissionID: &subID, ChallengeID: s.ChallengeID, UserID: *s.UserID,
			TeamID: s.TeamID, Value: val, Date: date,
		})
		stamped++
	}
	rep.wrote("solves", len(plan.Solves))
	if stamped > 0 {
		rep.note(SeverityInfo, CodeSolveValueStamp, "solves", stamped,
			"each solve stamped with the challenge's current value; the sum matches the source scoreboard, "+
				"per-solve decay history is not reconstructed")
	}
	return nil
}

func translateAwards(a *Archive, plan *Plan, rep *Report) error {
	rows, err := decodeTable[ctfdAward](a, "awards")
	if err != nil {
		return err
	}
	rep.read("awards", len(rows))
	for _, w := range rows {
		if w.UserID == nil {
			return fmt.Errorf("award %d has no user_id, which the schema requires", w.ID)
		}
		ts, terr := parseArchiveTime(w.Date)
		if terr != nil {
			return fmt.Errorf("award %d date: %w", w.ID, terr)
		}
		plan.Awards = append(plan.Awards, db.ImportAwardsParams{
			ID: w.ID, UserID: *w.UserID, TeamID: w.TeamID, Type: "standard", ChallengeID: nil,
			Name: w.Name, Description: w.Description, Value: w.Value, Category: w.Category,
			Icon: w.Icon, Date: ts,
		})
	}
	rep.wrote("awards", len(plan.Awards))
	return nil
}

// ---- helpers ----

func (a *Archive) mode(userMode string) string {
	if userMode == "teams" {
		return "teams"
	}
	return "users"
}

func accountID(mode string, userID int64, teamID *int64) *int64 {
	if mode == "teams" {
		return teamID
	}
	uid := userID
	return &uid
}

func nowTS() pgtype.Timestamptz { return pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true} }

func parseIP(s *string) *netip.Addr {
	if s == nil || strings.TrimSpace(*s) == "" {
		return nil
	}
	addr, err := netip.ParseAddr(strings.TrimSpace(*s))
	if err != nil {
		return nil
	}
	return &addr
}

func parseArchiveTime(s string) (pgtype.Timestamptz, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return pgtype.Timestamptz{}, fmt.Errorf("empty timestamp")
	}
	layouts := []string{
		time.RFC3339Nano, time.RFC3339,
		"2006-01-02T15:04:05.999999", "2006-01-02T15:04:05",
		"2006-01-02 15:04:05.999999", "2006-01-02 15:04:05",
	}
	for _, l := range layouts {
		if t, err := time.Parse(l, s); err == nil {
			return pgtype.Timestamptz{Time: t.UTC(), Valid: true}, nil
		}
	}
	return pgtype.Timestamptz{}, fmt.Errorf("unrecognised timestamp %q", s)
}

func derefOr[T any](p *T, def T) T {
	if p == nil {
		return def
	}
	return *p
}

func firstNonNil[T any](a, b *T) *T {
	if a != nil {
		return a
	}
	return b
}

func firstNonNilStr(a, b *string) string {
	if a != nil {
		return *a
	}
	if b != nil {
		return *b
	}
	return ""
}

func firstNonNilInt(vals ...*int32) int32 {
	for _, v := range vals {
		if v != nil {
			return *v
		}
	}
	return 0
}

// peelRequirements normalises the requirements column, which the source stored as JSON — but between
// two early versions stored as a JSON-encoded string. The result is always a JSON object.
func peelRequirements(raw json.RawMessage, rep *Report) json.RawMessage {
	empty := json.RawMessage(`{}`)
	v := bytes.TrimSpace(raw)
	if len(v) == 0 || string(v) == "null" || string(v) == `""` {
		return empty
	}
	switch v[0] {
	case '{':
		if json.Valid(v) {
			return append(json.RawMessage(nil), v...)
		}
	case '[':
		// A bare prerequisite list; wrap it in the object shape the schema expects.
		if json.Valid(v) {
			rep.note(SeverityInfo, CodeRequirementsFix, "", 1, "prerequisite list wrapped into {prerequisites:[…]}")
			return json.RawMessage(`{"prerequisites":` + string(v) + `}`)
		}
	case '"':
		var s string
		if json.Unmarshal(v, &s) == nil {
			st := strings.TrimSpace(s)
			rep.note(SeverityInfo, CodeRequirementsFix, "", 1, "string-encoded requirements decoded")
			switch {
			case st == "" || st == "null":
				return empty
			case strings.HasPrefix(st, "{") && json.Valid([]byte(st)):
				return json.RawMessage(st)
			case strings.HasPrefix(st, "[") && json.Valid([]byte(st)):
				return json.RawMessage(`{"prerequisites":` + st + `}`)
			}
		}
	}
	rep.note(SeverityWarning, CodeRequirementsFix, "", 1, "unparseable requirements dropped to {}")
	return empty
}
