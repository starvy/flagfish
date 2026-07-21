package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

			// A caller who resolved to anonymous while still carrying a session cookie is holding a
			// dead one — invalid cookies degrade to anonymous, a valid one resolves to MethodCookie.
			// Expire it so the browser stops re-presenting a cookie every request that can never
			// authenticate. It is written before the handler runs, so a successful re-login on this
			// same request still overwrites it with the fresh session cookie it sets afterwards.
			if auth.Method == MethodAnonymous {
				if c, cerr := r.Cookie(accounts.SessionCookie); cerr == nil && c.Value != "" {
					http.SetCookie(w, accounts.ClearSessionCookie(secureOf(r.Context())))
				}
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

// limiters are the budgets the chain enforces. The general one covers every route; the other three
// replace it on the credential routes, where per-source counting is the wrong control (see
// credentialLimit). All four are nil-tolerant: a credential route falls back to the general limiter
// when the credential set is not wired, so a partial wiring is tighter, never looser.
type limiters struct {
	general Limiter

	// target, failure and flood are the credential budgets, in the order they are spent.
	target  Limiter // failed attempts against one account or token
	failure Limiter // failed attempts from one source address
	flood   Limiter // all credential attempts from one source address
}

func (l limiters) credentialsWired() bool {
	return l.target != nil && l.failure != nil && l.flood != nil
}

// rateLimit keys on the caller (see limiterKey) — so an anonymous flood is limited per-source and
// an authenticated one per-account, which is what you want when a single team is behind one NAT.
// The other half of the key is the route (see bucketRoute), never the raw path.
//
// The IP is the one realIP resolved, which is the client's only if a proxy in front of us is
// trusted. Untrusted, every request behind that proxy keys on the proxy's own address and the
// anonymous bucket becomes global — one stranger can then rate-limit /login for everyone. That
// misconfiguration is warned about loudly at boot and on the first forwarded request.
//
// The credential routes are the exception and are handed to credentialLimit instead: an anonymous
// per-source budget is exactly what they must not have.
func rateLimit(routes chi.Routes, l limiters, log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rctx := chi.NewRouteContext()
			matched := routes.Match(rctx, r.Method, routedPath(r))

			if matched && l.credentialsWired() {
				if cred, ok := credentialRoutes[r.Method+" "+rctx.RoutePattern()]; ok {
					credentialLimit(w, r, next, l, cred, r.Method+" "+rctx.RoutePattern(), log)
					return
				}
			}

			key := limiterKey(r, AuthOf(r.Context()).Principal)

			ok, err := l.general.Allow(r.Context(), key+":"+bucketRoute(matched, rctx, r))
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

// limiterKey names the budget one caller spends from.
//
// An account is the right unit when there is one — teammates sharing a budget is deliberate. But a
// teamless player in teams mode has no account, and keying them on it puts every one of them in the
// same bucket: at kickoff the entire field is teamless at once and they rate-limit each other out of
// the join/create-team calls that would give them an account. So they key on the user instead, which
// an authenticated principal always has.
//
// The three namespaces are disjoint by prefix and must stay that way. A user id and an account id
// are separate sequences — in teams mode an account id is a team id — so the same integer routinely
// names both, and an unprefixed key would let one player spend another's budget.
func limiterKey(r *http.Request, pr policy.Principal) string {
	switch {
	case pr.Authed && pr.AccountID != 0:
		return "account:" + strconv.FormatInt(int64(pr.AccountID), 10)
	case pr.Authed && pr.UserID != 0:
		return "user:" + strconv.FormatInt(pr.UserID, 10)
	default:
		return "ip:" + clientKey(r)
	}
}

// credentialRoute says how one brute-force-sensitive route is limited.
type credentialRoute struct {
	// field is the body field naming what the request is aimed at: an account ("email") or a
	// secret ("token").
	field string

	// refundable says the response distinguishes a legitimate attempt from a failed one, so the
	// two failure budgets can be spent up front and handed back on success. A route that answers
	// the same thing either way must not be refundable — the refund would make it unmetered.
	refundable bool
}

// credentialRoutes are the brute-force-sensitive routes: password guessing (login), account spray
// and address probing (register), reset-request flooding, and reset- or verification-token
// guessing. Keyed by "METHOD "+the matched route pattern — the canonical route, never the raw path.
//
// POST /reset-password is the one non-refundable entry, and deliberately: it answers 200 to an
// unknown address on purpose, so there is no failure to detect, and refunding every 200 would leave
// nothing between a stranger and an unbounded run of reset emails to addresses they are guessing at.
var credentialRoutes = map[string]credentialRoute{
	http.MethodPost + " /api/v1/login":           {field: "email", refundable: true},
	http.MethodPost + " /api/v1/register":        {field: "email", refundable: true},
	http.MethodPost + " /api/v1/reset-password":  {field: "email"},
	http.MethodPatch + " /api/v1/reset-password": {field: "token", refundable: true},
	http.MethodPost + " /api/v1/verify/confirm":  {field: "token", refundable: true},
}

// credentialLimit enforces the credential budgets, which are keyed on three different axes because
// no single one of them is a brute-force control.
//
// A per-source counter is the intuitive choice and the wrong one twice over. Teams share one public
// address — a university NAT, a venue's wifi — so a budget tight enough to blunt guessing locks out
// a whole room at the start of an event, and an attacker who wants more than that budget rents a
// second address. So the strict budget is keyed on what is actually under attack: the account named
// by the submitted email, or the submitted token. Guessing one account is then throttled no matter
// how many addresses the guesses arrive from, and a hundred distinct players behind one address
// never contend at all.
//
// The source address keeps two budgets anyway. A strict one on failures, which is the only thing
// that sees a spray of one guess each against a thousand accounts, and which a room full of players
// typing their own passwords correctly never touches because a success is refunded. And a generous
// one on everything, because a password verification is deliberately expensive and the number of
// times a stranger may make us perform one has to be bounded regardless.
//
// Every denial is the same 429 with the same body whatever the key was, and the key is a hash of
// the submitted value rather than a lookup: nothing here can tell a caller whether an account
// exists.
func credentialLimit(
	w http.ResponseWriter, r *http.Request, next http.Handler,
	l limiters, cred credentialRoute, route string, log *slog.Logger,
) {
	body, err := readAndRestoreBody(r)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			problem(w, http.StatusRequestEntityTooLarge, "body-too-large", "request body is too large")
			return
		}
		log.WarnContext(r.Context(), "credential request body could not be read", "error", err)
		problem(w, http.StatusBadRequest, "bad-request", "could not read the request body")
		return
	}

	ip := clientKey(r)
	// A request that names no target keys on its source instead, or omitting the field would be
	// the bypass: one bucket per attacker rather than one shared by everybody who forgot it.
	targetKey := "auth:target:ip:" + ip + ":" + route
	if t, ok := credentialTarget(body, cred.field); ok {
		targetKey = "auth:target:" + cred.field + ":" + t + ":" + route
	}

	spend := func(ctx context.Context, lim Limiter, key string) bool {
		ok, aerr := lim.Allow(ctx, key)
		switch {
		case aerr != nil:
			// A limiter that cannot answer must not become an open door.
			log.ErrorContext(ctx, "credential rate limiter failed", "error", aerr)
			problem(w, http.StatusServiceUnavailable, "unavailable", "rate limiter unavailable")
		case !ok:
			log.WarnContext(ctx, "credential request rate-limited", "route", route, "ip", ip)
			problem(w, http.StatusTooManyRequests, "rate-limited", "too many requests")
		}
		return ok && aerr == nil
	}

	ctx := r.Context()
	floodKey, failureKey := "auth:flood:ip:"+ip, "auth:fail:ip:"+ip
	if !spend(ctx, l.flood, floodKey) {
		return
	}
	if !spend(ctx, l.failure, failureKey) {
		return
	}
	if !spend(ctx, l.target, targetKey) {
		return
	}

	sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
	next.ServeHTTP(sw, r)

	// The two failure budgets were spent before the outcome was known. A handler that did not
	// reject the caller proves the attempt was legitimate, so give them back — this is what keeps
	// a shared address free for players who log in successfully.
	if !cred.refundable || sw.status >= http.StatusBadRequest {
		return
	}
	for _, ref := range []struct {
		l   Limiter
		key string
	}{{l.failure, failureKey}, {l.target, targetKey}} {
		if rerr := ref.l.Refund(ctx, ref.key); rerr != nil {
			// The response is already written and the caller was let through. A lost refund only
			// costs them one unit of budget, so it is logged, not escalated.
			log.ErrorContext(ctx, "credential rate limit refund failed", "error", rerr, "route", route)
		}
	}
}

// maxCredentialBodyBytes bounds what credentialLimit will buffer to find the target. The outer
// limitBody cap is sized for uploads; a login body that needs more than this is not a login.
const maxCredentialBodyBytes int64 = 64 << 10

// readAndRestoreBody drains the body so the limiter can read the submitted email or token, then
// puts it back for the handler that has not run yet.
func readAndRestoreBody(r *http.Request) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxCredentialBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("read credential body: %w", err)
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	return body, nil
}

// credentialTarget hashes what the request is aimed at, so the strict budget can be keyed on it.
//
// The hash is not obfuscation — an operator reading the database can already read every address in
// the users table. It is there so the rate-limit key is fixed-width and so one spelling of an
// address cannot mint a fresh budget: the fold is lower-case, matching the fold the account lookup
// and the uniqueness constraint use. A token is taken verbatim, being case-sensitive.
func credentialTarget(body []byte, field string) (string, bool) {
	var probe struct {
		Email string `json:"email"`
		Token string `json:"token"`
	}
	if json.Unmarshal(body, &probe) != nil {
		return "", false
	}

	value := probe.Token
	if field == "email" {
		value = strings.ToLower(strings.TrimSpace(probe.Email))
	}
	if value == "" {
		return "", false
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:16]), true
}

// routedPath is the path chi itself routes on: RawPath when the path carried escapes, Path
// otherwise. Matching on anything else means bucketing a request against a route it did not take.
func routedPath(r *http.Request) string {
	if r.URL.RawPath != "" {
		return r.URL.RawPath
	}
	return r.URL.Path
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
// group, where chi has routed no further than the /api/v1/* mount. So the caller resolves the route
// itself, which is the same radix lookup chi is about to do anyway, and hands the result in.
//
// A path that matches no route shares one bucket per caller. Otherwise every request to a made-up
// path mints a rate_limits row that nothing will ever read again.
func bucketRoute(matched bool, rctx *chi.Context, r *http.Request) string {
	if !matched {
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
