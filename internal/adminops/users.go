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

// ForcePasswordChange sets the flag and, in the same transaction, kills the user's live sessions
// and revokes their API tokens. It reports how many tokens it revoked.
//
// The reason to force a change is that the credential is suspect, and a suspect credential must
// not keep riding an already-minted cookie — nor a bearer token, which outlives the password
// entirely and would otherwise carry an intruder through the whole remediation. The user logs
// back in (the login route is exempt) and is walled everywhere but the password-change endpoint
// until they comply; a token is not walled by that flag at all, which is exactly why it has to go.
func (s *Service) ForcePasswordChange(ctx context.Context, actor audit.Actor, userID int64) (db.AdminForcePasswordChangeRow, int64, error) {
	var (
		out     db.AdminForcePasswordChangeRow
		revoked int64
	)
	err := s.tx(ctx, actor, func(_ pgx.Tx, q *db.Queries) error {
		var err error
		out, err = q.AdminForcePasswordChange(ctx, userID)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: id=%d", ErrUserNotFound, userID)
		} else if err != nil {
			return fmt.Errorf("adminops: force password change for user %d: %w", userID, err)
		}
		if serr := q.DeleteUserSessions(ctx, userID); serr != nil {
			return fmt.Errorf("adminops: force password change for user %d: kill sessions: %w", userID, serr)
		}
		revoked, err = q.DeleteUserAPITokens(ctx, userID)
		if err != nil {
			return fmt.Errorf("adminops: force password change for user %d: revoke api tokens: %w", userID, err)
		}
		return nil
	})
	return out, revoked, err
}

// SetBanned bans or unbans, and a ban kills the user's live sessions in the same transaction —
// a banned user must not ride out an already-minted cookie. Self-ban is refused.
//
// Refusing self-ban is not on its own enough to keep an admin in the instance, because the
// two ways to remove one race: A demoting B while B bans A leaves each transaction counting
// the other as the admin that remains. So a ban takes the same lock a demotion takes and
// counts under it — one queue for both, or the count is a guess about a row somebody else
// is already changing.
func (s *Service) SetBanned(ctx context.Context, actor audit.Actor, userID int64, banned bool) (db.AdminSetUserBannedRow, error) {
	if banned && actor.ID == userID {
		return db.AdminSetUserBannedRow{}, fmt.Errorf("%w: id=%d", ErrSelfBan, userID)
	}

	var out db.AdminSetUserBannedRow
	err := s.tx(ctx, actor, func(tx pgx.Tx, q *db.Queries) error {
		if banned {
			if err := lockAdminRoster(ctx, tx); err != nil {
				return fmt.Errorf("adminops: set user %d banned: %w", userID, err)
			}
			// Read the target under the lock: whether banning it removes an admin depends on a
			// role another transaction may be in the middle of changing.
			target, err := q.AdminGetUser(ctx, userID)
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("%w: id=%d", ErrUserNotFound, userID)
			} else if err != nil {
				return fmt.Errorf("adminops: set user %d banned: read target: %w", userID, err)
			}
			if target.Role == "admin" && !target.Banned {
				remaining, err := q.AdminCountOtherAdmins(ctx, userID)
				if err != nil {
					return fmt.Errorf("adminops: set user %d banned: count admins: %w", userID, err)
				}
				if remaining == 0 {
					return fmt.Errorf("%w: id=%d", ErrLastAdmin, userID)
				}
			}
		}

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

// lockAdminRoster serializes every mutation that can cost the instance its last usable admin:
// demotion, account ban, team ban, and moving an admin onto a banned team.
//
// They all decide by counting who would be left, and a count taken outside this lock is a claim
// about rows another transaction is already rewriting — two of them each see the other as the
// admin that remains, both are satisfied, and both commit. One key for all of them, because
// serializing each against itself is what leaves the interleaving open.
func lockAdminRoster(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('admin_role'))`); err != nil {
		return fmt.Errorf("lock admin roster: %w", err)
	}
	return nil
}

// SetRole promotes or demotes. Demotions serialize on the admin-roster lock so two concurrent
// self-demotions cannot both see "another admin remains" and leave the instance with none —
// the same count-then-write trap the registration caps close the same way.
func (s *Service) SetRole(ctx context.Context, actor audit.Actor, userID int64, role string) (db.AdminSetUserRoleRow, error) {
	var out db.AdminSetUserRoleRow
	err := s.tx(ctx, actor, func(tx pgx.Tx, q *db.Queries) error {
		if role != "admin" {
			if err := lockAdminRoster(ctx, tx); err != nil {
				return fmt.Errorf("adminops: set user %d role: %w", userID, err)
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

// SetVerified marks an account's email verified, or un-marks it.
//
// This is the escape hatch for the failure the mail coherence rules cannot catch: a
// mail_server that is set but wrong — bad password, blocked port, a relay that accepts
// and drops — leaves every player registered, unverified, and 403ed out of the whole
// game, with no self-service remedy. Verification is a claim about an address the
// organizer can make on the player's behalf, so it is theirs to make, and it is audited
// like any other admin act on an account.
func (s *Service) SetVerified(ctx context.Context, actor audit.Actor, userID int64, verified bool) (db.AdminSetUserVerifiedRow, error) {
	var out db.AdminSetUserVerifiedRow
	err := s.tx(ctx, actor, func(_ pgx.Tx, q *db.Queries) error {
		var err error
		out, err = q.AdminSetUserVerified(ctx, db.AdminSetUserVerifiedParams{UserID: userID, Verified: verified})
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: id=%d", ErrUserNotFound, userID)
		} else if err != nil {
			return fmt.Errorf("adminops: set user %d verified: %w", userID, err)
		}
		return nil
	})
	return out, err
}

// VerifyAll marks every unverified account verified and reports how many it moved.
//
// When the mailer is the thing that is broken, it is broken for the whole field, and
// clicking through a paginated list one player at a time is not a recovery. One
// statement, one transaction, one actor stamped over all of it.
func (s *Service) VerifyAll(ctx context.Context, actor audit.Actor) (int64, error) {
	var n int64
	err := s.tx(ctx, actor, func(_ pgx.Tx, q *db.Queries) error {
		var err error
		n, err = q.AdminVerifyAllUsers(ctx)
		if err != nil {
			return fmt.Errorf("adminops: verify all users: %w", err)
		}
		return nil
	})
	return n, err
}
