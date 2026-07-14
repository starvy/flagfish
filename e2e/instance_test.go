//go:build e2e

package e2e

import (
	"context"
	"net/http"
	"testing"
)

// TestInstanceBranding checks the anonymous /instance endpoint and that an admin config
// change to the CTF name and theme is reflected there.
func TestInstanceBranding(t *testing.T) {
	ctx := context.Background()
	anon := anonUser()

	before, err := anon.api.InstanceWithResponse(ctx)
	if err != nil {
		t.Fatalf("instance: %v", err)
	}
	if before.JSON200 == nil {
		t.Fatalf("instance: status %d: %s", before.StatusCode(), before.Body)
	}

	name := "e2e cup " + t.Name()
	setConfig(t, map[string]any{"name": name, "theme": "core"})

	after, err := anon.api.InstanceWithResponse(ctx)
	if err != nil {
		t.Fatalf("instance: %v", err)
	}
	if after.JSON200 == nil {
		t.Fatalf("instance: status %d: %s", after.StatusCode(), after.Body)
	}
	if after.JSON200.CtfName != name {
		t.Errorf("ctf_name = %q, want %q", after.JSON200.CtfName, name)
	}
	if after.JSON200.Theme != "core" {
		t.Errorf("theme = %q, want core", after.JSON200.Theme)
	}
}

// TestInstanceServedBeforeAuth confirms /instance answers without a session — it is the one
// endpoint outside the ban wall and the setup gate.
func TestInstanceServedBeforeAuth(t *testing.T) {
	r := anonUser().publicReq(t, http.MethodGet, "/instance", nil)
	r.require(t, http.StatusOK)
}
