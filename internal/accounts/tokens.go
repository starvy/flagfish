package accounts

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/starvy/flagfish/internal/db"
)

// A Token is a minted API token. Plaintext is present exactly once, here, on the way
// back to the caller who asked for it.
type Token struct {
	ID          int64
	Plaintext   string // shown once, at creation, and never again. Only sha256 is stored.
	Description *string
	ExpiresAt   time.Time
}

// TokenInfo is a token as it can be listed: everything except the secret, because the
// secret is not recoverable.
type TokenInfo struct {
	ID          int64
	Description *string
	CreatedAt   time.Time
	ExpiresAt   time.Time
}

// CreateToken mints an API token.
//
// An expiry is always set. "Never expires" is deliberately not expressible: an immortal
// credential is one nobody remembers issuing.
func (s *Service) CreateToken(ctx context.Context, userID int64, description *string, ttl time.Duration) (Token, error) {
	if ttl <= 0 {
		ttl = TokenTTL
	}

	plaintext, hash, err := NewAPIToken()
	if err != nil {
		return Token{}, err
	}
	expires := time.Now().Add(ttl)

	row, err := s.q.CreateAPIToken(ctx, db.CreateAPITokenParams{
		UserID:      userID,
		TokenHash:   hash,
		Description: description,
		ExpiresAt:   pgtype.Timestamptz{Time: expires, Valid: true},
	})
	if err != nil {
		return Token{}, fmt.Errorf("accounts: create token: %w", err)
	}

	return Token{
		ID:          row.ID,
		Plaintext:   plaintext,
		Description: row.Description,
		ExpiresAt:   expires,
	}, nil
}

// ListTokens returns the caller's tokens, without secrets.
func (s *Service) ListTokens(ctx context.Context, userID int64) ([]TokenInfo, error) {
	rows, err := s.q.ListAPITokens(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("accounts: list tokens: %w", err)
	}
	out := make([]TokenInfo, 0, len(rows))
	for _, r := range rows {
		out = append(out, TokenInfo{
			ID:          r.ID,
			Description: r.Description,
			CreatedAt:   r.CreatedAt.Time,
			ExpiresAt:   r.ExpiresAt.Time,
		})
	}
	return out, nil
}

// DeleteToken revokes a token.
//
// Ownership is enforced in the WHERE clause, not by a SELECT-then-check, so there is no
// window between the check and the delete and no guard for a future edit to forget. Zero
// rows affected means "not yours, or not there" — and the caller cannot tell which,
// which is the correct amount of information to give them.
func (s *Service) DeleteToken(ctx context.Context, userID, tokenID int64) error {
	n, err := s.q.DeleteAPIToken(ctx, db.DeleteAPITokenParams{ID: tokenID, UserID: userID})
	if err != nil {
		return fmt.Errorf("accounts: delete token: %w", err)
	}
	if n == 0 {
		return ErrTokenNotFound
	}
	return nil
}

// ErrTokenNotFound is returned when the token does not exist OR belongs to someone else.
// One error for both, on purpose: distinguishing them would confirm the existence of
// another user's token id.
var ErrTokenNotFound = fmt.Errorf("accounts: token not found")
