//go:build integration

package security

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/starvy/flagfish/internal/adminops"
	"github.com/starvy/flagfish/internal/audit"
)

// A credential change must evict whoever else is holding the credential — including through
// an API token.
//
// Sessions solve this for free: each one carries a fingerprint of the password hash it was
// minted under, so they all die the moment the hash moves. API tokens carry nothing of the
// sort. Without an explicit delete, the standard advice — "your password leaked, change it" —
// leaves the thief a working bearer credential, and a bearer credential on this API can submit
// flags. The remediation has to actually remediate, on every path that claims to be one.
type credentialChange struct {
	name string
	// apply performs the change on the victim's behalf and returns the number of tokens the
	// path reports revoking. A path that revokes silently is as bad as one that does not
	// revoke, so the count is part of the contract and is asserted, not ignored.
	apply func(t *testing.T, f *fixture, ctx context.Context, userID int64) int64
}

func credentialChanges() []credentialChange {
	return []credentialChange{
		{
			// The one a logged-in user reaches for first. They know the current password, so
			// this may be routine rotation — but the platform cannot tell rotation from panic,
			// and guessing wrong in the other direction leaves an intruder inside.
			name: "self-service password change",
			apply: func(t *testing.T, f *fixture, ctx context.Context, userID int64) int64 {
				t.Helper()
				_, revoked, err := f.acct.ChangePassword(ctx, userID, pw, newPw)
				if err != nil {
					t.Fatalf("change password: %v", err)
				}
				return revoked
			},
		},
		{
			// The one run *because* the password is gone — the user cannot even log in to do
			// anything else. If any path must revoke, it is this one.
			name: "password reset via emailed token",
			apply: func(t *testing.T, f *fixture, ctx context.Context, userID int64) int64 {
				t.Helper()
				revoked, err := f.acct.ResetPassword(ctx, f.resetToken(userID), newPw)
				if err != nil {
					t.Fatalf("reset password: %v", err)
				}
				return revoked
			},
		},
		{
			// An admin declaring the account's credential suspect. The forced-change wall stops
			// cookies; it does not stop a token, which is precisely why the token has to go.
			name: "admin force password change",
			apply: func(t *testing.T, f *fixture, ctx context.Context, userID int64) int64 {
				t.Helper()
				admin := f.user("root", pw, asAdmin)
				_, revoked, err := adminops.New(f.pool).ForcePasswordChange(ctx, audit.Actor{ID: admin}, userID)
				if err != nil {
					t.Fatalf("force password change: %v", err)
				}
				return revoked
			},
		},
	}
}

const newPw = "a totally different password"

// TestS4b_CredentialChangeRevokesAPITokens is the regression test for the eviction gap: a
// token minted before a credential change must stop authenticating after it.
//
// It also pins the scope. The delete is keyed on user_id, and a revocation that reached one
// row too far would take an uninvolved player's tooling offline mid-event — so a bystander's
// token is asserted alive on every path.
func TestS4b_CredentialChangeRevokesAPITokens(t *testing.T) {
	for _, tc := range credentialChanges() {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t)
			ctx := context.Background()

			victim := f.user("victim", pw)
			bystander := f.user("bystander", pw)

			// Two tokens, because a revocation written as a single-row delete would pass with one.
			stolen := f.token(ctx, victim, "the attacker's copy")
			ci := f.token(ctx, victim, "the victim's own CI job")
			untouched := f.token(ctx, bystander, "someone else's, entirely uninvolved")

			for _, tok := range []string{stolen, ci, untouched} {
				if got := f.do(http.MethodGet, "/api/v1/probe", withToken(tok)).StatusCode; got != http.StatusOK {
					t.Fatalf("token should authenticate before the change: %d, want 200", got)
				}
			}

			if revoked := tc.apply(t, f, ctx, victim); revoked != 2 {
				t.Errorf("path reported %d tokens revoked, want 2 — a silent revocation is a support ticket nobody can diagnose", revoked)
			}

			// The attacker is out, and so is the victim's own tooling: revocation cannot tell
			// the two apart, which is the whole reason the count above has to be surfaced.
			for _, tok := range []string{stolen, ci} {
				if got := f.do(http.MethodGet, "/api/v1/probe", withToken(tok)).StatusCode; got != http.StatusUnauthorized {
					t.Errorf("token minted before the change STILL authenticates: %d, want 401", got)
				}
			}

			if got := f.do(http.MethodGet, "/api/v1/probe", withToken(untouched)).StatusCode; got != http.StatusOK {
				t.Errorf("an unrelated user's token was revoked: %d, want 200", got)
			}

			if n := f.count(`SELECT count(*) FROM api_tokens WHERE user_id = $1`, victim); n != 0 {
				t.Errorf("%d of the victim's api_tokens rows survived", n)
			}
			if n := f.count(`SELECT count(*) FROM api_tokens WHERE user_id = $1`, bystander); n != 1 {
				t.Errorf("bystander holds %d tokens, want 1", n)
			}
		})
	}
}

// The revocation and the password write are one transaction.
//
// A password that committed while the tokens survived is the exact bug being fixed, so the
// two must not be separable. Driving a concurrent reader mid-transaction is not something the
// service seam exposes; what can be pinned is the observable consequence — after the change,
// the new password works AND the old tokens are gone, with no intermediate state in which the
// hash has moved and a token still authenticates.
func TestS4b_RevocationCommitsWithThePasswordWrite(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	uid := f.user("owner", pw)
	tok := f.token(ctx, uid, "minted under the old password")

	fresh, revoked, err := f.acct.ChangePassword(ctx, uid, pw, newPw)
	if err != nil {
		t.Fatalf("change password: %v", err)
	}
	if revoked != 1 {
		t.Errorf("revoked %d, want 1", revoked)
	}

	// The new password took effect…
	if _, err := f.acct.Login(ctx, "owner@ctf.test", newPw); err != nil {
		t.Errorf("the new password does not work: %v", err)
	}
	// …the session the change minted is live…
	if got := f.do(http.MethodGet, "/api/v1/probe", withCookie(fresh.ID)).StatusCode; got != http.StatusOK {
		t.Errorf("the session minted by the change does not work: %d, want 200", got)
	}
	// …and the token minted under the old one is not.
	if got := f.do(http.MethodGet, "/api/v1/probe", withToken(tok)).StatusCode; got != http.StatusUnauthorized {
		t.Errorf("token survived the password change: %d, want 401", got)
	}
}

// The HTTP surface says so out loud.
//
// A credential that stops working with no explanation is indistinguishable from an outage,
// and the user is the only one who can re-mint it. The count rides the response of the very
// call that caused it.
func TestS4b_PasswordChangeResponseReportsTheRevocation(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	uid := f.user("owner", pw)
	f.token(ctx, uid, "one")
	f.token(ctx, uid, "two")

	sess, err := f.acct.Login(ctx, "owner@ctf.test", pw)
	if err != nil {
		t.Fatalf("login: %v", err)
	}

	body, err := json.Marshal(map[string]string{"current_password": pw, "new_password": newPw})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	res := f.do(http.MethodPost, "/api/v1/me/password",
		withCookie(sess.ID), withCSRF(sess.CSRFToken), withBody("application/json", body))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("change password: %d (%s)", res.StatusCode, res.Body)
	}

	var got struct {
		APITokensRevoked *int64 `json:"api_tokens_revoked"`
	}
	if err := json.Unmarshal([]byte(res.Body), &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, res.Body)
	}
	if got.APITokensRevoked == nil {
		t.Fatalf("the response does not mention the revocation at all: %s", res.Body)
	}
	if *got.APITokensRevoked != 2 {
		t.Errorf("api_tokens_revoked = %d, want 2", *got.APITokensRevoked)
	}
}

// Rotating a password hash is not changing a credential.
//
// An imported bcrypt hash is upgraded to Argon2id on the next successful login. The password
// itself is unchanged and nothing has leaked, so a login must not cost the user their tokens —
// that would make "log in once after an upgrade" a silent mass revocation across the field.
func TestS4b_LoginRehashDoesNotRevokeTokens(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	// A bcrypt hash, exactly as an import would carry — the population the login rehash exists for.
	bh, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}
	uid := f.user("imported", pw, withHash(string(bh)))
	tok := f.token(ctx, uid, "survives an upgrade")

	if _, err := f.acct.Login(ctx, "imported@ctf.test", pw); err != nil {
		t.Fatalf("login with the imported password: %v", err)
	}

	if got := f.do(http.MethodGet, "/api/v1/probe", withToken(tok)).StatusCode; got != http.StatusOK {
		t.Errorf("a hash upgrade revoked the user's token: %d, want 200", got)
	}
}

// token mints a real API token for a user and returns the plaintext.
func (f *fixture) token(ctx context.Context, userID int64, description string) string {
	f.t.Helper()
	tok, err := f.acct.CreateToken(ctx, userID, &description, time.Hour)
	if err != nil {
		f.t.Fatalf("create token: %v", err)
	}
	return tok.Plaintext
}

// resetToken plants a live reset token for a user and returns the plaintext.
//
// The mail path is not involved: this fixture has no job queue behind it, and what is under
// test is what ResetPassword does once a valid token reaches it. Only the digest is stored,
// exactly as the mailer's own path stores it.
func (f *fixture) resetToken(userID int64) string {
	f.t.Helper()
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i)
	}
	token := hex.EncodeToString(raw)
	digest := sha256.Sum256([]byte(token))
	if _, err := f.pool.Exec(context.Background(), `
        INSERT INTO email_tokens (token_hash, purpose, user_id, expires_at)
        VALUES ($1, 'reset', $2, now() + interval '1 hour')`, digest[:], userID); err != nil {
		f.t.Fatalf("plant reset token: %v", err)
	}
	return token
}
