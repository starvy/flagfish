package adminops

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/starvy/flagfish/internal/audit"
	"github.com/starvy/flagfish/internal/db"
	"github.com/starvy/flagfish/internal/domain/flags"
	"github.com/starvy/flagfish/internal/domain/prereq"
)

// NewChallenge is the create input. Type is not in it: it is derived from the scoring function,
// so the two columns cannot disagree.
type NewChallenge struct {
	Name           string
	Category       string
	Description    string
	Attribution    *string
	ConnectionInfo *string
	State          string
	Value          int32
	Function       string
	Initial        *int32
	Minimum        *int32
	Decay          *int32
	MaxAttempts    int32
	Logic          string
	Position       int32
	FirstBlood     string
	// FirstBloodBonus must be present exactly when FirstBlood is "bonus"; the pairing and
	// positivity CHECKs arbitrate, not this struct.
	FirstBloodBonus *int32
}

// ChallengePatch is a partial update. For every field nil means "keep". The nullable columns carry
// an extra Clear flag: setting it nulls the column, which is distinct from leaving the value nil.
// A caller must not set both a value and its Clear on the same field.
type ChallengePatch struct {
	Name            *string
	Category        *string
	Description     *string
	Attribution     *string
	ConnectionInfo  *string
	Value           *int32
	Function        *string
	Initial         *int32
	Minimum         *int32
	Decay           *int32
	MaxAttempts     *int32
	Logic           *string
	Position        *int32
	FirstBlood      *string
	FirstBloodBonus *int32
	// NextID is the suggested-next challenge shown after a solve. Existence is the FK's job and
	// self-reference is a CHECK — both surface as a 422 here, never a pre-read.
	NextID *int64

	ClearAttribution     bool
	ClearConnectionInfo  bool
	ClearInitial         bool
	ClearMinimum         bool
	ClearDecay           bool
	ClearFirstBloodBonus bool
	ClearNextID          bool
}

func challengeType(function string) string {
	if function == "static" {
		return "standard"
	}
	return "dynamic"
}

//nolint:gocritic // hugeParam: the create input is a value; a pointer here would invite a caller to mutate it mid-call.
func (s *Service) CreateChallenge(ctx context.Context, actor audit.Actor, in NewChallenge) (db.Challenge, error) {
	var out db.Challenge
	err := s.tx(ctx, actor, func(_ pgx.Tx, q *db.Queries) error {
		var err error
		out, err = q.AdminCreateChallenge(ctx, db.AdminCreateChallengeParams{
			Name: in.Name, Category: in.Category, Description: in.Description,
			Attribution: in.Attribution, ConnectionInfo: in.ConnectionInfo,
			Type: challengeType(in.Function), State: in.State, Value: in.Value,
			Function: in.Function, Initial: in.Initial, Minimum: in.Minimum, Decay: in.Decay,
			MaxAttempts: in.MaxAttempts, Logic: in.Logic, Position: in.Position,
			FirstBlood: in.FirstBlood, FirstBloodBonus: in.FirstBloodBonus,
		})
		if err != nil {
			return fmt.Errorf("adminops: create challenge: %w", checkViolation(err))
		}
		return nil
	})
	return out, err
}

func (s *Service) UpdateChallenge(ctx context.Context, actor audit.Actor, challengeID int64, patch ChallengePatch) (db.Challenge, error) {
	var out db.Challenge
	err := s.tx(ctx, actor, func(_ pgx.Tx, q *db.Queries) error {
		var typ *string
		if patch.Function != nil {
			t := challengeType(*patch.Function)
			typ = &t
		}
		// logic='all' is meaningless on a unique-flag challenge: per-account flags are one flag, so
		// there is nothing to combine. Refuse it loudly rather than store a value the submit path
		// never consults.
		if patch.Logic != nil && *patch.Logic == "all" {
			ch, getErr := q.AdminGetChallenge(ctx, challengeID)
			if errors.Is(getErr, pgx.ErrNoRows) {
				return fmt.Errorf("%w: id=%d", ErrChallengeNotFound, challengeID)
			} else if getErr != nil {
				return fmt.Errorf("adminops: update challenge %d: read for logic guard: %w", challengeID, getErr)
			}
			if ch.FlagMode == "unique" {
				return invalidf("logic 'all' is meaningless for a unique-flag challenge; keep logic 'any'")
			}
		}
		var err error
		out, err = q.AdminUpdateChallenge(ctx, db.AdminUpdateChallengeParams{
			ChallengeID: challengeID,
			Name:        patch.Name, Category: patch.Category, Description: patch.Description,
			Attribution: patch.Attribution, ConnectionInfo: patch.ConnectionInfo,
			Type: typ, Value: patch.Value, Function: patch.Function,
			Initial: patch.Initial, Minimum: patch.Minimum, Decay: patch.Decay,
			MaxAttempts: patch.MaxAttempts, Logic: patch.Logic, Position: patch.Position,
			FirstBlood: patch.FirstBlood, FirstBloodBonus: patch.FirstBloodBonus,
			NextID:               patch.NextID,
			ClearAttribution:     patch.ClearAttribution,
			ClearConnectionInfo:  patch.ClearConnectionInfo,
			ClearInitial:         patch.ClearInitial,
			ClearMinimum:         patch.ClearMinimum,
			ClearDecay:           patch.ClearDecay,
			ClearFirstBloodBonus: patch.ClearFirstBloodBonus,
			ClearNextID:          patch.ClearNextID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: id=%d", ErrChallengeNotFound, challengeID)
		} else if err != nil {
			return fmt.Errorf("adminops: update challenge %d: %w", challengeID, challengeWriteViolation(err, patch.NextID))
		}
		return nil
	})
	return out, err
}

// SetChallengeFlagMode switches how a challenge issues flags (static ↔ unique), in one transaction,
// behind three guards that a mid-event switch would otherwise walk straight through:
//
//   - either direction is refused while the challenge has any solve. The unissued-solve detector is
//     a date-blind anti-join, so flipping static→unique mid-event would report every legitimate
//     prior solver as an unissued solve — the docs sell that detector as "not a heuristic", so it
//     must not be handed a false positive by construction. Deleting and recreating the challenge is
//     the deliberate escape hatch.
//   - → unique is refused while a regex flag exists: a pattern cannot be baked into a pool entry, so
//     it could never be issued. This is the other half of the one-directional guard on flag create.
//   - → static is refused when the challenge has no flags at all: checkStatic hard-errors on an
//     empty flag set, so the switch would turn every submission into a 500.
//
// Switching to unique with an empty pool is allowed on purpose: the failure is loud by design (a 503
// on the first view), and refusing it here would just move the authoring order around. logic='all'
// is meaningless for a unique challenge, so a switch to unique is refused while logic is 'all'.
func (s *Service) SetChallengeFlagMode(ctx context.Context, actor audit.Actor, challengeID int64, mode string) (db.Challenge, error) {
	if _, err := flags.ParseMode(mode); err != nil {
		return db.Challenge{}, invalidf("unknown flag_mode %q", mode)
	}
	var out db.Challenge
	err := s.tx(ctx, actor, func(_ pgx.Tx, q *db.Queries) error {
		ch, err := q.AdminGetChallenge(ctx, challengeID)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: id=%d", ErrChallengeNotFound, challengeID)
		} else if err != nil {
			return fmt.Errorf("adminops: set flag_mode: read challenge %d: %w", challengeID, err)
		}
		if ch.FlagMode == mode {
			out = ch
			return nil
		}

		solves, err := q.AdminCountChallengeSolves(ctx, challengeID)
		if err != nil {
			return fmt.Errorf("adminops: set flag_mode: count solves: %w", err)
		}
		if solves > 0 {
			return invalidf("cannot change flag_mode while the challenge has solves: the unissued-solve " +
				"detector would report every prior solver as sharing. Delete and recreate the challenge instead")
		}

		stats, err := q.AdminChallengeFlagStats(ctx, challengeID)
		if err != nil {
			return fmt.Errorf("adminops: set flag_mode: flag stats: %w", err)
		}
		switch mode {
		case "unique":
			if stats.Regex > 0 {
				return invalidf("cannot switch to unique flags while a regex flag exists: a pattern cannot be pool-issued — remove it first")
			}
			if ch.Logic == "all" {
				return invalidf("cannot switch to unique flags while logic is 'all': per-account flags are one flag, so 'all' is meaningless — set logic to 'any' first")
			}
		case "static":
			if stats.Total == 0 {
				return invalidf("cannot switch to static flags: the challenge has no flags, so every submission would error — add a flag first")
			}
		}

		out, err = q.AdminSetChallengeFlagMode(ctx, db.AdminSetChallengeFlagModeParams{
			ChallengeID: challengeID, FlagMode: mode,
		})
		if err != nil {
			return fmt.Errorf("adminops: set flag_mode on challenge %d: %w", challengeID, err)
		}
		return nil
	})
	return out, err
}

func (s *Service) SetChallengeState(ctx context.Context, actor audit.Actor, challengeID int64, state string) (db.Challenge, error) {
	var out db.Challenge
	err := s.tx(ctx, actor, func(_ pgx.Tx, q *db.Queries) error {
		var err error
		out, err = q.AdminSetChallengeState(ctx, db.AdminSetChallengeStateParams{
			ChallengeID: challengeID, State: state,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: id=%d", ErrChallengeNotFound, challengeID)
		} else if err != nil {
			return fmt.Errorf("adminops: set challenge %d state: %w", challengeID, err)
		}
		return nil
	})
	return out, err
}

// DeleteChallenge removes a challenge and its cascading content (flags, hints, tags, file links).
// Ledger rows never cascade: solves are scoreboard facts, and submissions, awards and hint unlocks
// are gameplay history and anticheat evidence, so the database refuses the delete while any exist.
// There is no pre-check: the constraint IS the check, and a row landing mid-delete fails the same
// way a pre-existing one does.
func (s *Service) DeleteChallenge(ctx context.Context, actor audit.Actor, challengeID int64) error {
	return s.tx(ctx, actor, func(_ pgx.Tx, q *db.Queries) error {
		n, err := q.AdminDeleteChallenge(ctx, challengeID)
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23503" {
				switch pgErr.ConstraintName {
				case "solves_challenge_id_fkey":
					return fmt.Errorf("%w: id=%d", ErrChallengeHasSolves, challengeID)
				// hint_unlocks_hint_id_fkey fires through the challenges→hints cascade.
				case "submissions_challenge_id_fkey", "awards_challenge_id_fkey", "hint_unlocks_hint_id_fkey":
					return fmt.Errorf("%w: id=%d (%s)", ErrChallengeHasHistory, challengeID, pgErr.ConstraintName)
				default:
					return fmt.Errorf("%w: id=%d (%s)", ErrChallengeInUse, challengeID, pgErr.ConstraintName)
				}
			}
			return fmt.Errorf("adminops: delete challenge %d: %w", challengeID, err)
		}
		if n == 0 {
			return fmt.Errorf("%w: id=%d", ErrChallengeNotFound, challengeID)
		}
		return nil
	})
}

// ChallengeOrder is one challenge's new board position.
type ChallengeOrder struct {
	ID       int64
	Position int32
}

// ReorderChallenges sets the board position of many challenges at once, in one transaction. Every
// id must exist: a partial match means the caller sent a stale board, and applying half of it would
// leave an ordering nobody asked for, so the whole batch is refused.
func (s *Service) ReorderChallenges(ctx context.Context, actor audit.Actor, orders []ChallengeOrder) error {
	if len(orders) == 0 {
		return invalidf("no challenges to reorder")
	}
	ids := make([]int64, len(orders))
	positions := make([]int32, len(orders))
	seen := make(map[int64]struct{}, len(orders))
	for i, o := range orders {
		if _, dup := seen[o.ID]; dup {
			return invalidf("challenge %d appears twice in the reorder", o.ID)
		}
		seen[o.ID] = struct{}{}
		ids[i], positions[i] = o.ID, o.Position
	}

	return s.tx(ctx, actor, func(_ pgx.Tx, q *db.Queries) error {
		n, err := q.AdminReorderChallenges(ctx, db.AdminReorderChallengesParams{Ids: ids, Positions: positions})
		if err != nil {
			return fmt.Errorf("adminops: reorder challenges: %w", err)
		}
		if int(n) != len(orders) {
			return fmt.Errorf("%w: %d of %d ids matched", ErrChallengeNotFound, n, len(orders))
		}
		return nil
	})
}

// ── flags ───────────────────────────────────────────────────────────────────────

type NewFlag struct {
	Type            string
	Content         string
	CaseInsensitive bool
}

type FlagPatch struct {
	Type            *string
	Content         *string
	CaseInsensitive *bool
}

// validateFlag rejects what the schema cannot: a regex that will not compile (a parse error now
// beats a submit that can never match), and a regex flag on a unique-flag challenge (pool entries
// are concrete strings; a pattern cannot be issued).
func validateFlag(flagMode, flagType, content string) error {
	if flagType != "regex" {
		return nil
	}
	if flagMode == "unique" {
		return invalidf("a challenge with unique flags cannot have a regex flag")
	}
	if _, err := regexp.Compile(content); err != nil {
		return invalidf("regex flag does not compile: %v", err)
	}
	return nil
}

func (s *Service) AddFlag(ctx context.Context, actor audit.Actor, challengeID int64, in NewFlag) (db.Flag, error) {
	var out db.Flag
	err := s.tx(ctx, actor, func(_ pgx.Tx, q *db.Queries) error {
		ch, err := q.AdminGetChallenge(ctx, challengeID)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: id=%d", ErrChallengeNotFound, challengeID)
		} else if err != nil {
			return fmt.Errorf("adminops: add flag: read challenge %d: %w", challengeID, err)
		}
		if verr := validateFlag(ch.FlagMode, in.Type, in.Content); verr != nil {
			return verr
		}
		out, err = q.AdminInsertFlag(ctx, db.AdminInsertFlagParams{
			ChallengeID: challengeID, Type: in.Type, Content: in.Content,
			CaseInsensitive: in.CaseInsensitive,
		})
		if err != nil {
			return fmt.Errorf("adminops: add flag to challenge %d: %w", challengeID, err)
		}
		return nil
	})
	return out, err
}

func (s *Service) UpdateFlag(ctx context.Context, actor audit.Actor, challengeID, flagID int64, patch FlagPatch) (db.Flag, error) {
	var out db.Flag
	err := s.tx(ctx, actor, func(_ pgx.Tx, q *db.Queries) error {
		ch, err := q.AdminGetChallenge(ctx, challengeID)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: id=%d", ErrChallengeNotFound, challengeID)
		} else if err != nil {
			return fmt.Errorf("adminops: update flag: read challenge %d: %w", challengeID, err)
		}
		cur, err := q.AdminGetFlag(ctx, db.AdminGetFlagParams{FlagID: flagID, ChallengeID: challengeID})
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: id=%d", ErrFlagNotFound, flagID)
		} else if err != nil {
			return fmt.Errorf("adminops: update flag: read flag %d: %w", flagID, err)
		}

		// Validate the row as it will be after the patch, not the patch alone: changing only the
		// type to regex must still compile the content that stays.
		typ, content := cur.Type, cur.Content
		if patch.Type != nil {
			typ = *patch.Type
		}
		if patch.Content != nil {
			content = *patch.Content
		}
		if verr := validateFlag(ch.FlagMode, typ, content); verr != nil {
			return verr
		}

		out, err = q.AdminUpdateFlag(ctx, db.AdminUpdateFlagParams{
			FlagID: flagID, ChallengeID: challengeID,
			Type: patch.Type, Content: patch.Content, CaseInsensitive: patch.CaseInsensitive,
		})
		if err != nil {
			return fmt.Errorf("adminops: update flag %d: %w", flagID, err)
		}
		return nil
	})
	return out, err
}

func (s *Service) DeleteFlag(ctx context.Context, actor audit.Actor, challengeID, flagID int64) error {
	return s.tx(ctx, actor, func(_ pgx.Tx, q *db.Queries) error {
		n, err := q.AdminDeleteFlag(ctx, db.AdminDeleteFlagParams{FlagID: flagID, ChallengeID: challengeID})
		if err != nil {
			return fmt.Errorf("adminops: delete flag %d: %w", flagID, err)
		}
		if n == 0 {
			return fmt.Errorf("%w: id=%d", ErrFlagNotFound, flagID)
		}
		return nil
	})
}

// ── hints ───────────────────────────────────────────────────────────────────────

type NewHint struct {
	Title    *string
	Content  string
	Cost     int32
	Position int32
	// Prerequisites are hint ids on the same challenge that must be unlocked before this one.
	Prerequisites []int64
}

type HintPatch struct {
	Title    *string
	Content  *string
	Cost     *int32
	Position *int32
	// Prerequisites replaces the whole set: nil keeps, empty clears.
	Prerequisites *[]int64
}

func (s *Service) AddHint(ctx context.Context, actor audit.Actor, challengeID int64, in NewHint) (db.Hint, error) {
	ids := dedupeIDs(in.Prerequisites)
	raw, err := prereq.Encode(prereq.Requirements{Prerequisites: ids})
	if err != nil {
		return db.Hint{}, fmt.Errorf("adminops: add hint: %w", err)
	}
	var out db.Hint
	err = s.tx(ctx, actor, func(_ pgx.Tx, q *db.Queries) error {
		var err error
		out, err = q.AdminInsertHint(ctx, db.AdminInsertHintParams{
			ChallengeID: challengeID, Title: in.Title, Content: in.Content,
			Cost: in.Cost, Position: in.Position, Requirements: raw,
		})
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23503" {
				return fmt.Errorf("%w: id=%d", ErrChallengeNotFound, challengeID)
			}
			return fmt.Errorf("adminops: add hint to challenge %d: %w", challengeID, err)
		}
		return validateHintPrereqs(ctx, q, challengeID, out.ID, ids)
	})
	return out, err
}

func (s *Service) UpdateHint(ctx context.Context, actor audit.Actor, challengeID, hintID int64, patch HintPatch) (db.Hint, error) {
	var ids []int64
	var raw []byte
	if patch.Prerequisites != nil {
		ids = dedupeIDs(*patch.Prerequisites)
		var err error
		raw, err = prereq.Encode(prereq.Requirements{Prerequisites: ids})
		if err != nil {
			return db.Hint{}, fmt.Errorf("adminops: update hint: %w", err)
		}
	}
	var out db.Hint
	err := s.tx(ctx, actor, func(_ pgx.Tx, q *db.Queries) error {
		var err error
		out, err = q.AdminUpdateHint(ctx, db.AdminUpdateHintParams{
			HintID: hintID, ChallengeID: challengeID,
			Title: patch.Title, Content: patch.Content, Cost: patch.Cost, Position: patch.Position,
			Requirements: raw,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: id=%d", ErrHintNotFound, hintID)
		} else if err != nil {
			return fmt.Errorf("adminops: update hint %d: %w", hintID, err)
		}
		if patch.Prerequisites == nil {
			return nil
		}
		return validateHintPrereqs(ctx, q, challengeID, hintID, ids)
	})
	return out, err
}

func (s *Service) DeleteHint(ctx context.Context, actor audit.Actor, challengeID, hintID int64) error {
	return s.tx(ctx, actor, func(_ pgx.Tx, q *db.Queries) error {
		n, err := q.AdminDeleteHint(ctx, db.AdminDeleteHintParams{HintID: hintID, ChallengeID: challengeID})
		if err != nil {
			// An unlocked hint stays: the unlock and its charge are ledger rows.
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23503" && pgErr.ConstraintName == "hint_unlocks_hint_id_fkey" {
				return fmt.Errorf("%w: id=%d", ErrHintUnlocked, hintID)
			}
			return fmt.Errorf("adminops: delete hint %d: %w", hintID, err)
		}
		if n == 0 {
			return fmt.Errorf("%w: id=%d", ErrHintNotFound, hintID)
		}
		return nil
	})
}

// checkViolation translates the challenge CHECK constraints into operator language. Anything else
// passes through untouched for the boundary to wrap.
func checkViolation(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
		return err
	}
	switch pgErr.ConstraintName {
	case "challenges_dynamic_params":
		return invalidf("a decayed challenge needs initial, minimum and decay, with decay > 0 and initial >= minimum >= 0")
	case "challenges_fb_bonus":
		return invalidf("first_blood 'bonus' needs a first_blood_bonus, and any other mode must not carry one")
	case "challenges_fb_bonus_positive":
		return invalidf("first_blood_bonus must be positive: zero scores nothing and a negative bonus would penalize the first solver")
	case "challenges_next_not_self":
		return invalidf("a challenge cannot suggest itself as the next one")
	}
	return err
}

// challengeWriteViolation adds the next_id foreign key to checkViolation's translations: a supplied
// target that does not exist trips the FK, and the caller wants the id it sent named rather than a
// bare database error. nextID is the value the patch set, for the message; a clear or an omitted
// next_id never trips this.
func challengeWriteViolation(err error, nextID *int64) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" && pgErr.ConstraintName == "challenges_next_id_fkey" {
		if nextID != nil {
			return invalidf("next_id references challenge %d, which does not exist", *nextID)
		}
		return invalidf("next_id references a challenge that does not exist")
	}
	return checkViolation(err)
}
