package httpapi

import (
	"context"
	"encoding/json"
	"html"
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
	// wrong-but-honest IP beats a forged one. Empty behind a proxy is a misconfiguration, and
	// New says so.
	TrustedProxies []*net.IPNet

	// InsecureCookies drops the Secure attribute from the session cookie. The zero value is the
	// safe one on purpose — a caller that forgets this field, and a deployment that never sets
	// FLAGFISH_SECURE_COOKIES, both get Secure cookies. Only plain-HTTP development wants it set.
	InsecureCookies bool

	// MaxUploadBytes caps a multipart upload; zero means the built-in default. Every other body
	// is capped far lower and is not configurable.
	MaxUploadBytes int64

	// RequestTimeout is the per-request deadline imposed on the JSON API surfaces; zero means the
	// built-in default. The SSE stream is mounted outside this deadline — it is long-lived by
	// design and a deadline would sever it.
	RequestTimeout time.Duration
}

// DefaultRequestTimeout bounds a single JSON API request. It is a generous backstop, not an SLA:
// the point is that a wedged handler releases its pool connection rather than holding it until the
// client times out. A streaming download does not hold a pool connection while it copies from the
// object store, so this ceiling does not gate ordinary large downloads.
const DefaultRequestTimeout = 30 * time.Second

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
	if opts.MaxUploadBytes <= 0 {
		opts.MaxUploadBytes = config.DefaultMaxUploadBytes
	}
	if opts.RequestTimeout <= 0 {
		opts.RequestTimeout = DefaultRequestTimeout
	}
	if len(opts.TrustedProxies) == 0 {
		// Not fatal — a bare `flagfish serve` on a laptop has no proxy and is right not to trust
		// one. It is fatal-shaped behind a reverse proxy, though, so name both consequences.
		opts.Log.Warn("no trusted proxies configured: X-Forwarded-For is ignored. " +
			"If anything is proxying to this process, EVERY client behind it shares ONE anonymous " +
			"rate-limit bucket and every recorded submission IP is the proxy's. " +
			"Set FLAGFISH_TRUSTED_PROXIES to the proxy's address(es).")
	}
	if opts.InsecureCookies {
		opts.Log.Warn("FLAGFISH_SECURE_COOKIES=false: session cookies are issued WITHOUT Secure, " +
			"so a browser will send them over plain HTTP. Local development only.")
	}

	r := chi.NewRouter()

	// --- outside the wall -----------------------------------------------------
	// These run before authentication because they must also work when it fails. The body limit
	// is one of them: an anonymous caller must not be able to make us read a gigabyte before
	// anyone has decided who they are.
	r.Use(chimw.RequestID)
	r.Use(realIP(opts.TrustedProxies, !opts.InsecureCookies, opts.Log))
	r.Use(recoverer(opts.Log))
	r.Use(logging(opts.Log))
	r.Use(limitBody(opts.MaxUploadBytes))

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
		// The root router is handed to the limiter so it can resolve which route a request matches:
		// the bucket is keyed on the pattern and the parsed id, and the raw path is neither.
		gated.Use(rateLimit(r, opts.Limiter, opts.Log))

		// s.gated carries the raw routes Huma cannot type — the SSE stream — with NO request
		// deadline: a live notification stream is long-lived by design.
		s.gated = gated

		// The JSON API surfaces get a per-request deadline in their own nested group. The SSE
		// route stays on s.gated above, structurally outside this timeout — not by a path check
		// that could rot, but by which router it is registered on. A request to the stream path
		// matches that specific route ahead of the /api/v1/* mount, so it never inherits the
		// deadline.
		gated.Group(func(bounded chi.Router) {
			bounded.Use(requestDeadline(opts.RequestTimeout))
			s.Public = s.newAPI(bounded, "/api/v1", "flagfish", policy.SurfacePublic)
			s.Admin = s.newAPI(bounded, "/api/v1/admin", "flagfish (admin)", policy.SurfaceAdmin)
		})
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
	// bots are written against it, so each surface serves its own docs and schema. The paths are
	// relative to the sub-router this API is mounted on, which is what lands them at
	// {prefix}/docs and {prefix}/openapi.json on the wire.
	cfg.DocsPath = "/docs"
	cfg.OpenAPIPath = "/openapi"

	// The admin document is a map of every admin endpoint, and Huma registers docs and schema
	// through Adapter.Handle — which bypasses UseMiddleware, so the policy gate never sees them.
	// Left to Huma they answer 200 to anyone. They are re-registered below, behind the same
	// authorization as the routes they describe.
	if surface == policy.SurfaceAdmin {
		cfg.DocsPath, cfg.OpenAPIPath = "", ""
	}

	sub := chi.NewRouter()
	r.Mount(prefix, sub)

	api := humachi.New(sub, cfg)
	api.UseMiddleware(s.policyGate(api, surface))

	if surface == policy.SurfaceAdmin {
		s.mountAdminDocs(sub, api, prefix, title)
	}
	return api
}

// mountAdminDocs serves the admin surface's schema and reference UI to admins only: the same
// Decide, on the same class as the endpoints the document describes.
func (s *Server) mountAdminDocs(sub chi.Router, api huma.API, prefix, title string) {
	sub.Group(func(g chi.Router) {
		g.Use(s.httpGate(policy.ClassAdmin, policy.SurfaceAdmin))

		g.Get("/openapi.json", func(w http.ResponseWriter, r *http.Request) {
			doc, err := json.Marshal(api.OpenAPI())
			if err != nil {
				s.opts.Log.ErrorContext(r.Context(), "rendering the admin OpenAPI document failed", "error", err)
				problem(w, http.StatusInternalServerError, "internal-error", "internal error")
				return
			}
			s.writeDoc(r.Context(), w, "application/openapi+json", doc)
		})

		g.Get("/openapi.yaml", func(w http.ResponseWriter, r *http.Request) {
			doc, err := api.OpenAPI().YAML()
			if err != nil {
				s.opts.Log.ErrorContext(r.Context(), "rendering the admin OpenAPI document failed", "error", err)
				problem(w, http.StatusInternalServerError, "internal-error", "internal error")
				return
			}
			s.writeDoc(r.Context(), w, "application/openapi+yaml", doc)
		})

		g.Get("/docs", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Security-Policy", adminDocsCSP)
			s.writeDoc(r.Context(), w, "text/html; charset=utf-8", []byte(adminDocsHTML(title, prefix)))
		})
	})
}

func (s *Server) writeDoc(ctx context.Context, w http.ResponseWriter, contentType string, body []byte) {
	w.Header().Set("Content-Type", contentType)
	if _, err := w.Write(body); err != nil {
		// The 200 is already on the wire; the client hung up. There is no second response to send.
		s.opts.Log.WarnContext(ctx, "writing an admin doc response failed", "error", err)
	}
}

const adminDocsCSP = "default-src 'none'; base-uri 'none'; connect-src 'self'; form-action 'none'; " +
	"frame-ancestors 'none'; sandbox allow-same-origin allow-scripts; " +
	"script-src https://unpkg.com/@stoplight/elements@9.0.15/web-components.min.js; " +
	"style-src 'unsafe-inline' https://unpkg.com/@stoplight/elements@9.0.15/styles.min.css"

// adminDocsHTML is Huma's Stoplight Elements page, rendered by us because Huma's own is not behind
// the gate. It fetches the spec same-origin, so the admin's cookie carries it through that gate too.
func adminDocsHTML(title, prefix string) string {
	return `<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8">
    <meta name="referrer" content="no-referrer">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <title>` + html.EscapeString(title) + ` Reference</title>
    <link rel="stylesheet" href="https://unpkg.com/@stoplight/elements@9.0.15/styles.min.css" crossorigin integrity="sha384-iVQBHadsD+eV0M5+ubRCEVXrXEBj+BqcuwjUwPoVJc0Pb1fmrhYSAhL+BFProHdV">
    <script src="https://unpkg.com/@stoplight/elements@9.0.15/web-components.min.js" crossorigin integrity="sha384-xjOcq9PZ/k+pGtPS/xcsCRXGjKKfTlIa4H1IYEnC+97jNa6sAMWTNrV6hY08W3GL"></script>
  </head>
  <body style="height: 100vh;">
    <elements-api
      apiDescriptionUrl="` + html.EscapeString(prefix) + `/openapi.yaml"
      router="hash"
      layout="sidebar"
      tryItCredentialsPolicy="same-origin"
    ></elements-api>
  </body>
</html>`
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
