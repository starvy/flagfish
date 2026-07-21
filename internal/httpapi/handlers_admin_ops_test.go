package httpapi

import (
	"io"
	"log/slog"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"

	"github.com/starvy/flagfish/internal/domain/account"
	"github.com/starvy/flagfish/internal/domain/policy"
	"github.com/starvy/flagfish/internal/opsjob"
)

// Every async-ops route must be gated as ClassAdmin, and ClassAdmin must deny a non-admin. Together
// these are "non-admin refused": the policy gate reads the class off the operation, and a route that
// forgot its class would fail closed as ClassUnknown — so this asserts the class is really stamped.
func TestAdminOpsRoutesAreAdminGated(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	api := humachi.New(chi.NewRouter(), huma.DefaultConfig("test", "1"))
	s := &Server{Admin: api, opts: Options{Log: log, Ops: &opsjob.Service{}}}
	s.registerAdminOps()

	wantPaths := map[string]bool{
		"/backup": false, "/restore": false, "/import": false,
		"/tasks/{id}": false, "/tasks/{id}/download": false,
	}
	for path, item := range api.OpenAPI().Paths {
		if _, tracked := wantPaths[path]; !tracked {
			continue
		}
		wantPaths[path] = true
		for _, op := range operationsOf(item) {
			if got := classOfOperation(op); got != policy.ClassAdmin {
				t.Errorf("%s %s registered as class %v, want ClassAdmin", op.Method, path, got)
			}
		}
	}
	for path, seen := range wantPaths {
		if !seen {
			t.Errorf("expected ops route %q was not registered", path)
		}
	}

	// The class means what the gate makes it mean: ClassAdmin denies a plain authenticated user and
	// admits an admin. If this ever flips, every route above is exposed.
	live := policy.Event{SetupDone: true, Mode: account.ModeUsers}
	nonAdmin := policy.Policy{E: live, P: policy.Principal{Authed: true, Verified: true}, R: policy.Request{Class: policy.ClassAdmin, Surface: policy.SurfaceAdmin}}
	if !policy.Decide(nonAdmin).Denied() {
		t.Fatal("ClassAdmin must deny a non-admin caller")
	}
	admin := policy.Policy{E: live, P: policy.Principal{Authed: true, Verified: true, IsAdmin: true}, R: policy.Request{Class: policy.ClassAdmin, Surface: policy.SurfaceAdmin}}
	if policy.Decide(admin).Denied() {
		t.Fatal("ClassAdmin must admit an admin caller")
	}
}

func operationsOf(item *huma.PathItem) []*huma.Operation {
	all := []*huma.Operation{item.Get, item.Post, item.Put, item.Patch, item.Delete}
	out := all[:0]
	for _, op := range all {
		if op != nil {
			out = append(out, op)
		}
	}
	return out
}
