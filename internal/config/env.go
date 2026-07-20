package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Env is the process-level configuration: the settings that must exist before the
// database does, and therefore cannot live in the config table.
//
// Everything else — event name, freeze time, visibility — is instance config and is
// changed at runtime by an admin, not by a redeploy.
type Env struct {
	DatabaseURL    string
	Addr           string
	LogLevel       slog.Level
	LogFormat      string // text | json
	TrustedProxies []netip.Prefix

	// SecureCookies marks the session cookie Secure. It defaults to true and is deliberately
	// NOT derived from the trusted-proxy list: the documented topology is a TLS proxy in front
	// of a plaintext app, so a deployment that never configures proxy trust would otherwise
	// hand out session cookies a browser will happily send over plain HTTP. Turn it off only
	// to serve the app over http:// yourself, which means a development laptop.
	SecureCookies bool

	// MaxUploadBytes caps a multipart upload. Every other request body gets a much smaller,
	// fixed cap — an upload is the one route that legitimately carries megabytes.
	MaxUploadBytes int64

	// DBMaxConns and DBMinConns size the pgx connection pool. They are the first-class knob
	// for what was otherwise only reachable by appending ?pool_max_conns= to the DSN. The
	// default max is sized for the CTF-start thundering herd — every team hitting the board and
	// its first submits inside one minute — rather than pgx's NumCPU-shaped default, which
	// queues under exactly that load. MinConns keeps a few connections warm so the first wave
	// does not pay connection setup. Size the max to fit the database's own max_connections
	// across every replica that shares it.
	DBMaxConns int32
	DBMinConns int32

	// RateLimit is the number of requests one caller may make per RateWindow before the
	// limiter starts denying. It is process-level and not runtime config on purpose: a
	// brute-force guard an admin can turn down from the UI is a brute-force guard an
	// attacker turns down first.
	RateLimit  int
	RateWindow time.Duration

	// AuthRateLimit is the tighter per-caller budget on the credential routes (login, register,
	// password reset) within the same RateWindow. It is deliberately far below RateLimit: those
	// routes are where brute force lives, and the general budget is too loose to blunt it. Like
	// RateLimit it is process-level, not runtime config an attacker could turn down first.
	AuthRateLimit int
}

// DefaultMaxUploadBytes bounds a multipart upload when none is configured. The multipart
// parser spills past its memory budget to a temp file, so this bounds disk more than heap —
// but unbounded is unbounded either way, and the container it ships in has 512 MB.
const DefaultMaxUploadBytes int64 = 32 << 20

// Pool defaults. The max is deliberately above pgx's NumCPU-shaped default so the CTF-start
// burst has connections to hand out instead of queuing; the min keeps a handful warm.
const (
	DefaultDBMaxConns int32 = 25
	DefaultDBMinConns int32 = 2
)

// EnvError lists everything wrong with the environment at once. Reporting the first
// failure only means a misconfigured deploy takes four restarts to diagnose.
type EnvError struct{ Problems []string }

func (e *EnvError) Error() string {
	return fmt.Sprintf("invalid environment:\n  - %s", strings.Join(e.Problems, "\n  - "))
}

// LoadEnv reads and validates the environment. It never falls back to a default for
// anything whose wrong value would be silently harmful.
func LoadEnv() (Env, error) {
	var (
		env  Env
		errs []string
	)

	// FLAGFISH_DATABASE_URL is canonical; DATABASE_URL is what compose, CI and most
	// hosting platforms set, so it is accepted too.
	env.DatabaseURL = firstSet("FLAGFISH_DATABASE_URL", "DATABASE_URL")
	if env.DatabaseURL == "" {
		errs = append(errs, "FLAGFISH_DATABASE_URL (or DATABASE_URL) is not set")
	} else if err := validateDSN(env.DatabaseURL); err != nil {
		errs = append(errs, err.Error())
	}

	env.Addr = envOr("FLAGFISH_ADDR", ":8000")

	switch lvl := strings.ToLower(envOr("FLAGFISH_LOG_LEVEL", "info")); lvl {
	case "debug":
		env.LogLevel = slog.LevelDebug
	case "info":
		env.LogLevel = slog.LevelInfo
	case "warn", "warning":
		env.LogLevel = slog.LevelWarn
	case "error":
		env.LogLevel = slog.LevelError
	default:
		errs = append(errs, fmt.Sprintf("FLAGFISH_LOG_LEVEL=%q (want debug|info|warn|error)", lvl))
	}

	switch f := strings.ToLower(envOr("FLAGFISH_LOG_FORMAT", "json")); f {
	case "text", "json":
		env.LogFormat = f
	default:
		errs = append(errs, fmt.Sprintf("FLAGFISH_LOG_FORMAT=%q (want text|json)", f))
	}

	// Default on. An operator who forgets this variable entirely must still get Secure
	// cookies, because the deployment that forgets it is the one behind a TLS proxy.
	env.SecureCookies = true
	if raw := firstSet("FLAGFISH_SECURE_COOKIES"); raw != "" {
		b, err := strconv.ParseBool(raw)
		if err != nil {
			errs = append(errs, fmt.Sprintf("FLAGFISH_SECURE_COOKIES=%q is not a boolean (true|false)", raw))
		} else {
			env.SecureCookies = b
		}
	}

	env.MaxUploadBytes = DefaultMaxUploadBytes
	if raw := firstSet("FLAGFISH_MAX_UPLOAD_BYTES"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		switch {
		case err != nil:
			errs = append(errs, fmt.Sprintf("FLAGFISH_MAX_UPLOAD_BYTES=%q is not an integer", raw))
		case n <= 0:
			errs = append(errs, fmt.Sprintf("FLAGFISH_MAX_UPLOAD_BYTES=%d must be positive", n))
		default:
			env.MaxUploadBytes = n
		}
	}

	env.DBMaxConns = DefaultDBMaxConns
	if raw := firstSet("FLAGFISH_DB_MAX_CONNS"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 32)
		switch {
		case err != nil:
			errs = append(errs, fmt.Sprintf("FLAGFISH_DB_MAX_CONNS=%q is not an integer", raw))
		case n < 1:
			errs = append(errs, fmt.Sprintf("FLAGFISH_DB_MAX_CONNS=%d must be at least 1", n))
		default:
			env.DBMaxConns = int32(n)
		}
	}

	env.DBMinConns = DefaultDBMinConns
	if raw := firstSet("FLAGFISH_DB_MIN_CONNS"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 32)
		switch {
		case err != nil:
			errs = append(errs, fmt.Sprintf("FLAGFISH_DB_MIN_CONNS=%q is not an integer", raw))
		case n < 0:
			errs = append(errs, fmt.Sprintf("FLAGFISH_DB_MIN_CONNS=%d must not be negative", n))
		default:
			env.DBMinConns = int32(n)
		}
	}
	// A min above max never opens a connection and would fail deep in the pool; reject it here,
	// loudly, next to the values that caused it.
	if env.DBMinConns > env.DBMaxConns {
		errs = append(errs, fmt.Sprintf("FLAGFISH_DB_MIN_CONNS=%d exceeds FLAGFISH_DB_MAX_CONNS=%d",
			env.DBMinConns, env.DBMaxConns))
	}

	env.RateLimit = 60
	if raw := firstSet("FLAGFISH_RATE_LIMIT"); raw != "" {
		n, err := strconv.Atoi(raw)
		switch {
		case err != nil:
			errs = append(errs, fmt.Sprintf("FLAGFISH_RATE_LIMIT=%q is not an integer", raw))
		case n <= 0:
			// Zero would mean "deny everything", which is never what an operator means by
			// setting a rate limit; it is how you fat-finger the whole site offline.
			errs = append(errs, fmt.Sprintf("FLAGFISH_RATE_LIMIT=%d must be positive", n))
		default:
			env.RateLimit = n
		}
	}

	env.AuthRateLimit = 10
	if raw := firstSet("FLAGFISH_AUTH_RATE_LIMIT"); raw != "" {
		n, err := strconv.Atoi(raw)
		switch {
		case err != nil:
			errs = append(errs, fmt.Sprintf("FLAGFISH_AUTH_RATE_LIMIT=%q is not an integer", raw))
		case n <= 0:
			errs = append(errs, fmt.Sprintf("FLAGFISH_AUTH_RATE_LIMIT=%d must be positive", n))
		default:
			env.AuthRateLimit = n
		}
	}

	env.RateWindow = time.Minute
	if raw := firstSet("FLAGFISH_RATE_WINDOW"); raw != "" {
		d, err := time.ParseDuration(raw)
		switch {
		case err != nil:
			errs = append(errs, fmt.Sprintf("FLAGFISH_RATE_WINDOW=%q is not a duration (e.g. 1m, 30s)", raw))
		case d <= 0:
			errs = append(errs, fmt.Sprintf("FLAGFISH_RATE_WINDOW=%q must be positive", raw))
		default:
			env.RateWindow = d
		}
	}

	// A malformed CIDR here is fatal rather than skipped. Silently dropping one would
	// leave the proxy untrusted, so every request would be attributed to the proxy's own
	// address — which poisons the anti-cheat data rather than breaking anything visibly.
	for _, raw := range splitList(os.Getenv("FLAGFISH_TRUSTED_PROXIES")) {
		p, err := netip.ParsePrefix(raw)
		if err != nil {
			// Accept a bare address as a single-host prefix; it is the obvious thing to write.
			addr, aerr := netip.ParseAddr(raw)
			if aerr != nil {
				errs = append(errs, fmt.Sprintf("FLAGFISH_TRUSTED_PROXIES: %q is not a CIDR or IP", raw))
				continue
			}
			p = netip.PrefixFrom(addr, addr.BitLen())
		}
		env.TrustedProxies = append(env.TrustedProxies, p)
	}

	if len(errs) > 0 {
		return Env{}, &EnvError{Problems: errs}
	}
	return env, nil
}

// Logger builds the process logger from the environment.
func (e Env) Logger() *slog.Logger {
	opts := &slog.HandlerOptions{Level: e.LogLevel}
	if e.LogFormat == "text" {
		return slog.New(slog.NewTextHandler(os.Stderr, opts))
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, opts))
}

// RedactedDatabaseURL is the DSN with the password removed, safe to log.
func (e Env) RedactedDatabaseURL() string { return redactDSN(e.DatabaseURL) }

// TrustedProxiesCSV renders the parsed prefixes back to the comma-separated form the
// serve flag takes, so the env var and the flag are the same knob.
func (e Env) TrustedProxiesCSV() string {
	parts := make([]string, len(e.TrustedProxies))
	for i, p := range e.TrustedProxies {
		parts[i] = p.String()
	}
	return strings.Join(parts, ",")
}

func validateDSN(dsn string) error {
	u, err := url.Parse(dsn)
	if err != nil {
		return fmt.Errorf("FLAGFISH_DATABASE_URL is not a valid URL: %w", err)
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		return fmt.Errorf("FLAGFISH_DATABASE_URL scheme is %q, want postgres:// (Postgres 17 only)", u.Scheme)
	}
	if u.Host == "" {
		return errors.New("FLAGFISH_DATABASE_URL has no host")
	}
	if strings.TrimPrefix(u.Path, "/") == "" {
		return errors.New("FLAGFISH_DATABASE_URL has no database name")
	}
	return nil
}

func redactDSN(dsn string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		return "postgres://<unparseable>"
	}
	if u.User != nil {
		if _, hasPassword := u.User.Password(); hasPassword {
			u.User = url.UserPassword(u.User.Username(), "xxxxx")
		}
	}
	return u.String()
}

func firstSet(keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}
