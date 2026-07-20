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

	ClearAttribution     bool
	ClearConnectionInfo  bool
	ClearInitial         bool
	ClearMinimum         bool
	ClearDecay           bool
	ClearFirstBloodBonus bool
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
		var err error
		out, err = q.AdminUpdateChallenge(ctx, db.AdminUpdateChallengeParams{
			ChallengeID: challengeID,
			Name:        patch.Name, Category: patch.Category, Description: patch.Description,
			Attribution: patch.Attribution, ConnectionInfo: patch.ConnectionInfo,
			Type: typ, Value: patch.Value, Function: patch.Function,
			Initial: patch.Initial, Minimum: patch.Minimum, Decay: patch.Decay,
			MaxAttempts: patch.MaxAttempts, Logic: patch.Logic, Position: patch.Position,
			FirstBlood: patch.FirstBlood, FirstBloodBonus: patch.FirstBloodBonus,
			ClearAttribution:     patch.ClearAttribution,
			ClearConnectionInfo:  patch.ClearConnectionInfo,
			ClearInitial:         patch.ClearInitial,
			ClearMinimum:         patch.ClearMinimum,
			ClearDecay:           patch.ClearDecay,
			ClearFirstBloodBonus: patch.ClearFirstBloodBonus,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: id=%d", ErrChallengeNotFound, challengeID)
		} else if err != nil {
			return fmt.Errorf("adminops: update challenge %d: %w", challengeID, checkViolation(err))
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
}

type HintPatch struct {
	Title    *string
	Content  *string
	Cost     *int32
	Position *int32
}

func (s *Service) AddHint(ctx context.Context, actor audit.Actor, challengeID int64, in NewHint) (db.Hint, error) {
	var out db.Hint
	err := s.tx(ctx, actor, func(_ pgx.Tx, q *db.Queries) error {
		var err error
		out, err = q.AdminInsertHint(ctx, db.AdminInsertHintParams{
			ChallengeID: challengeID, Title: in.Title, Content: in.Content,
			Cost: in.Cost, Position: in.Position,
		})
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23503" {
				return fmt.Errorf("%w: id=%d", ErrChallengeNotFound, challengeID)
			}
			return fmt.Errorf("adminops: add hint to challenge %d: %w", challengeID, err)
		}
		return nil
	})
	return out, err
}

func (s *Service) UpdateHint(ctx context.Context, actor audit.Actor, challengeID, hintID int64, patch HintPatch) (db.Hint, error) {
	var out db.Hint
	err := s.tx(ctx, actor, func(_ pgx.Tx, q *db.Queries) error {
		var err error
		out, err = q.AdminUpdateHint(ctx, db.AdminUpdateHintParams{
			HintID: hintID, ChallengeID: challengeID,
			Title: patch.Title, Content: patch.Content, Cost: patch.Cost, Position: patch.Position,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: id=%d", ErrHintNotFound, hintID)
		} else if err != nil {
			return fmt.Errorf("adminops: update hint %d: %w", hintID, err)
		}
		return nil
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
	}
	return err
}
