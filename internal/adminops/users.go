package adminops

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/starvy/flagfish/internal/audit"
	"github.com/starvy/flagfish/internal/db"
)

// UserPage is one page of the user list plus the total the pagination is computed from.
type UserPage struct {
	Users []db.AdminListUsersRow
	Total int64
}

// ListUsers pages through the users, optionally narrowed by one (q, field) search pair.
// An empty q means no filter; an empty field searches the name.
func (s *Service) ListUsers(ctx context.Context, page, perPage int, q, field string) (UserPage, error) {
	rows, err := s.q.AdminListUsers(ctx, db.AdminListUsersParams{
		Q:     nilIfEmpty(q),
		Field: nilIfEmpty(field),
		Lim:   int32(perPage),              //nolint:gosec // Huma caps per_page at 100
		Off:   int32((page - 1) * perPage), //nolint:gosec // page is bounded by the handler; an over-large offset just returns an empty page
	})
	if err != nil {
		return UserPage{}, fmt.Errorf("adminops: list users: %w", err)
	}
	p := UserPage{Users: rows}
	if len(rows) > 0 {
		p.Total = rows[0].Total
	}
	return p, nil
}

func (s *Service) GetUser(ctx context.Context, userID int64) (db.AdminGetUserRow, error) {
	row, err := s.q.AdminGetUser(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.AdminGetUserRow{}, fmt.Errorf("%w: id=%d", ErrUserNotFound, userID)
	} else if err != nil {
		return db.AdminGetUserRow{}, fmt.Errorf("adminops: get user %d: %w", userID, err)
	}
	return row, nil
}

// UserPatch is a partial update over the moderatable profile fields. nil means keep; the Clear
// flags null their column. Email, role, ban, hide and membership each have their own route.
type UserPatch struct {
	Name        *string
	Website     *string
	Affiliation *string
	Country     *string

	ClearWebsite     bool
	ClearAffiliation bool
	ClearCountry     bool
}

func (s *Service) UpdateUser(ctx context.Context, actor audit.Actor, userID int64, patch UserPatch) (db.AdminUpdateUserRow, error) {
	var out db.AdminUpdateUserRow
	err := s.tx(ctx, actor, func(_ pgx.Tx, q *db.Queries) error {
		var err error
		out, err = q.AdminUpdateUser(ctx, db.AdminUpdateUserParams{
			UserID: userID,
			Name:   patch.Name, Website: patch.Website,
			Affiliation: patch.Affiliation, Country: patch.Country,
			ClearWebsite:     patch.ClearWebsite,
			ClearAffiliation: patch.ClearAffiliation,
			ClearCountry:     patch.ClearCountry,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: id=%d", ErrUserNotFound, userID)
		} else if err != nil {
			return fmt.Errorf("adminops: update user %d: %w", userID, err)
		}
		return nil
	})
	return out, err
}

func (s *Service) SetUserHidden(ctx context.Context, actor audit.Actor, userID int64, hidden bool) (db.AdminSetUserHiddenRow, error) {
	var out db.AdminSetUserHiddenRow
	err := s.tx(ctx, actor, func(_ pgx.Tx, q *db.Queries) error {
		var err error
		out, err = q.AdminSetUserHidden(ctx, db.AdminSetUserHiddenParams{UserID: userID, Hidden: hidden})
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: id=%d", ErrUserNotFound, userID)
		} else if err != nil {
			return fmt.Errorf("adminops: set user %d hidden: %w", userID, err)
		}
		return nil
	})
	return out, err
}

// ForcePasswordChange sets the flag and kills the user's live sessions in the same transaction:
// the reason to force a change is that the credential is suspect, and a suspect credential must
// not keep riding an already-minted cookie. The user logs back in (the login route is exempt)
// and is walled everywhere but the password-change endpoint until they comply.
func (s *Service) ForcePasswordChange(ctx context.Context, actor audit.Actor, userID int64) (db.AdminForcePasswordChangeRow, error) {
	var out db.AdminForcePasswordChangeRow
	err := s.tx(ctx, actor, func(_ pgx.Tx, q *db.Queries) error {
		var err error
		out, err = q.AdminForcePasswordChange(ctx, userID)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: id=%d", ErrUserNotFound, userID)
		} else if err != nil {
			return fmt.Errorf("adminops: force password change for user %d: %w", userID, err)
		}
		if err := q.DeleteUserSessions(ctx, userID); err != nil {
			return fmt.Errorf("adminops: force password change for user %d: kill sessions: %w", userID, err)
		}
		return nil
	})
	return out, err
}

// SetBanned bans or unbans, and a ban kills the user's live sessions in the same transaction —
// a banned user must not ride out an already-minted cookie. Self-ban is refused; since every
// caller is an unbanned admin, that alone guarantees a ban can never leave the instance without
// a usable admin.
func (s *Service) SetBanned(ctx context.Context, actor audit.Actor, userID int64, banned bool) (db.AdminSetUserBannedRow, error) {
	if banned && actor.ID == userID {
		return db.AdminSetUserBannedRow{}, fmt.Errorf("%w: id=%d", ErrSelfBan, userID)
	}

	var out db.AdminSetUserBannedRow
	err := s.tx(ctx, actor, func(_ pgx.Tx, q *db.Queries) error {
		var err error
		out, err = q.AdminSetUserBanned(ctx, db.AdminSetUserBannedParams{UserID: userID, Banned: banned})
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: id=%d", ErrUserNotFound, userID)
		} else if err != nil {
			return fmt.Errorf("adminops: set user %d banned: %w", userID, err)
		}
		if banned {
			if err := q.DeleteUserSessions(ctx, userID); err != nil {
				return fmt.Errorf("adminops: ban user %d: kill sessions: %w", userID, err)
			}
		}
		return nil
	})
	return out, err
}

// SetRole promotes or demotes. Demotions serialize on an advisory lock so two concurrent
// self-demotions cannot both see "another admin remains" and leave the instance with none —
// the same count-then-write trap the registration caps close the same way.
func (s *Service) SetRole(ctx context.Context, actor audit.Actor, userID int64, role string) (db.AdminSetUserRoleRow, error) {
	var out db.AdminSetUserRoleRow
	err := s.tx(ctx, actor, func(tx pgx.Tx, q *db.Queries) error {
		if role != "admin" {
			if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('admin_role'))`); err != nil {
				return fmt.Errorf("adminops: set role: lock: %w", err)
			}
			remaining, err := q.AdminCountOtherAdmins(ctx, userID)
			if err != nil {
				return fmt.Errorf("adminops: set role: count admins: %w", err)
			}
			if remaining == 0 {
				return fmt.Errorf("%w: id=%d", ErrLastAdmin, userID)
			}
		}
		var err error
		out, err = q.AdminSetUserRole(ctx, db.AdminSetUserRoleParams{UserID: userID, Role: role})
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: id=%d", ErrUserNotFound, userID)
		} else if err != nil {
			return fmt.Errorf("adminops: set user %d role: %w", userID, err)
		}
		return nil
	})
	return out, err
}
