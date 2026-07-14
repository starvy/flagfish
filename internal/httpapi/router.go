package httpapi

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/starvy/flagfish/internal/accounts"
	"github.com/starvy/flagfish/internal/adminops"
	"github.com/starvy/flagfish/internal/anticheat"
	"github.com/starvy/flagfish/internal/auth"
	"github.com/starvy/flagfish/internal/board"
	"github.com/starvy/flagfish/internal/catalog"
	"github.com/starvy/flagfish/internal/config"
	"github.com/starvy/flagfish/internal/domain/policy"
	"github.com/starvy/flagfish/internal/files"
	"github.com/starvy/flagfish/internal/gameplay"
	"github.com/starvy/flagfish/internal/notify"
	"github.com/starvy/flagfish/internal/web"
)

// Options are what the HTTP layer needs from the outside world: the config snapshot, the auth and
// rate-limit seams, and the feature services the handlers drive. This package never opens a database
// connection — the services own their pools.
type Options struct {
	Config  *config.Manager
	Auth    Authenticator
	Limiter Limiter
	Log     *slog.Logger

	Accounts  *accounts.Service
	Gameplay  *gameplay.Service
	Catalog   *catalog.Service
	Board     *board.Service
	AdminOps  *adminops.Service
	Anticheat *anticheat.Service
	Files     *files.Service

	// Notify is the notifications service; Broadcaster is the live SSE fan-out. Both are needed for
	// the notifications endpoints, and either being nil disables them (the router warns at boot).
	Notify      *notify.Service
	Broadcaster *notify.Broadcaster

	// TrustedProxies is the list of networks whose X-Forwarded-For we believe. Empty means
	// trust nobody and record the socket peer: submission IPs are anti-cheat evidence, and a
	// wrong-but-honest IP beats a forged one.
	TrustedProxies []*net.IPNet
}

// A Server is the router plus the two Huma APIs — public and admin. Two paths on purpose:
// a response shape that varies by role cannot be typed in OpenAPI, but one that varies by
// URL can — and the policy surface becomes which mux the request arrived on, not something
// a handler asserts about itself.
type Server struct {
	Router chi.Router
	Public huma.API
	Admin  huma.API

	// gated is the sub-router behind the authenticated middleware chain. Raw routes that Huma
	// cannot express — the SSE stream is not a JSON operation — mount here so they sit behind the
	// same auth, ban wall, and rate limit as everything else.
	gated chi.Router

	opts Options
}

// New builds the router and the middleware chain. The order is load-bearing: auth resolves
// cookie or token in one step, so the ban wall after it covers both credentials.
//
//nolint:gocritic // hugeParam: Options is assembled once at wiring time; a pointer would only add indirection.
func New(opts Options) *Server {
	if opts.Limiter == nil {
		opts.Limiter = auth.NoLimit{}
		opts.Log.Warn("no rate limiter configured: every endpoint is unlimited")
	}
	if opts.Auth == nil {
		opts.Auth = auth.Anonymous{}
		opts.Log.Warn("no authenticator configured: every caller is anonymous")
	}

	r := chi.NewRouter()

	// --- outside the wall -----------------------------------------------------
	// These four are safe for anyone, including a banned account, and they run
	// before authentication because they must also work when it fails.
	r.Use(chimw.RequestID)
	r.Use(realIP(opts.TrustedProxies))
	r.Use(recoverer(opts.Log))
	r.Use(logging(opts.Log))

	// Health and static assets mount outside the authenticated chain, which makes their
	// ban-exemption structural rather than a hand-maintained list. A banned user must still
	// be able to load the CSS that renders the page telling them they are banned.
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte("ok")); err != nil {
			// The 200 is already on the wire; the probe hung up. Say so and move on.
			opts.Log.WarnContext(r.Context(), "healthz write failed", "error", err)
		}
	})

	s := &Server{Router: r, opts: opts}

	// --- behind the wall ------------------------------------------------------
	r.Group(func(gated chi.Router) {
		gated.Use(authenticate(opts.Auth, opts.Log))
		gated.Use(banWall()) // covers cookie and token. See PolicyBanCoversTokenAuth.
		gated.Use(csrf())
		gated.Use(rateLimit(opts.Limiter, opts.Log))

		s.gated = gated
		s.Public = s.newAPI(gated, "/api/v1", "flagfish", policy.SurfacePublic)
		s.Admin = s.newAPI(gated, "/api/v1/admin", "flagfish (admin)", policy.SurfaceAdmin)
	})

	s.registerRoutes()

	// The SPA sits outside the authenticated chain, alongside health: a banned
	// user must still be able to load the page that tells them they are banned.
	// It is the NotFound handler, so the mounted /api/v1 subtree keeps its own
	// JSON 404s and only genuine app routes fall through to index.html.
	r.NotFound(web.Handler(opts.Log).ServeHTTP)

	return s
}

func (s *Server) newAPI(r chi.Router, prefix, title string, surface policy.Surface) huma.API {
	cfg := huma.DefaultConfig(title, "1.0.0")
	cfg.Servers = []*huma.Server{{URL: prefix}}
	// The OpenAPI document is a contract: the TypeScript client, flagfishctl, and third-party
	// bots are written against it, so each surface serves its own docs and schema.
	cfg.DocsPath = prefix + "/docs"
	cfg.OpenAPIPath = prefix + "/openapi"

	sub := chi.NewRouter()
	r.Mount(prefix, sub)

	api := humachi.New(sub, cfg)
	api.UseMiddleware(s.policyGate(api, surface))
	return api
}

// policyGate is a Huma middleware so it can read the operation's declared RouteClass. An
// operation registered without one is denied — Decide fails closed on ClassUnknown, so a
// forgotten annotation cannot silently publish an endpoint.
//
// contextcheck only recognises *http.Request and a context.Context parameter as carriers, so a
// Huma middleware — which cannot take a ctx parameter — reads to it as a broken chain.
//
//nolint:contextcheck // huma.Context is the context here, and it is threaded (ctx.Context()).
func (s *Server) policyGate(api huma.API, surface policy.Surface) func(huma.Context, func(huma.Context)) {
	return func(ctx huma.Context, next func(huma.Context)) {
		snap := s.opts.Config.Current()
		caller := AuthOf(ctx.Context())

		p := policy.Policy{
			E: snap.Event(time.Now()),
			P: caller.Principal,
			R: policy.Request{
				Class:     classOfOperation(ctx.Operation()),
				Surface:   surface,
				AdminView: ctx.Query("view") == "admin",
				Preview:   ctx.Query("preview") != "",
			},
		}

		if out := policy.Decide(p); out.Denied() {
			status := out.Status
			if status == 0 {
				status = http.StatusForbidden
			}
			// An API caller gets the status; the redirect is for the HTML transport
			// and is carried on the Outcome for it. Same decision, two renderings.
			if out.Redirect != "" {
				ctx.SetHeader("Location", out.Redirect)
			}
			if err := huma.WriteErr(api, ctx, status, out.Reason.String()); err != nil {
				s.opts.Log.ErrorContext(ctx.Context(), "writing a policy denial failed",
					"error", err, "status", status, "reason", out.Reason)
			}
			return
		}

		// Handler guards and response redaction read this same Policy: one decision,
		// every consumer, nothing to drift from.
		ctx = huma.WithValue(ctx, ctxPolicy, p)
		ctx = huma.WithValue(ctx, ctxClass, p.R.Class)
		next(ctx)
	}
}

// httpGate is the policy gate for a raw net/http route. Huma's policyGate reads the class off the
// operation, but a raw route has no operation — so the class is passed in here, and the same
// Decide runs. Without this a chi route mounted behind the auth chain would still skip the one
// place that turns "who is calling" into allow or deny.
func (s *Server) httpGate(class policy.RouteClass, surface policy.Surface) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p := policy.Policy{
				E: s.opts.Config.Current().Event(time.Now()),
				P: AuthOf(r.Context()).Principal,
				R: policy.Request{Class: class, Surface: surface},
			}
			if out := policy.Decide(p); out.Denied() {
				status := out.Status
				if status == 0 {
					status = http.StatusForbidden
				}
				if out.Redirect != "" {
					w.Header().Set("Location", out.Redirect)
				}
				problem(w, status, out.Reason.String(), out.Reason.String())
				return
			}
			ctx := context.WithValue(r.Context(), ctxPolicy, p)
			ctx = context.WithValue(ctx, ctxClass, class)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

const metadataClass = "policy.class"

// Register is how a feature package publishes an operation. The RouteClass is a
// required argument, not an option: an endpoint that has not said what it is cannot
// be gated, and an ungated endpoint is a vulnerability with an OpenAPI schema.
//
// op is taken by value, deliberately: we stamp the class into its Metadata, and a
// pointer would write that into the caller's operation. The copy also mirrors
// huma.Register's own signature, which is what callers already have in hand.
//
//nolint:gocritic // hugeParam: see above — the copy is the point.
func Register[I, O any](api huma.API, class policy.RouteClass, op huma.Operation, handler func(context.Context, *I) (*O, error)) {
	if op.Metadata == nil {
		op.Metadata = map[string]any{}
	}
	op.Metadata[metadataClass] = class
	huma.Register(api, op, handler)
}

func classOfOperation(op *huma.Operation) policy.RouteClass {
	if op == nil || op.Metadata == nil {
		return policy.ClassUnknown
	}
	c, ok := op.Metadata[metadataClass].(policy.RouteClass)
	if !ok {
		return policy.ClassUnknown
	}
	return c
}
