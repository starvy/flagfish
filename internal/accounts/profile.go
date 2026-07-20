package accounts

import (
	"context"
	"errors"
	"fmt"

	"github.com/starvy/flagfish/internal/db"
	"github.com/starvy/flagfish/internal/domain/account"
)

// ErrInvalidLanguage is returned when a language preference is not a well-formed BCP 47 tag.
var ErrInvalidLanguage = errors.New("accounts: language is not a well-formed BCP 47 tag")

// Profile is the caller's own account, for the "who am I" endpoint.
type Profile struct {
	ID          int64
	Name        string
	Email       string
	Role        string
	Verified    bool
	Banned      bool
	TeamID      *int64
	Website     *string
	Affiliation *string
	Country     *string
	Language    *string
}

func (s *Service) Profile(ctx context.Context, userID int64) (Profile, error) {
	u, err := s.q.GetUserByID(ctx, userID)
	if err != nil {
		return Profile{}, fmt.Errorf("accounts: profile: %w", err)
	}
	return Profile{
		ID:          u.ID,
		Name:        u.Name,
		Email:       u.Email,
		Role:        u.Role,
		Verified:    u.Verified,
		Banned:      u.Banned,
		TeamID:      u.TeamID,
		Website:     u.Website,
		Affiliation: u.Affiliation,
		Country:     u.Country,
		Language:    u.Language,
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
		ID:          u.ID,
		Name:        u.Name,
		Email:       u.Email,
		Role:        u.Role,
		Verified:    u.Verified,
		Banned:      u.Banned,
		TeamID:      u.TeamID,
		Website:     u.Website,
		Affiliation: u.Affiliation,
		Country:     u.Country,
		Language:    u.Language,
	}, nil
}
