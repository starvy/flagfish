package adminops

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/starvy/flagfish/internal/audit"
	"github.com/starvy/flagfish/internal/db"
	"github.com/starvy/flagfish/internal/domain/account"
)

// NewBracket is the create input. AppliesTo fixes the account kind the bracket can hold; it is not
// patchable, because changing it would strand every member whose kind no longer matches.
type NewBracket struct {
	Name        string
	Description *string
	AppliesTo   string
}

// BracketPatch is a partial update: nil means "keep". Description cannot be cleared through it, and
// AppliesTo is absent on purpose.
type BracketPatch struct {
	Name        *string
	Description *string
}

// BracketAssignment is the narrow result of an assign: the account touched and its bracket after.
type BracketAssignment struct {
	AccountID int64
	Name      string
	BracketID *int64
}

// ListBrackets returns every bracket, admin view (both account kinds). It is a plain read, so it
// runs outside the audited transaction the mutations use.
func (s *Service) ListBrackets(ctx context.Context) ([]db.Bracket, error) {
	rows, err := s.q.ListBrackets(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("adminops: list brackets: %w", err)
	}
	return rows, nil
}

//nolint:gocritic // hugeParam: the create input is a value; a pointer would invite mutation mid-call.
func (s *Service) CreateBracket(ctx context.Context, actor audit.Actor, in NewBracket) (db.Bracket, error) {
	if in.AppliesTo != "users" && in.AppliesTo != "teams" {
		return db.Bracket{}, invalidf("applies_to must be 'users' or 'teams'")
	}
	var out db.Bracket
	err := s.tx(ctx, actor, func(_ pgx.Tx, q *db.Queries) error {
		var err error
		out, err = q.AdminCreateBracket(ctx, db.AdminCreateBracketParams{
			Name: in.Name, Description: in.Description, AppliesTo: in.AppliesTo,
		})
		if err != nil {
			return fmt.Errorf("adminops: create bracket: %w", err)
		}
		return nil
	})
	return out, err
}

//nolint:gocritic // hugeParam: BracketPatch is a small value; a pointer would only add indirection.
func (s *Service) UpdateBracket(ctx context.Context, actor audit.Actor, bracketID int64, patch BracketPatch) (db.Bracket, error) {
	var out db.Bracket
	err := s.tx(ctx, actor, func(_ pgx.Tx, q *db.Queries) error {
		var err error
		out, err = q.AdminUpdateBracket(ctx, db.AdminUpdateBracketParams{
			BracketID: bracketID, Name: patch.Name, Description: patch.Description,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: id=%d", ErrBracketNotFound, bracketID)
		} else if err != nil {
			return fmt.Errorf("adminops: update bracket %d: %w", bracketID, err)
		}
		return nil
	})
	return out, err
}

// DeleteBracket removes a bracket. Its members are unassigned by the ON DELETE SET NULL FK, not
// blocked — a division can be dissolved without touching anyone's score.
func (s *Service) DeleteBracket(ctx context.Context, actor audit.Actor, bracketID int64) error {
	return s.tx(ctx, actor, func(_ pgx.Tx, q *db.Queries) error {
		n, err := q.AdminDeleteBracket(ctx, bracketID)
		if err != nil {
			return fmt.Errorf("adminops: delete bracket %d: %w", bracketID, err)
		}
		if n == 0 {
			return fmt.Errorf("%w: id=%d", ErrBracketNotFound, bracketID)
		}
		return nil
	})
}

// AssignBracket moves an account into a bracket, or clears it when bracketID is nil. The account is
// a user or a team per the instance mode. A bracket whose kind does not match the mode is refused
// rather than silently ignored: an account can only join a bracket of its own kind.
func (s *Service) AssignBracket(ctx context.Context, actor audit.Actor, mode account.Mode, accountID int64, bracketID *int64) (BracketAssignment, error) {
	var out BracketAssignment
	err := s.tx(ctx, actor, func(_ pgx.Tx, q *db.Queries) error {
		if bracketID != nil {
			br, err := q.GetBracket(ctx, *bracketID)
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("%w: id=%d", ErrBracketNotFound, *bracketID)
			} else if err != nil {
				return fmt.Errorf("adminops: assign bracket: read bracket %d: %w", *bracketID, err)
			}
			if br.AppliesTo != mode.String() {
				return invalidf("bracket %d holds %s, but this instance's accounts are %s", *bracketID, br.AppliesTo, mode)
			}
		}

		switch mode {
		case account.ModeTeams:
			row, err := q.AdminAssignTeamBracket(ctx, db.AdminAssignTeamBracketParams{TeamID: accountID, BracketID: bracketID})
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("%w: id=%d", ErrAccountNotFound, accountID)
			} else if err != nil {
				return fmt.Errorf("adminops: assign team %d bracket: %w", accountID, err)
			}
			out = BracketAssignment{AccountID: row.ID, Name: row.Name, BracketID: row.BracketID}
		default:
			row, err := q.AdminAssignUserBracket(ctx, db.AdminAssignUserBracketParams{UserID: accountID, BracketID: bracketID})
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("%w: id=%d", ErrAccountNotFound, accountID)
			} else if err != nil {
				return fmt.Errorf("adminops: assign user %d bracket: %w", accountID, err)
			}
			out = BracketAssignment{AccountID: row.ID, Name: row.Name, BracketID: row.BracketID}
		}
		return nil
	})
	return out, err
}
