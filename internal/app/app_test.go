package app

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"

	"go.uber.org/fx"

	"github.com/starvy/flagfish/internal/config"
)

// The graph is validated, not run. fx.ValidateApp resolves every dependency and every
// invocation without constructing anything or opening a connection — so a provider that
// went missing, a type that stopped matching an interface, or a cycle introduced by a
// refactor fails HERE, in `task ci`, instead of at `flagfish serve` on a customer's box.
//
// These three cases are the three shipping topologies. If the binary can run it, a test
// covers its wiring.

func testEnv() config.Env {
	return config.Env{
		DatabaseURL: "postgres://u:p@localhost:5432/flagfish?sslmode=disable",
		Addr:        ":8000",
		RateLimit:   60,
		RateWindow:  0,
	}
}

func testLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func validate(t *testing.T, opts []fx.Option) {
	t.Helper()
	if err := fx.ValidateApp(opts...); err != nil {
		t.Fatalf("graph does not validate: %v", err)
	}
}

func TestServeGraphValidates(t *testing.T) {
	validate(t, ServeOptions(context.Background(), testEnv(), testLog(), ServeConfig{
		Addr:           ":8000",
		TrustedProxies: []*net.IPNet{},
	}))
}

func TestServeWithWorkerGraphValidates(t *testing.T) {
	validate(t, ServeOptions(context.Background(), testEnv(), testLog(), ServeConfig{
		Addr:       ":8000",
		WithWorker: true,
	}))
}

func TestWorkerGraphValidates(t *testing.T) {
	validate(t, WorkerOptions(context.Background(), testEnv(), testLog()))
}
