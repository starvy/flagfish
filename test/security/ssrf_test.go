//go:build integration

package security

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"

	"github.com/starvy/flagfish/internal/config"
	"github.com/starvy/flagfish/internal/egress"
	"github.com/starvy/flagfish/internal/jobs"
)

// The webhook URL is admin-settable, and the worker POSTs to whatever it says. That makes a
// stolen admin session a request forger standing inside the deployment's network unless the
// poster refuses to dial there — the reply never has to come back for the metadata endpoint or
// an internal admin panel to have been reached.
//
// Every case here fails if the egress guard is removed from the poster.

// loopbackExempt is the escape hatch a deployment with a genuine internal receiver would set,
// spelled the way these tests need it: the httptest receivers live on 127.0.0.1.
func loopbackExempt() egress.Policy {
	return egress.Policy{Allowed: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}}
}

// hostPort rewrites a URL's host, keeping its port. It is how a test points a name at the port an
// httptest server is really listening on.
func hostPort(t *testing.T, raw, host string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	u.Host = host + ":" + u.Port()
	return u.String()
}

// S30 — the shipped poster refuses to dial anything but a public address.
//
// The literal cases are the obvious half. The one that matters is "loopback by name": nothing in
// the URL says 127.0.0.1, and the refusal still lands, because the decision is taken on the
// address the socket is about to connect to and not on the hostname it came from.
func TestS45_WebhookRefusesInternalDestinations(t *testing.T) {
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer receiver.Close()

	tests := []struct {
		name string
		url  string
	}{
		{"loopback literal", receiver.URL},
		{"loopback by name", hostPort(t, receiver.URL, "localhost")},
		{"cloud metadata", "http://169.254.169.254/latest/meta-data/iam/security-credentials/"},
		{"cloud metadata, IPv4-mapped", "http://[::ffff:169.254.169.254]/latest/meta-data/"},
		{"rfc1918 ten", "http://10.0.0.1/hook"},
		{"rfc1918 one-seven-two", "http://172.16.0.1/hook"},
		{"rfc1918 one-nine-two", "http://192.168.1.1/hook"},
		{"unique-local v6", "http://[fd00::1]/hook"},
		{"unspecified", "http://0.0.0.0/hook"},
		{"carrier-grade nat", "http://100.64.0.1/hook"},
		{"loopback v6", "http://[::1]/hook"},
	}

	// The production constructor, built from an environment that names no exemption — the poster
	// a deployment gets when it configures nothing.
	poster := jobs.NewHTTPPoster(&config.Env{})

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := poster.Post(context.Background(), tc.url, []byte(`{"content":"x"}`))
			if !errors.Is(err, egress.ErrBlocked) {
				t.Fatalf("posting to %s: %v, want egress.ErrBlocked", tc.url, err)
			}
		})
	}
}

// S31 — a redirect toward an internal address is refused.
//
// A validated destination that answers 302 is a destination that gets to choose the next one, so
// the poster does not follow any of them. The receiver here is on loopback and explicitly
// exempted, so the first hop is permitted and the refusal can only be about the redirect.
func TestS46_WebhookRefusesRedirects(t *testing.T) {
	tests := []struct {
		name string
		to   string
	}{
		{"toward cloud metadata", "http://169.254.169.254/latest/meta-data/"},
		{"toward rfc1918", "http://10.0.0.1/hook"},
		{"toward loopback", "http://127.0.0.1:1/hook"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var hits int
			redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits++
				http.Redirect(w, r, tc.to, http.StatusFound)
			}))
			defer redirector.Close()

			poster := jobs.NewHTTPPosterWithPolicy(loopbackExempt())
			err := poster.Post(context.Background(), redirector.URL, []byte(`{"content":"x"}`))
			if !errors.Is(err, egress.ErrRedirect) {
				t.Fatalf("posting to a redirector: %v, want egress.ErrRedirect", err)
			}
			if hits != 1 {
				t.Errorf("redirector was hit %d times, want exactly 1", hits)
			}
		})
	}
}

// S32 — the block is a block, not an outage: a destination the deployment named still receives.
//
// Without this the suite would pass just as well against a poster that refused everything, and
// the escape hatch a self-hoster with an internal Mattermost needs would be untested.
func TestS47_NamedExemptionStillDelivers(t *testing.T) {
	delivered := make(chan []byte, 1)
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		delivered <- body
		w.WriteHeader(http.StatusNoContent)
	}))
	defer receiver.Close()

	poster := jobs.NewHTTPPosterWithPolicy(loopbackExempt())
	if err := poster.Post(context.Background(), receiver.URL, []byte(`{"content":"hello"}`)); err != nil {
		t.Fatalf("post to an exempted receiver: %v", err)
	}
	if got := string(<-delivered); got != `{"content":"hello"}` {
		t.Errorf("receiver got %q", got)
	}
}

// S33 — the policy permits public addresses, and naming one internal network exempts only it.
//
// Asserted on the policy directly so the boundary between "public" and "internal" is pinned
// without a socket, including the ranges no deployment ever reaches on purpose.
func TestS48_EgressPolicyClassification(t *testing.T) {
	tests := []struct {
		name    string
		addr    string
		policy  egress.Policy
		blocked bool
	}{
		{"public v4", "1.1.1.1", egress.Policy{}, false},
		{"public v4, other", "93.184.216.34", egress.Policy{}, false},
		{"public v6", "2606:4700:4700::1111", egress.Policy{}, false},
		{"loopback", "127.0.0.1", egress.Policy{}, true},
		{"link-local", "169.254.169.254", egress.Policy{}, true},
		{"link-local v6", "fe80::1", egress.Policy{}, true},
		{"private", "10.1.2.3", egress.Policy{}, true},
		{"unique-local", "fd12::1", egress.Policy{}, true},
		{"multicast", "224.0.0.1", egress.Policy{}, true},
		{"benchmarking", "198.18.0.1", egress.Policy{}, true},
		{"nat64 wrapping metadata", "64:ff9b::a9fe:a9fe", egress.Policy{}, true},

		// An exemption covers what it names and nothing adjacent: the receiver is reachable,
		// the metadata endpoint on the same host is not.
		{"exempted internal", "10.0.5.7", policyFor("10.0.5.0/24"), false},
		{"outside the exemption", "10.0.6.7", policyFor("10.0.5.0/24"), true},
		{"metadata despite an exemption", "169.254.169.254", policyFor("10.0.5.0/24"), true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			addr, err := netip.ParseAddr(tc.addr)
			if err != nil {
				t.Fatalf("parse %q: %v", tc.addr, err)
			}
			err = tc.policy.CheckAddr(addr)
			if tc.blocked && !errors.Is(err, egress.ErrBlocked) {
				t.Fatalf("CheckAddr(%s) = %v, want egress.ErrBlocked", tc.addr, err)
			}
			if !tc.blocked && err != nil {
				t.Fatalf("CheckAddr(%s) = %v, want permitted", tc.addr, err)
			}
		})
	}
}

func policyFor(cidrs ...string) egress.Policy {
	var p egress.Policy
	for _, c := range cidrs {
		p.Allowed = append(p.Allowed, netip.MustParsePrefix(c))
	}
	return p
}

// S34 — an operator who mistypes the exemption is told, rather than quietly getting no exemption.
func TestS49_MalformedExemptionIsLoud(t *testing.T) {
	t.Setenv("FLAGFISH_DATABASE_URL", "postgres://u:p@localhost:5432/db")
	t.Setenv("FLAGFISH_WEBHOOK_ALLOWED_NETWORKS", "10.0.5.0/24,not-a-network")

	_, err := config.LoadEnv()
	if err == nil {
		t.Fatal("a malformed exemption loaded without complaint")
	}
	if got := err.Error(); !strings.Contains(got, "FLAGFISH_WEBHOOK_ALLOWED_NETWORKS") {
		t.Errorf("error does not name the variable: %s", got)
	}
}
