package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/starvy/flagfish/internal/accounts"
	"github.com/starvy/flagfish/internal/domain/policy"
)

// The middleware chain. The order is not decoration:
//
//	request-id → real-IP → recover → structured-log → OTel span
//	  → auth (session cookie | API token)
//	  → ban wall      ← covers both auth paths; running it before the token hook would be an authz bypass
//	  → CSRF          ← cookie auth only; token auth is CSRF-exempt
//	  → rate limit
//	  → policy gate   ← Decide(Policy)
//	  → handler

// realIP extracts the client address, honouring X-Forwarded-For only from a
// trusted proxy.
//
// This is not a nicety. submissions.ip is a recorded field and it is load-bearing for
// the anti-cheat detectors. Getting it wrong does not break the app — it silently
// poisons the evidence, which is worse, because you find out when it is already too late.
//
// chi's RealIP is not used: it trusts X-Forwarded-For from anyone, which means any
// client can forge their own recorded IP by setting a header. Behind a proxy — which
// is every real deployment — you need a trusted-proxy list, and the list has to be
// consulted, so it has to be here.
func realIP(trusted []*net.IPNet, secureCookies bool, log *slog.Logger) func(http.Handler) http.Handler {
	var warned atomic.Bool
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Something is forwarding to us and we trust nobody: every request now presents the
			// forwarder's address, so the rate limiter sees the whole internet as one client and
			// the recorded submission IP is the proxy's. Say so — once, and in full.
			if len(trusted) == 0 && r.Header.Get("X-Forwarded-For") != "" && warned.CompareAndSwap(false, true) {
				log.WarnContext(r.Context(), "requests carry X-Forwarded-For but no proxy is trusted: "+
					"the header is ignored, so every client behind that proxy shares ONE rate-limit bucket "+
					"and every recorded IP is the proxy's. Set FLAGFISH_TRUSTED_PROXIES to the proxy's address(es).",
					"peer", r.RemoteAddr)
			}

			// Peer trust is read before clientIP rewrites RemoteAddr — afterwards the socket
			// peer is gone, and X-Forwarded-Proto must be gated on the same list as X-Forwarded-For.
			trustedPeer := peerTrusted(r, trusted)
			r.RemoteAddr = clientIP(r, trusted)
			ctx := r.Context()
			if addr, err := netip.ParseAddr(r.RemoteAddr); err == nil {
				ctx = context.WithValue(ctx, ctxClientIP, addr)
			}
			ctx = context.WithValue(ctx, ctxSecure, isSecure(r, trustedPeer, secureCookies))
			// The cookie's Secure attribute is on by operator default; HSTS is not. It is a
			// commitment, so it is claimed only when this request is provably TLS — arrived over
			// TLS, or forwarded https by a trusted proxy — which is isSecure without the default.
			ctx = context.WithValue(ctx, ctxServedTLS, isSecure(r, trustedPeer, false))
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func peerTrusted(r *http.Request, trusted []*net.IPNet) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && isTrusted(ip, trusted)
}

// isSecure decides the session cookie's Secure attribute.
//
// secureCookies is the operator's answer and it defaults to yes, which is the whole point: the
// shipped topology terminates TLS at a proxy and speaks plain HTTP to us, so deriving Secure from
// "did this request arrive over TLS" hands out cookies without it on a real HTTPS site whenever the
// proxy is not in the trusted list. An operator opts out only to serve plain http:// themselves.
//
// TLS and a trusted X-Forwarded-Proto still force it on regardless — an opt-out that survives a
// move to HTTPS would be the same silent failure with a different cause. X-Forwarded-Proto is
// believed only from a trusted proxy: an untrusted client must not be able to flip the attribute
// by setting a header.
func isSecure(r *http.Request, trustedPeer, secureCookies bool) bool {
	if secureCookies || r.TLS != nil {
		return true
	}
	return trustedPeer && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func clientIP(r *http.Request, trusted []*net.IPNet) string {
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		peer = r.RemoteAddr
	}
	ip := net.ParseIP(peer)
	if ip == nil || !isTrusted(ip, trusted) {
		// The peer is not a proxy we trust, so whatever it claims about the "real"
		// client is a claim it made about itself. Believe the socket.
		return peer
	}

	// Walk right-to-left: the rightmost entry was appended by our proxy and is the
	// only one it vouched for. Entries further left are whatever the client sent.
	xff := r.Header.Get("X-Forwarded-For")
	for i := len(xff); i > 0; {
		j := strings.LastIndex(xff[:i], ",")
		candidate := strings.TrimSpace(xff[j+1 : i])
		i = j
		if parsed := net.ParseIP(candidate); parsed != nil && !isTrusted(parsed, trusted) {
			return candidate
		}
		if j < 0 {
			break
		}
	}
	return peer
}

// clientKey identifies an unauthenticated caller to the rate limiter: the client address realIP
// resolved, rather than whatever socket the request last hopped through.
func clientKey(r *http.Request) string {
	if addr, ok := r.Context().Value(ctxClientIP).(netip.Addr); ok {
		return addr.String()
	}
	return r.RemoteAddr
}

func isTrusted(ip net.IP, trusted []*net.IPNet) bool {
	for _, n := range trusted {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// maxJSONBodyBytes caps every request body that is not a multipart upload. Anonymous routes take
// bodies (login, register), so this bound is what stands between a stranger and our heap.
const maxJSONBodyBytes int64 = 1 << 20

// limitBody bounds what a caller can make us read, before routing and before authentication.
//
// Huma caps a JSON body per operation, but the multipart path does not go through that cap: it
// hands the request to ParseMultipartForm, which streams to a temp file and stops at nothing. So
// the outer bound has to live here. A body that declares itself too large is refused at the header;
// one that lies is cut off by MaxBytesReader mid-read.
func limitBody(uploadMax int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			limit := maxJSONBodyBytes
			if ct, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err == nil && ct == "multipart/form-data" {
				limit = uploadMax
			}
			if r.ContentLength > limit {
				problem(w, http.StatusRequestEntityTooLarge, "body-too-large",
					fmt.Sprintf("request body is too large (limit %d bytes)", limit))
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, limit)
			next.ServeHTTP(w, r)
		})
	}
}

// requestDeadline attaches a deadline to the request context. A wedged handler or a slow query is
// then cut instead of pinning a pool connection until the client gives up, which under load is how
// the pool drains and the whole event stalls. It is a context deadline, not http.TimeoutHandler, on
// purpose: TimeoutHandler buffers the entire response to substitute a 503, which would break the
// streaming file download; a context deadline lets pgx and the object store observe cancellation and
// unwind on their own. It is mounted only on the JSON API group — the SSE stream, which is long-lived
// by design, is registered outside it.
func requestDeadline(d time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), d)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// spaCSP is the Content-Security-Policy carried by every response. It is sized to the embedded
// SPA and no wider: the production Vite build loads a single external module script and its
// stylesheet from our own origin, the JSON API and the SSE stream are same-origin, and the only
// non-self asset is the inline data: favicon. 'unsafe-inline' is granted to styles — React writes
// inline style props and a code-split build can inject a <style> — but never to scripts:
// script-src stays 'self', so an injected <script>, a javascript: URL, or reflected author
// markdown or a hostile team name cannot execute. frame-ancestors and X-Frame-Options both forbid
// framing; base-uri, object-src and form-action are closed to what the SPA actually uses.
const spaCSP = "default-src 'self'; base-uri 'none'; object-src 'none'; frame-ancestors 'none'; " +
	"img-src 'self' data:; font-src 'self' data:; style-src 'self' 'unsafe-inline'; " +
	"script-src 'self'; connect-src 'self'; form-action 'self'"

// securityHeaders stamps the response-hardening headers onto every response. It runs before any
// handler, so a handler that writes its own status — a 413, a policy denial, the SPA shell — still
// carries them. HSTS is emitted only when the request reached us over TLS, directly or via a
// trusted proxy's X-Forwarded-Proto (the same signal that decides the Secure cookie): announcing
// it on a deployment that legitimately serves plain HTTP would pin browsers to a scheme it does
// not speak. A route needing a different policy — the admin reference UI loads from a CDN —
// overwrites Content-Security-Policy with its own, and Set replaces, so there is one header.
func securityHeaders() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("X-Frame-Options", "DENY")
			h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
			h.Set("Content-Security-Policy", spaCSP)
			if servedOverTLS(r.Context()) {
				h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
			}
			next.ServeHTTP(w, r)
		})
	}
}

const requestIDHeader = "X-Request-Id"

// maxRequestIDLen bounds an inbound id we are willing to adopt: a proxy that sets one keeps it
// short, and an unbounded value we echo and log is a header-smuggling and log-injection vector.
const maxRequestIDLen = 128

// requestID assigns a correlation id to every request and returns it as X-Request-Id, so a caller
// holding a response can quote the exact id that names its log lines. An inbound id is honoured
// only from a trusted proxy — otherwise any client could pin, collide, or forge the id that
// appears in our logs. Trust is read from the socket peer, so this runs before realIP rewrites
// RemoteAddr.
func requestID(trusted []*net.IPNet) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get(requestIDHeader)
			if id == "" || !peerTrusted(r, trusted) || !validRequestID(id) {
				id = newRequestID()
			}
			w.Header().Set(requestIDHeader, id)
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxRequestID, id)))
		})
	}
}

// newRequestID mints a random id. crypto/rand is used so ids do not collide across processes the
// way a per-process counter would; a failing generator is a broken machine, not a runtime path, so
// a time-based fallback keeps the request correlated rather than blank.
func newRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b[:])
}

// validRequestID keeps an adopted inbound id to a bounded, safe token, so a trusted proxy's header
// cannot carry control characters into a log line or fold a second header into the response.
func validRequestID(s string) bool {
	if s == "" || len(s) > maxRequestIDLen {
		return false
	}
	for i := range len(s) {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.':
		default:
			return false
		}
	}
	return true
}

// requestIDLog wraps a slog.Handler so every record whose context carries a request id is stamped
// with it. Correlation then costs the call sites nothing: any *Context log — the access line, a
// panic, a handler's own warning — is tied back to the response the caller holds, without each one
// remembering to add the field.
type requestIDLog struct{ slog.Handler }

// slog.Handler fixes this signature: Record is passed by value by the interface, so the
// hugeParam suggestion to take a pointer cannot apply here.
//
//nolint:gocritic // hugeParam: slog.Handler.Handle takes Record by value; the signature is not ours to change.
func (h requestIDLog) Handle(ctx context.Context, r slog.Record) error {
	if id, ok := ctx.Value(ctxRequestID).(string); ok && id != "" {
		r.AddAttrs(slog.String("request_id", id))
	}
	if err := h.Handler.Handle(ctx, r); err != nil {
		return fmt.Errorf("request-id log handler: %w", err)
	}
	return nil
}

func (h requestIDLog) WithAttrs(as []slog.Attr) slog.Handler {
	return requestIDLog{h.Handler.WithAttrs(as)}
}

func (h requestIDLog) WithGroup(name string) slog.Handler {
	return requestIDLog{h.Handler.WithGroup(name)}
}

// recoverer turns a panic into a 500 and a log line, and keeps the process up.
func recoverer(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// The context is captured explicitly rather than read off the request
			// inside the deferred closure: the handler may have replaced r, and the
			// log line for a panic must carry the context the request arrived with.
			ctx, path := r.Context(), r.URL.Path
			defer func(ctx context.Context) {
				if rec := recover(); rec != nil {
					log.ErrorContext(ctx, "panic", "error", rec, "path", path)
					problem(w, http.StatusInternalServerError, "internal-error", "internal error")
				}
			}(ctx)
			next.ServeHTTP(w, r)
		})
	}
}

// logging emits one structured line per request.
func logging(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}

			next.ServeHTTP(sw, r)

			log.InfoContext(
				r.Context(), "request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", sw.status,
				"duration_ms", time.Since(start).Milliseconds(),
				"ip", r.RemoteAddr,
			)
		})
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// Flush forwards to the underlying writer so the SSE stream can push each event immediately. Without
// it this wrapper would hide the Flusher the handler needs, and the stream would buffer.
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// authenticate resolves the Principal from either credential, once, before anything
// downstream can make a decision that depends on it.
//
// "The credential is bad" and "we could not check the credential" are different answers and get
// different statuses. Collapsing them into a 401 whose body is err.Error() tells an anonymous
// caller that the database is down, and tells them in pgx's words — a 401 that is really a 503,
// with our internals in the detail field.
func authenticate(a Authenticator, log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			auth, err := a.Authenticate(r.Context(), r)
			switch {
			case errors.Is(err, accounts.ErrUnauthorized):
				log.WarnContext(r.Context(), "credential rejected", "error", err)
				problem(w, http.StatusUnauthorized, "invalid-credentials", "invalid or expired credential")
				return
			case err != nil:
				// Our failure, not the caller's. The real error goes to the log, where it is
				// useful; the caller gets a status that says "try again", not a stack of wrapped
				// driver text that says which database we run.
				log.ErrorContext(r.Context(), "authentication failed", "error", err)
				problem(w, http.StatusServiceUnavailable, "unavailable", "could not verify credentials")
				return
			}
			ctx := context.WithValue(r.Context(), ctxAuth, auth)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// banWall is the wall, and it stands in one place.
//
// It reads the Principal, which by now was resolved from either a cookie or a token
// — so it covers both, and it cannot fail to cover both, because it cannot see the
// difference. Running the wall before a token hook populates the session is how a
// banned user holding a valid API token keeps full access including flag submission;
// here there is nothing for it to run before.
//
// It is also enforced inside Decide. That is not redundancy for its own sake: the
// wall is the thing we are least willing to have a future refactor quietly remove, so
// it is true in the middleware and true in the pure function, and there is a test for
// each.
//
// The ban exemption (theme assets, static files) is structural rather than a list of
// special cases: exempt routes are mounted outside the authenticated chain, so the
// wall is not something they get past — it is something they are not behind. A list
// of exemptions is a thing you can forget to update; a subtree is not.
func banWall() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			pr := AuthOf(r.Context()).Principal

			if pr.Authed {
				switch {
				case pr.Banned:
					problem(w, http.StatusForbidden, "banned", "You have been banned from this CTF")
					return
				case pr.TeamBanned:
					problem(w, http.StatusForbidden, "team-banned", "Your team has been banned from this CTF")
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// csrf protects cookie-authenticated writes only.
//
// Token auth is exempt, and that is correct rather than a concession: CSRF exists
// because a browser attaches cookies to cross-site requests on its own. It does not
// attach an Authorization header on its own. There is nothing to protect against.
func csrf() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			auth := AuthOf(r.Context())

			if auth.Method != MethodCookie || safeMethod(r.Method) {
				next.ServeHTTP(w, r)
				return
			}

			sent := r.Header.Get("CSRF-Token")
			if sent == "" || subtle.ConstantTimeCompare([]byte(sent), []byte(auth.CSRFToken)) != 1 {
				problem(w, http.StatusForbidden, "csrf", "missing or invalid CSRF token")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func safeMethod(m string) bool {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return true
	}
	return false
}

// rateLimit keys on the account when we know it, and on the client IP when we do
// not — so an anonymous flood is limited per-source and an authenticated one
// per-account, which is what you want when a single team is behind one NAT. The other half of the
// key is the route (see bucketRoute), never the raw path.
//
// The IP is the one realIP resolved, which is the client's only if a proxy in front of us is
// trusted. Untrusted, every request behind that proxy keys on the proxy's own address and the
// anonymous bucket becomes global — one stranger can then rate-limit /login for everyone. That
// misconfiguration is warned about loudly at boot and on the first forwarded request.
func rateLimit(routes chi.Routes, l Limiter, log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			pr := AuthOf(r.Context()).Principal

			key := "ip:" + clientKey(r)
			if pr.Authed {
				key = fmt.Sprintf("account:%d", pr.AccountID)
			}

			ok, err := l.Allow(r.Context(), key+":"+bucketRoute(routes, r))
			if err != nil {
				// A limiter that cannot answer must not become an open door.
				log.ErrorContext(r.Context(), "rate limiter failed", "error", err)
				problem(w, http.StatusServiceUnavailable, "unavailable", "rate limiter unavailable")
				return
			}
			if !ok {
				problem(w, http.StatusTooManyRequests, "rate-limited", "too many requests")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// credentialRoutes are the brute-force-sensitive routes: password guessing (login), account-spray
// (register), and reset-token guessing. They earn a second, tighter budget on top of the general
// limiter, so a guessing flood trips long before the general per-route budget would. Keyed by
// "METHOD "+the matched route pattern — the same canonical route the general limiter buckets on,
// never the raw path.
var credentialRoutes = map[string]bool{
	http.MethodPost + " /api/v1/login":           true,
	http.MethodPost + " /api/v1/register":        true,
	http.MethodPost + " /api/v1/reset-password":  true,
	http.MethodPatch + " /api/v1/reset-password": true,
	http.MethodPost + " /api/v1/verify/confirm":  true,
}

// authRateLimit imposes a tighter budget on the credential routes and passes everything else
// straight through. It runs after the general rateLimit, not instead of it: the general limiter is
// unchanged and still applies: this only adds a lower ceiling where brute force lives. The bucket
// is namespaced ("auth:") so it never shares a row with the general limiter, and keyed on the
// caller and the canonical route exactly as the general limiter is.
func authRateLimit(routes chi.Routes, l Limiter, log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path := r.URL.RawPath // match chi's own routing: it routes on RawPath when the path had escapes
			if path == "" {
				path = r.URL.Path
			}
			rctx := chi.NewRouteContext()
			if !routes.Match(rctx, r.Method, path) || !credentialRoutes[r.Method+" "+rctx.RoutePattern()] {
				next.ServeHTTP(w, r)
				return
			}

			pr := AuthOf(r.Context()).Principal
			key := "ip:" + clientKey(r)
			if pr.Authed {
				key = fmt.Sprintf("account:%d", pr.AccountID)
			}

			ok, err := l.Allow(r.Context(), "auth:"+key+":"+r.Method+" "+rctx.RoutePattern())
			if err != nil {
				log.ErrorContext(r.Context(), "auth rate limiter failed", "error", err)
				problem(w, http.StatusServiceUnavailable, "unavailable", "rate limiter unavailable")
				return
			}
			if !ok {
				problem(w, http.StatusTooManyRequests, "rate-limited", "too many requests")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// bucketRoute names the route a request is limited against: the matched route pattern, plus its
// path parameters in the canonical form the handler will see.
//
// The raw URL path cannot be the key. chi matches any segment against {id} and Huma parses it with
// strconv.ParseInt, so /challenges/7/attempt, /challenges/007/attempt and /challenges/+7/attempt all
// reach challenge 7 — but as three different strings they are three separate buckets, each with a
// full budget, and the padding space has no end. Since max_attempts defaults to unlimited, on most
// challenges the limiter is the only thing standing between a script and the flag.
//
// The pattern is not available from the request's own RouteContext here: this middleware runs on the
// group, where chi has routed no further than the /api/v1/* mount. So the router is asked to resolve
// the route itself, which is the same radix lookup it is about to do anyway.
//
// A path that matches no route shares one bucket per caller. Otherwise every request to a made-up
// path mints a rate_limits row that nothing will ever read again.
func bucketRoute(routes chi.Routes, r *http.Request) string {
	path := r.URL.RawPath // chi routes on RawPath when the path had escapes; match it exactly
	if path == "" {
		path = r.URL.Path
	}

	rctx := chi.NewRouteContext()
	if !routes.Match(rctx, r.Method, path) {
		return r.Method + ":<unrouted>"
	}

	var b strings.Builder
	b.WriteString(r.Method)
	b.WriteByte(':')
	b.WriteString(rctx.RoutePattern())
	for i, k := range rctx.URLParams.Keys {
		if k == "*" || i >= len(rctx.URLParams.Values) {
			continue // the mount's wildcard is the rest of the path, which the pattern already says
		}
		b.WriteByte(':')
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(canonicalParam(rctx.URLParams.Values[i]))
	}
	return b.String()
}

// canonicalParam reduces a path parameter to the value the handler resolves it to, so that every
// spelling of one id shares one budget. A parameter that is not an integer gets a single bucket for
// the whole route rather than one per spelling: it names no row, and it must not be able to mint an
// unbounded number of buckets by varying.
func canonicalParam(v string) string {
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		return strconv.FormatInt(n, 10)
	}
	return "*"
}

// WithClass declares a route's policy class. A route without one is denied by
// Decide (fail closed), so this is not optional decoration — it is the route's
// registration with the policy layer.
func WithClass(c policy.RouteClass) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxClass, c)))
		})
	}
}

// problem writes an RFC 7807 problem document, which is the error envelope Huma
// emits — so a policy denial and a validation failure look the same on the wire.
//
// The policy gate itself lives in Server.policyGate (router.go), as a Huma middleware: only Huma
// knows the operation, and the operation is what carries the RouteClass. A second,
// chi-level gate here would be a second place a route could be denied — and two
// gates that must agree are a gate that eventually does not.
func problem(w http.ResponseWriter, status int, kind, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	//nolint:errcheck // the status line is already on the wire: a failed body write
	// means the client hung up, and there is no second response to send them.
	json.NewEncoder(w).Encode(map[string]any{
		"type":   "urn:flagfish:error:" + kind,
		"title":  http.StatusText(status),
		"status": status,
		"detail": detail,
	})
}
