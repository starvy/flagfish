package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

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
func realIP(trusted []*net.IPNet) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Peer trust is read before clientIP rewrites RemoteAddr — afterwards the socket
			// peer is gone, and X-Forwarded-Proto must be gated on the same list as X-Forwarded-For.
			trustedPeer := peerTrusted(r, trusted)
			r.RemoteAddr = clientIP(r, trusted)
			ctx := r.Context()
			if addr, err := netip.ParseAddr(r.RemoteAddr); err == nil {
				ctx = context.WithValue(ctx, ctxClientIP, addr)
			}
			ctx = context.WithValue(ctx, ctxSecure, isSecure(r, trustedPeer))
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

// isSecure reports whether the client reached us over TLS, so a session cookie can be marked Secure on
// a real deployment and left open on plain-HTTP localhost. X-Forwarded-Proto is believed only from a
// trusted proxy — an untrusted client cannot flip the cookie's Secure attribute by setting a header.
func isSecure(r *http.Request, trustedPeer bool) bool {
	if r.TLS != nil {
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

func isTrusted(ip net.IP, trusted []*net.IPNet) bool {
	for _, n := range trusted {
		if n.Contains(ip) {
			return true
		}
	}
	return false
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
func authenticate(a Authenticator, log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			auth, err := a.Authenticate(r.Context(), r)
			if err != nil {
				log.WarnContext(r.Context(), "authentication failed", "error", err)
				problem(w, http.StatusUnauthorized, "invalid-credentials", err.Error())
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
// per-account, which is what you want when a single team is behind one NAT.
func rateLimit(l Limiter, log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			pr := AuthOf(r.Context()).Principal

			key := "ip:" + r.RemoteAddr
			if pr.Authed {
				key = fmt.Sprintf("account:%d", pr.AccountID)
			}

			ok, err := l.Allow(r.Context(), key+":"+r.Method+":"+r.URL.Path)
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
