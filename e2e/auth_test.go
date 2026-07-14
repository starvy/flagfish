//go:build e2e

package e2e

import (
	"context"
	"net/http"
	"testing"

	"github.com/oapi-codegen/runtime/types"

	"github.com/starvy/flagfish/e2e/client"
)

// TestRegisterLoginLogoutMe walks the session lifecycle: register mints a usable session,
// /me reflects the account, logout invalidates it, and login mints a fresh one.
func TestRegisterLoginLogoutMe(t *testing.T) {
	ctx := context.Background()
	u := mustRegister(t)

	me, err := u.api.MeWithResponse(ctx)
	if err != nil {
		t.Fatalf("me: %v", err)
	}
	if me.JSON200 == nil {
		t.Fatalf("me: status %d: %s", me.StatusCode(), me.Body)
	}
	if me.JSON200.Email != u.email {
		t.Errorf("me.email = %q, want %q", me.JSON200.Email, u.email)
	}
	if me.JSON200.IsAdmin {
		t.Error("a freshly registered user must not be admin")
	}
	if !me.JSON200.Verified {
		t.Error("with verify_emails off a new user should be verified")
	}

	out, err := u.api.LogoutWithResponse(ctx, &client.LogoutParams{})
	if err != nil {
		t.Fatalf("logout: %v", err)
	}
	if out.StatusCode() != http.StatusOK {
		t.Fatalf("logout status %d: %s", out.StatusCode(), out.Body)
	}

	after, err := u.api.MeWithResponse(ctx)
	if err != nil {
		t.Fatalf("me after logout: %v", err)
	}
	if after.JSON200 != nil {
		t.Errorf("me after logout should be unauthenticated, got %+v", after.JSON200)
	}

	// A fresh client can log in with the same credentials.
	back := anonUser()
	if err = back.login(ctx, u.email, u.password); err != nil {
		t.Fatalf("login: %v", err)
	}
	whoami, err := back.api.MeWithResponse(ctx)
	if err != nil || whoami.JSON200 == nil {
		t.Fatalf("me after login: %v (status %d)", err, whoami.StatusCode())
	}
	if whoami.JSON200.UserId != u.userID {
		t.Errorf("logged-in user_id = %d, want %d", whoami.JSON200.UserId, u.userID)
	}
}

// TestChangePassword confirms a password change invalidates the old password and mints a
// new session, and that the new password works.
func TestChangePassword(t *testing.T) {
	ctx := context.Background()
	u := mustRegister(t)
	newPass := "an-entirely-different-secret-9"

	resp, err := u.api.ChangePasswordWithResponse(ctx, client.ChangePasswordInputBody{
		CurrentPassword: u.password, NewPassword: newPass,
	})
	if err != nil {
		t.Fatalf("change-password: %v", err)
	}
	if resp.JSON200 == nil {
		t.Fatalf("change-password status %d: %s", resp.StatusCode(), resp.Body)
	}

	old := anonUser()
	if err := old.login(ctx, u.email, u.password); err == nil {
		t.Error("the old password must no longer log in")
	}
	fresh := anonUser()
	if err := fresh.login(ctx, u.email, newPass); err != nil {
		t.Errorf("the new password should log in: %v", err)
	}
}

// TestLoginRejectsBadPassword is the negative path: wrong credentials never mint a session.
func TestLoginRejectsBadPassword(t *testing.T) {
	ctx := context.Background()
	u := mustRegister(t)
	resp, err := anonUser().api.LoginWithResponse(ctx, client.LoginInputBody{
		Email: types.Email(u.email), Password: "not-the-password",
	})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if resp.JSON200 != nil {
		t.Error("login with a wrong password must fail")
	}
}

// TestVerifyResendWithoutMailIsHandled exercises the verification-resend endpoint. With no
// SMTP configured it must not 5xx — the flow is a no-op the caller cannot distinguish, which
// is the anti-enumeration behaviour.
func TestVerifyResendWithoutMail(t *testing.T) {
	u := mustRegister(t)
	resp, err := u.api.VerifyResendWithResponse(context.Background())
	if err != nil {
		t.Fatalf("verify-resend: %v", err)
	}
	if resp.StatusCode() >= 500 {
		t.Fatalf("verify-resend 5xx: %d %s", resp.StatusCode(), resp.Body)
	}
}
