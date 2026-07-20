package adminops

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/starvy/flagfish/internal/audit"
	"github.com/starvy/flagfish/internal/db"
	"github.com/starvy/flagfish/internal/domain/account"
	"github.com/starvy/flagfish/internal/domain/award"
)

// ManualAward is one out-of-band point adjustment, the shape both the user-mode and team-mode
// queries collapse to. Reason is the operator's justification, stored as the award's description.
type ManualAward struct {
	ID     int64
	Value  int32
	Reason string
	Date   time.Time
}

// GrantAward records a manual point adjustment against a scoring account: a team in teams mode, a
// user in users mode — the same account the scoreboard sums. value may be negative (a penalty); a
// zero adjustment is refused as scoreboard noise. reason is required and lands in the audit trail
// with the acting admin, because an out-of-band score change must never be silent.
func (s *Service) GrantAward(ctx context.Context, actor audit.Actor, mode account.Mode, accountID int64, value int32, reason string) (ManualAward, error) {
	cleaned, err := award.ValidateManual(value, reason)
	if err != nil {
		return ManualAward{}, invalidf("%s", err.Error())
	}

	var out ManualAward
	err = s.tx(ctx, actor, func(_ pgx.Tx, q *db.Queries) error {
		switch mode {
		case account.ModeTeams:
			row, grantErr := q.GrantTeamAward(ctx, db.GrantTeamAwardParams{TeamID: accountID, Value: value, Reason: &cleaned})
			if errors.Is(grantErr, pgx.ErrNoRows) {
				// No such team, or a memberless team with no captain to record against.
				return fmt.Errorf("%w: id=%d", ErrAccountNotFound, accountID)
			} else if grantErr != nil {
				return fmt.Errorf("adminops: grant team %d award: %w", accountID, grantErr)
			}
			out = ManualAward{ID: row.ID, Value: row.Value, Reason: derefReason(row.Description), Date: row.Date.Time}
		default:
			row, grantErr := q.GrantUserAward(ctx, db.GrantUserAwardParams{UserID: accountID, Value: value, Reason: &cleaned})
			if errors.Is(grantErr, pgx.ErrNoRows) {
				return fmt.Errorf("%w: id=%d", ErrAccountNotFound, accountID)
			} else if grantErr != nil {
				return fmt.Errorf("adminops: grant user %d award: %w", accountID, grantErr)
			}
			out = ManualAward{ID: row.ID, Value: row.Value, Reason: derefReason(row.Description), Date: row.Date.Time}
		}
		return nil
	})
	return out, err
}

// RevokeAward deletes a manual award. It is a real DELETE, not a status flip: the scoreboard replays
// the ledger for time-travel, so a revoked adjustment must read as never-having-happened. Only
// type='standard' awards are deletable — a hint_unlock or first_blood award is a gameplay fact, and
// the query's own predicate refuses it whatever id is passed.
func (s *Service) RevokeAward(ctx context.Context, actor audit.Actor, awardID int64) error {
	return s.tx(ctx, actor, func(_ pgx.Tx, q *db.Queries) error {
		res, err := q.RevokeManualAward(ctx, awardID)
		if err != nil {
			return fmt.Errorf("adminops: revoke award %d: %w", awardID, err)
		}
		switch {
		case res.FoundType == "":
			return fmt.Errorf("%w: id=%d", ErrAwardNotFound, awardID)
		case res.DeletedID == 0:
			// The row exists but is a gameplay award; the delete predicate spared it.
			return fmt.Errorf("%w: id=%d type=%s", ErrAwardNotManual, awardID, res.FoundType)
		}
		return nil
	})
}

// ListManualAwards returns the manual awards on one scoring account, newest first, for the revoke
// surface. A plain read, so it runs outside the audited transaction the mutations use.
func (s *Service) ListManualAwards(ctx context.Context, mode account.Mode, accountID int64) ([]ManualAward, error) {
	switch mode {
	case account.ModeTeams:
		rows, err := s.q.ListTeamManualAwards(ctx, &accountID)
		if err != nil {
			return nil, fmt.Errorf("adminops: list team %d awards: %w", accountID, err)
		}
		out := make([]ManualAward, len(rows))
		for i, r := range rows {
			out[i] = ManualAward{ID: r.ID, Value: r.Value, Reason: derefReason(r.Description), Date: r.Date.Time}
		}
		return out, nil
	default:
		rows, err := s.q.ListUserManualAwards(ctx, accountID)
		if err != nil {
			return nil, fmt.Errorf("adminops: list user %d awards: %w", accountID, err)
		}
		out := make([]ManualAward, len(rows))
		for i, r := range rows {
			out[i] = ManualAward{ID: r.ID, Value: r.Value, Reason: derefReason(r.Description), Date: r.Date.Time}
		}
		return out, nil
	}
}

// derefReason unwraps the nullable description column. A manual award always writes a non-empty
// reason, so a NULL here would be a corrupt row; an empty string is the honest, non-panicking read.
func derefReason(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
