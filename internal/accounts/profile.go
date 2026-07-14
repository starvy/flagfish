package accounts

import (
	"context"
	"fmt"
)

// Profile is the caller's own account, for the "who am I" endpoint.
type Profile struct {
	ID       int64
	Name     string
	Email    string
	Role     string
	Verified bool
	Banned   bool
	TeamID   *int64
}

func (s *Service) Profile(ctx context.Context, userID int64) (Profile, error) {
	u, err := s.q.GetUserByID(ctx, userID)
	if err != nil {
		return Profile{}, fmt.Errorf("accounts: profile: %w", err)
	}
	return Profile{
		ID:       u.ID,
		Name:     u.Name,
		Email:    u.Email,
		Role:     u.Role,
		Verified: u.Verified,
		Banned:   u.Banned,
		TeamID:   u.TeamID,
	}, nil
}
