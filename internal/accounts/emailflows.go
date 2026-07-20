package accounts

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/starvy/flagfish/internal/db"
	"github.com/starvy/flagfish/internal/jobs"
)

// Token lifetimes. Verification guards nothing but the address, so it can be generous;
// a reset token is a live credential and dies fast.
const (
	verifyTokenTTL = 24 * time.Hour
	resetTokenTTL  = time.Hour
)

var (
	ErrAlreadyVerified = errors.New("accounts: email is already verified")

	// ErrTokenInvalid covers unknown, expired, and already-consumed alike. The
	// database cannot tell them apart, and neither may the caller: which one it
	// was is information about someone else's token.
	ErrTokenInvalid = errors.New("accounts: invalid or expired token")
)

// ResendVerification queues a fresh verification email for an unverified user.
// Older tokens stay valid until they expire; the newest link is not the only one
// that works, which is what a player mashing "resend" expects.
func (s *Service) ResendVerification(ctx context.Context, userID int64, ctfName string) error {
	u, err := s.q.GetUserByID(ctx, userID)
	if err != nil {
		return fmt.Errorf("accounts: resend verification: %w", err)
	}
	if u.Verified {
		return ErrAlreadyVerified
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("accounts: resend verification: %w", err)
	}
	defer tx.Rollback(ctx)

	if err := s.enqueueVerification(ctx, tx, u.ID, u.Email, ctfName); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("accounts: resend verification: %w", err)
	}
	return nil
}

// ConfirmEmail consumes a verification token and marks its user verified, atomically:
// the consume is a conditional UPDATE, so a token spends exactly once no matter how
// many requests carry it.
func (s *Service) ConfirmEmail(ctx context.Context, token string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("accounts: confirm email: %w", err)
	}
	defer tx.Rollback(ctx)
	q := s.q.WithTx(tx)

	userID, err := q.ConsumeEmailToken(ctx, db.ConsumeEmailTokenParams{
		TokenHash: digestOf(token), Purpose: "verify",
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrTokenInvalid
	} else if err != nil {
		return fmt.Errorf("accounts: confirm email: %w", err)
	}

	if err := q.MarkUserVerified(ctx, userID); err != nil {
		return fmt.Errorf("accounts: confirm email: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("accounts: confirm email: %w", err)
	}
	return nil
}

// RequestPasswordReset queues a reset email if the address belongs to an account that
// can be reset. An unknown address returns nil identically — the caller's response must
// not be an existence oracle for the registered-player list.
func (s *Service) RequestPasswordReset(ctx context.Context, email, ctfName string) error {
	u, err := s.q.GetUserByEmail(ctx, email)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	} else if err != nil {
		return fmt.Errorf("accounts: request password reset: %w", err)
	}
	if u.PasswordHash == nil {
		// An OAuth-only account has no password; a reset would mint one and bypass
		// the provider. Same silence as an unknown address.
		return nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("accounts: request password reset: %w", err)
	}
	defer tx.Rollback(ctx)

	if err := s.enqueueTokenEmail(ctx, tx, u.ID, u.Email, "reset", resetTokenTTL, resetMail(ctfName)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("accounts: request password reset: %w", err)
	}
	return nil
}

// ResetPassword consumes a reset token and sets the password. Every existing session
// dies with the old hash: sessions are fingerprinted against the password hash they
// were minted under, so the write below is also the kill switch — exactly the property
// a reset needs when the reason for it is a stolen password.
func (s *Service) ResetPassword(ctx context.Context, token, password string) error {
	hash, err := Hash(password)
	if err != nil {
		return err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("accounts: reset password: %w", err)
	}
	defer tx.Rollback(ctx)
	q := s.q.WithTx(tx)

	userID, err := q.ConsumeEmailToken(ctx, db.ConsumeEmailTokenParams{
		TokenHash: digestOf(token), Purpose: "reset",
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrTokenInvalid
	} else if err != nil {
		return fmt.Errorf("accounts: reset password: %w", err)
	}

	// A reset is a real password change, so it also discharges a pending forced change.
	if err := q.UpdatePasswordAndClearForcedChange(ctx, db.UpdatePasswordAndClearForcedChangeParams{
		UserID: userID, PasswordHash: &hash,
	}); err != nil {
		return fmt.Errorf("accounts: reset password: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("accounts: reset password: %w", err)
	}
	return nil
}

func (s *Service) enqueueVerification(ctx context.Context, tx pgx.Tx, userID int64, email, ctfName string) error {
	return s.enqueueTokenEmail(ctx, tx, userID, email, "verify", verifyTokenTTL, verificationMail(ctfName))
}

// enqueueTokenEmail mints a single-use token, stores its digest, and enqueues the mail
// on the caller's transaction. The plaintext token lives only in the queued message —
// never in a log, never in a column of its own.
func (s *Service) enqueueTokenEmail(
	ctx context.Context, tx pgx.Tx, userID int64, email, purpose string, ttl time.Duration,
	compose func(token string) (subject, body string),
) error {
	token, digest, err := newSecret()
	if err != nil {
		return err
	}
	if _, err := s.q.WithTx(tx).CreateEmailToken(ctx, db.CreateEmailTokenParams{
		TokenHash: digest,
		Purpose:   purpose,
		UserID:    userID,
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(ttl), Valid: true},
	}); err != nil {
		return fmt.Errorf("accounts: store %s token: %w", purpose, err)
	}

	subject, body := compose(token)
	if _, err := s.jobs.InsertTx(ctx, tx, jobs.SendEmail{To: email, Subject: subject, Body: body}, nil); err != nil {
		return fmt.Errorf("accounts: enqueue %s mail: %w", purpose, err)
	}
	return nil
}

func verificationMail(ctfName string) func(token string) (subject, body string) {
	return func(token string) (string, string) {
		return mailSubject(ctfName, "Verify your email address"), fmt.Sprintf(
			"Enter this code to verify your email address:\n\n%s\n\n"+
				"The code expires in 24 hours. If you did not register, ignore this message.\n",
			token,
		)
	}
}

func resetMail(ctfName string) func(token string) (subject, body string) {
	return func(token string) (string, string) {
		return mailSubject(ctfName, "Password reset"), fmt.Sprintf(
			"Enter this code to reset your password:\n\n%s\n\n"+
				"The code expires in 1 hour and works once. If you did not ask for a reset, "+
				"ignore this message — your password is unchanged.\n",
			token,
		)
	}
}

func mailSubject(ctfName, what string) string {
	if ctfName == "" {
		return what
	}
	return ctfName + ": " + what
}
