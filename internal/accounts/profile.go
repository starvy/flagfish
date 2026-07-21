package accounts

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/starvy/flagfish/internal/db"
	"github.com/starvy/flagfish/internal/domain/account"
)

// ErrInvalidLanguage is returned when a language preference is not a well-formed BCP 47 tag.
var ErrInvalidLanguage = errors.New("accounts: language is not a well-formed BCP 47 tag")

// ErrUserNotFound is returned when a public profile is requested for an account that does not exist
// or is hidden/banned from the caller — the two are indistinguishable to a non-admin, by design.
var ErrUserNotFound = errors.New("accounts: user not found")

// Profile is the caller's own account, for the "who am I" endpoint.
type Profile struct {
	ID           int64
	Name         string
	Email        string
	PendingEmail *string
	Role         string
	Verified     bool
	Banned       bool
	TeamID       *int64
	Website      *string
	Affiliation  *string
	Country      *string
	Language     *string
}

func (s *Service) Profile(ctx context.Context, userID int64) (Profile, error) {
	u, err := s.q.GetUserByID(ctx, userID)
	if err != nil {
		return Profile{}, fmt.Errorf("accounts: profile: %w", err)
	}
	return Profile{
		ID:           u.ID,
		Name:         u.Name,
		Email:        u.Email,
		PendingEmail: u.PendingEmail,
		Role:         u.Role,
		Verified:     u.Verified,
		Banned:       u.Banned,
		TeamID:       u.TeamID,
		Website:      u.Website,
		Affiliation:  u.Affiliation,
		Country:      u.Country,
		Language:     u.Language,
	}, nil
}

// PublicProfile is a user's public page: the visibility-gated identity fields, the score summed from
// the stamped ledger, and the solved-challenge history. Contact data (email) is never here.
type PublicProfile struct {
	ID          int64
	Name        string
	Website     *string
	Affiliation *string
	Country     *string
	BracketID   *int64
	BracketName *string
	Score       int64
	CreatedAt   time.Time
	Solves      []ProfileSolve
	History     []ProfileHistoryPoint
	// Fields carries only the public, answered custom fields — a private answer never reaches here.
	// It rides the account-visibility gate (a hidden account 404s), not score_visibility: a public
	// answer is public regardless of whether scores are shown.
	Fields []PublicFieldAnswer
}

// ProfileSolve is one solved challenge on a public profile, newest first.
type ProfileSolve struct {
	ChallengeID   int64
	ChallengeName string
	Category      string
	Value         int32
	Date          time.Time
}

// ProfileHistoryPoint is one instant on a user's own cumulative score curve: Delta is the ledger
// event's value, Score the running total up to it. Keyed on the user's stamped ledger, so in teams
// mode it is the player's personal contribution — not their team's curve.
type ProfileHistoryPoint struct {
	Date  time.Time
	Delta int64
	Score int64
}

// UserProfile is the public user page, gated exactly like the scoreboard and the team page: a hidden
// or banned account 404s to the public and is visible to an admin. cutoff is the caller's freeze
// horizon (nil = live), clamping both the score and the solve history.
func (s *Service) UserProfile(ctx context.Context, userID int64, admin bool, cutoff *time.Time) (PublicProfile, error) {
	row, err := s.q.GetUserPublicProfile(ctx, db.GetUserPublicProfileParams{
		UserID: userID, Admin: admin, Cutoff: cutoffArg(cutoff),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return PublicProfile{}, fmt.Errorf("%w: id=%d", ErrUserNotFound, userID)
	} else if err != nil {
		return PublicProfile{}, fmt.Errorf("accounts: user profile: %w", err)
	}

	solveRows, err := s.q.ListUserSolves(ctx, db.ListUserSolvesParams{
		UserID: userID, Cutoff: cutoffArg(cutoff),
	})
	if err != nil {
		return PublicProfile{}, fmt.Errorf("accounts: user profile solves: %w", err)
	}
	solves := make([]ProfileSolve, 0, len(solveRows))
	for _, r := range solveRows {
		solves = append(solves, ProfileSolve{
			ChallengeID: r.ChallengeID, ChallengeName: r.ChallengeName,
			Category: r.Category, Value: r.Value, Date: r.Date.Time,
		})
	}

	// The curve under the profile is this user's own ledger, keyed on user_id — not the scoreboard
	// account. In teams mode a user id would otherwise resolve to a stranger's team, so the history
	// is read here through a user-keyed query rather than the scoreboard-detail one.
	histRows, err := s.q.GetUserScoreHistory(ctx, db.GetUserScoreHistoryParams{
		UserID: userID, Cutoff: cutoffArg(cutoff),
	})
	if err != nil {
		return PublicProfile{}, fmt.Errorf("accounts: user profile history: %w", err)
	}
	history := make([]ProfileHistoryPoint, len(histRows))
	for i, r := range histRows {
		history[i] = ProfileHistoryPoint{Date: r.Date.Time, Delta: r.Delta, Score: r.Cumulative}
	}

	// Only public, answered fields — the query itself refuses to project a private answer.
	fields, err := s.PublicFields(ctx, userID)
	if err != nil {
		return PublicProfile{}, fmt.Errorf("accounts: user profile fields: %w", err)
	}

	return PublicProfile{
		ID: row.ID, Name: row.Name,
		Website: row.Website, Affiliation: row.Affiliation, Country: row.Country,
		BracketID: row.BracketID, BracketName: row.BracketName,
		Score: row.Score, CreatedAt: row.CreatedAt.Time, Solves: solves, History: history,
		Fields: fields,
	}, nil
}

// ChangeName sets the caller's display name. A display name is not an identity, so it is deliberately
// not unique and this is a plain write with no collision to report.
func (s *Service) ChangeName(ctx context.Context, userID int64, name string) (Profile, error) {
	u, err := s.q.UpdateUserName(ctx, db.UpdateUserNameParams{UserID: userID, Name: name})
	if err != nil {
		return Profile{}, fmt.Errorf("accounts: change name: %w", err)
	}
	return Profile{
		ID:           u.ID,
		Name:         u.Name,
		Email:        u.Email,
		PendingEmail: u.PendingEmail,
		Role:         u.Role,
		Verified:     u.Verified,
		Banned:       u.Banned,
		TeamID:       u.TeamID,
		Website:      u.Website,
		Affiliation:  u.Affiliation,
		Country:      u.Country,
		Language:     u.Language,
	}, nil
}

// ProfilePatch is the player-owned slice of the account: nil keeps, the Clear flags null.
// Name and email are identity, not profile — they are not editable here.
type ProfilePatch struct {
	Website     *string
	Affiliation *string
	Country     *string
	Language    *string

	ClearWebsite     bool
	ClearAffiliation bool
	ClearCountry     bool
	ClearLanguage    bool
}

func (s *Service) UpdateProfile(ctx context.Context, userID int64, patch ProfilePatch) (Profile, error) {
	// A malformed tag is refused before it reaches the column: the preference feeds locale
	// formatting, and a value that is not a real BCP 47 tag can only mis-format silently.
	if patch.Language != nil && !account.WellFormedLanguageTag(*patch.Language) {
		return Profile{}, ErrInvalidLanguage
	}
	u, err := s.q.UpdateOwnProfile(ctx, db.UpdateOwnProfileParams{
		UserID:  userID,
		Website: patch.Website, Affiliation: patch.Affiliation, Country: patch.Country,
		Language:         patch.Language,
		ClearWebsite:     patch.ClearWebsite,
		ClearAffiliation: patch.ClearAffiliation,
		ClearCountry:     patch.ClearCountry,
		ClearLanguage:    patch.ClearLanguage,
	})
	if err != nil {
		return Profile{}, fmt.Errorf("accounts: update profile: %w", err)
	}
	return Profile{
		ID:           u.ID,
		Name:         u.Name,
		Email:        u.Email,
		PendingEmail: u.PendingEmail,
		Role:         u.Role,
		Verified:     u.Verified,
		Banned:       u.Banned,
		TeamID:       u.TeamID,
		Website:      u.Website,
		Affiliation:  u.Affiliation,
		Country:      u.Country,
		Language:     u.Language,
	}, nil
}
