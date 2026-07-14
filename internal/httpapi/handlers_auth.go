package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/starvy/flagfish/internal/accounts"
	"github.com/starvy/flagfish/internal/domain/policy"
)

type registerInput struct {
	Body struct {
		Name     string `json:"name" minLength:"1" maxLength:"128"`
		Email    string `json:"email" format:"email" maxLength:"255"`
		Password string `json:"password" minLength:"8" maxLength:"128"`
	}
}

type loginInput struct {
	Body struct {
		Email    string `json:"email" format:"email"`
		Password string `json:"password"`
	}
}

type logoutInput struct {
	Session string `cookie:"flagfish_session"`
}

type changePasswordInput struct {
	Body struct {
		Current string `json:"current_password"`
		New     string `json:"new_password" minLength:"8" maxLength:"128"`
	}
}

// sessionOutput carries the session cookie plus the CSRF token the SPA echoes on writes.
type sessionOutput struct {
	SetCookie http.Cookie `header:"Set-Cookie"`
	Body      struct {
		UserID    int64     `json:"user_id"`
		CSRFToken string    `json:"csrf_token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
}

type clearedOutput struct {
	SetCookie http.Cookie `header:"Set-Cookie"`
	Body      struct {
		OK bool `json:"ok"`
	}
}

// ok is the bare acknowledgement the email flows return: they never leak whether an
// address exists, so the body is the same regardless of what happened behind it.
func ok() *okOutput {
	o := &okOutput{}
	o.Body.OK = true
	return o
}

type confirmEmailInput struct {
	Body struct {
		Token string `json:"token" minLength:"1" maxLength:"128"`
	}
}

type requestResetInput struct {
	Body struct {
		Email string `json:"email" format:"email" maxLength:"255"`
	}
}

type resetPasswordInput struct {
	Body struct {
		Token    string `json:"token" minLength:"1" maxLength:"128"`
		Password string `json:"password" minLength:"8" maxLength:"128"`
	}
}

type meOutput struct {
	Body struct {
		UserID   int64  `json:"user_id"`
		Name     string `json:"name"`
		Email    string `json:"email"`
		Role     string `json:"role"`
		Verified bool   `json:"verified"`
		IsAdmin  bool   `json:"is_admin"`
		TeamID   *int64 `json:"team_id,omitempty"`
	}
}

func (s *Server) registerAuth() {
	Register(s.Public, policy.ClassRegister, huma.Operation{
		OperationID: "register", Method: http.MethodPost, Path: "/register",
		Summary: "Register a new account", Tags: []string{"auth"},
	}, s.register)

	Register(s.Public, policy.ClassLogin, huma.Operation{
		OperationID: "login", Method: http.MethodPost, Path: "/login",
		Summary: "Log in with email and password", Tags: []string{"auth"},
	}, s.login)

	Register(s.Public, policy.ClassLogout, huma.Operation{
		OperationID: "logout", Method: http.MethodPost, Path: "/logout",
		Summary: "Log out the current session", Tags: []string{"auth"},
	}, s.logout)

	Register(s.Public, policy.ClassAccountSelf, huma.Operation{
		OperationID: "me", Method: http.MethodGet, Path: "/me",
		Summary: "Get the current account", Tags: []string{"auth"},
	}, s.me)

	Register(s.Public, policy.ClassAccountSelf, huma.Operation{
		OperationID: "change-password", Method: http.MethodPost, Path: "/me/password",
		Summary: "Change the current account's password", Tags: []string{"auth"},
	}, s.changePassword)

	Register(s.Public, policy.ClassAccountSelf, huma.Operation{
		OperationID: "verify-resend", Method: http.MethodPost, Path: "/verify/resend",
		Summary: "Resend the email verification link", Tags: []string{"auth"},
	}, s.resendVerification)

	Register(s.Public, policy.ClassConfirm, huma.Operation{
		OperationID: "verify-confirm", Method: http.MethodPost, Path: "/verify/confirm",
		Summary: "Confirm an email address with a token", Tags: []string{"auth"},
	}, s.confirmEmail)

	Register(s.Public, policy.ClassReset, huma.Operation{
		OperationID: "reset-request", Method: http.MethodPost, Path: "/reset-password",
		Summary: "Request a password reset email", Tags: []string{"auth"},
	}, s.requestPasswordReset)

	Register(s.Public, policy.ClassReset, huma.Operation{
		OperationID: "reset-apply", Method: http.MethodPatch, Path: "/reset-password",
		Summary: "Set a new password with a reset token", Tags: []string{"auth"},
	}, s.resetPassword)
}

func sessionOut(ctx context.Context, sess accounts.Session) *sessionOutput {
	out := &sessionOutput{SetCookie: *accounts.SessionCookieFor(sess, secureOf(ctx))}
	out.Body.UserID = sess.UserID
	out.Body.CSRFToken = sess.CSRFToken
	out.Body.ExpiresAt = sess.ExpiresAt
	return out
}

func (s *Server) register(ctx context.Context, in *registerInput) (*sessionOutput, error) {
	snap := s.opts.Config.Current()
	verified := !snap.VerifyEmails
	sess, err := s.opts.Accounts.Register(ctx, in.Body.Name, in.Body.Email, in.Body.Password, verified, snap.CTFName)
	switch {
	case errors.Is(err, accounts.ErrEmailTaken):
		return nil, huma.Error409Conflict("that email is already registered")
	case errors.Is(err, accounts.ErrCapReached):
		return nil, huma.Error403Forbidden("registration is full")
	case err != nil:
		s.opts.Log.ErrorContext(ctx, "register failed", "error", err)
		return nil, huma.Error500InternalServerError("could not register")
	}
	return sessionOut(ctx, sess), nil
}

func (s *Server) login(ctx context.Context, in *loginInput) (*sessionOutput, error) {
	sess, err := s.opts.Accounts.Login(ctx, in.Body.Email, in.Body.Password)
	switch {
	case errors.Is(err, accounts.ErrBadCredentials):
		return nil, huma.Error401Unauthorized("invalid email or password")
	case err != nil:
		s.opts.Log.ErrorContext(ctx, "login failed", "error", err)
		return nil, huma.Error500InternalServerError("could not log in")
	}
	return sessionOut(ctx, sess), nil
}

func (s *Server) logout(ctx context.Context, in *logoutInput) (*clearedOutput, error) {
	if in.Session != "" {
		if err := s.opts.Accounts.Logout(ctx, in.Session); err != nil {
			s.opts.Log.ErrorContext(ctx, "logout failed", "error", err)
			return nil, huma.Error500InternalServerError("could not log out")
		}
	}
	out := &clearedOutput{SetCookie: *accounts.ClearSessionCookie(secureOf(ctx))}
	out.Body.OK = true
	return out, nil
}

func (s *Server) me(ctx context.Context, _ *struct{}) (*meOutput, error) {
	pr := AuthOf(ctx).Principal
	p, err := s.opts.Accounts.Profile(ctx, pr.UserID)
	if err != nil {
		s.opts.Log.ErrorContext(ctx, "profile lookup failed", "error", err)
		return nil, huma.Error500InternalServerError("could not load account")
	}
	out := &meOutput{}
	out.Body.UserID = p.ID
	out.Body.Name = p.Name
	out.Body.Email = p.Email
	out.Body.Role = p.Role
	out.Body.Verified = p.Verified
	out.Body.IsAdmin = pr.IsAdmin
	out.Body.TeamID = p.TeamID
	return out, nil
}

func (s *Server) resendVerification(ctx context.Context, _ *struct{}) (*okOutput, error) {
	pr := AuthOf(ctx).Principal
	err := s.opts.Accounts.ResendVerification(ctx, pr.UserID, s.opts.Config.Current().CTFName)
	switch {
	case errors.Is(err, accounts.ErrAlreadyVerified):
		return nil, huma.Error409Conflict("your email is already verified")
	case err != nil:
		s.opts.Log.ErrorContext(ctx, "resend verification failed", "error", err)
		return nil, huma.Error500InternalServerError("could not send the verification email")
	}
	return ok(), nil
}

func (s *Server) confirmEmail(ctx context.Context, in *confirmEmailInput) (*okOutput, error) {
	err := s.opts.Accounts.ConfirmEmail(ctx, in.Body.Token)
	switch {
	case errors.Is(err, accounts.ErrTokenInvalid):
		return nil, huma.Error400BadRequest("that verification link is invalid or has expired")
	case err != nil:
		s.opts.Log.ErrorContext(ctx, "confirm email failed", "error", err)
		return nil, huma.Error500InternalServerError("could not confirm the email address")
	}
	return ok(), nil
}

// requestPasswordReset ALWAYS answers 200 with the same body. An unknown address, an
// OAuth-only account, or a database error must be indistinguishable from a real send —
// the response is not allowed to reveal who has an account.
func (s *Server) requestPasswordReset(ctx context.Context, in *requestResetInput) (*okOutput, error) {
	if err := s.opts.Accounts.RequestPasswordReset(ctx, in.Body.Email, s.opts.Config.Current().CTFName); err != nil {
		s.opts.Log.ErrorContext(ctx, "password reset request failed", "error", err)
	}
	return ok(), nil
}

func (s *Server) resetPassword(ctx context.Context, in *resetPasswordInput) (*okOutput, error) {
	err := s.opts.Accounts.ResetPassword(ctx, in.Body.Token, in.Body.Password)
	switch {
	case errors.Is(err, accounts.ErrTokenInvalid):
		return nil, huma.Error400BadRequest("that reset link is invalid or has expired")
	case err != nil:
		s.opts.Log.ErrorContext(ctx, "password reset failed", "error", err)
		return nil, huma.Error500InternalServerError("could not reset the password")
	}
	return ok(), nil
}

func (s *Server) changePassword(ctx context.Context, in *changePasswordInput) (*sessionOutput, error) {
	pr := AuthOf(ctx).Principal
	sess, err := s.opts.Accounts.ChangePassword(ctx, pr.UserID, in.Body.Current, in.Body.New)
	switch {
	case errors.Is(err, accounts.ErrBadCredentials):
		return nil, huma.Error401Unauthorized("current password is incorrect")
	case err != nil:
		s.opts.Log.ErrorContext(ctx, "change password failed", "error", err)
		return nil, huma.Error500InternalServerError("could not change password")
	}
	return sessionOut(ctx, sess), nil
}
