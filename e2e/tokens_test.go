//go:build e2e

package e2e

import (
	"context"
	"testing"

	"github.com/starvy/flagfish/e2e/client"
)

// TestAPITokenLifecycle mints a token, uses it as a bearer credential, lists it, revokes it,
// and confirms the revoked token no longer authenticates.
func TestAPITokenLifecycle(t *testing.T) {
	ctx := context.Background()
	u := mustRegister(t)

	ttl := int64(24)
	desc := "e2e token"
	created, err := u.api.CreateTokenWithResponse(ctx, client.CreateTokenInputBody{
		Description: &desc, TtlHours: &ttl,
	})
	if err != nil {
		t.Fatalf("create-token: %v", err)
	}
	if created.JSON200 == nil {
		t.Fatalf("create-token status %d: %s", created.StatusCode(), created.Body)
	}
	if created.JSON200.Token == "" {
		t.Fatal("create-token returned an empty plaintext token")
	}
	tokenID := created.JSON200.Id

	// The token authenticates as the same account.
	bearer := tokenClient(t, created.JSON200.Token)
	me, err := bearer.api.MeWithResponse(ctx)
	if err != nil || me.JSON200 == nil {
		t.Fatalf("me via token: %v (status %d)", err, me.StatusCode())
	}
	if me.JSON200.UserId != u.userID {
		t.Errorf("token account = %d, want %d", me.JSON200.UserId, u.userID)
	}

	// It shows up in the owner's token list (without the plaintext).
	list, err := u.api.ListTokensWithResponse(ctx)
	if err != nil || list.JSON200 == nil {
		t.Fatalf("list-tokens: %v (status %d)", err, list.StatusCode())
	}
	if !containsToken(list.JSON200, tokenID) {
		t.Errorf("token %d missing from list", tokenID)
	}

	// Revoke it, and confirm it stops authenticating.
	del, err := u.api.DeleteTokenWithResponse(ctx, tokenID)
	if err != nil || del.JSON200 == nil {
		t.Fatalf("delete-token: %v (status %d)", err, del.StatusCode())
	}
	dead, err := bearer.api.MeWithResponse(ctx)
	if err != nil {
		t.Fatalf("me via revoked token: %v", err)
	}
	if dead.JSON200 != nil {
		t.Error("a revoked token must not authenticate")
	}
}

func containsToken(list *client.ListTokensOutputBody, id int64) bool {
	if list.Tokens == nil {
		return false
	}
	for _, tok := range *list.Tokens {
		if tok.Id == id {
			return true
		}
	}
	return false
}
