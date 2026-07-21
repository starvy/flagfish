// Package egress guards the outbound HTTP requests the server makes to an
// operator-supplied destination.
//
// There is exactly one such destination today — the announcement webhook — and its URL is
// settable over the admin API. Without a guard, a stolen admin session is a request
// forger living inside the deployment's network: the cloud metadata endpoint, the
// Postgres box, and every other service that trusts its own subnet are one PATCH away,
// and the reply never has to come back for the damage to be done.
package egress

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"
)

var (
	// ErrBlocked is the refusal of a connection to an address the policy does not permit.
	ErrBlocked = errors.New("egress: destination address is not permitted")
	// ErrRedirect is the refusal of a redirect hop.
	ErrRedirect = errors.New("egress: refusing to follow a redirect")
)

// Policy decides which resolved addresses an outbound request may connect to. Its zero
// value permits public addresses only, which is the policy every deployment gets unless
// it says otherwise.
type Policy struct {
	// Allowed exempts specific networks from the block, and nothing else. It is empty by
	// default and is deployment-level configuration on purpose: an operator who really
	// does post to an internal receiver names that receiver's network, and naming
	// 10.0.5.0/24 still leaves the metadata address and the rest of the estate refused.
	Allowed []netip.Prefix
}

// Client builds the http.Client for a destination the operator chose. Every connection it
// opens is checked against p, and it does not follow redirects.
func (p Policy) Client(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: p.transport(),
		// A webhook receiver answers on the URL it was given; Discord and the
		// Discord-compatible receivers never redirect. Following a hop would hand the
		// choice of destination to whoever controls the first one, so the destination
		// stays the one the operator configured. The dial guard would catch a hop into a
		// blocked network anyway — this keeps a hop from being interesting at all.
		CheckRedirect: func(req *http.Request, _ []*http.Request) error {
			return fmt.Errorf("%w: %s", ErrRedirect, req.URL.Redacted())
		},
	}
}

func (p Policy) transport() *http.Transport {
	return &http.Transport{
		DialContext: p.dialer().DialContext,
		// No proxy, including none from the environment. A proxy would move the dial to
		// the proxy's address and carry the real destination in the request line, so the
		// check below would be inspecting the wrong host and permitting everything.
		Proxy:                 nil,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          10,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
}

func (p Policy) dialer() *net.Dialer {
	return &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
		// The check belongs here, on the address this socket is about to connect to, and
		// not on the URL's hostname. A hostname is not a destination: DNS is free to
		// answer a perfectly public name with 169.254.169.254, and free to answer
		// differently the second time, so anything decided before resolution is a decision
		// about a different connection than the one that gets made. Control runs after
		// resolution and before connect, on this attempt's own address.
		Control: func(network, address string, _ syscall.RawConn) error {
			return p.checkDial(network, address)
		},
	}
}

func (p Policy) checkDial(network, address string) error {
	switch network {
	case "tcp", "tcp4", "tcp6":
	default:
		return fmt.Errorf("%w: network %q", ErrBlocked, network)
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("egress: parse dial address %q: %w", address, err)
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		// Control is handed a resolved literal. Anything else means we cannot tell what we
		// are about to talk to, and not knowing is a refusal, not a pass.
		return fmt.Errorf("%w: unresolved dial address %q", ErrBlocked, address)
	}
	return p.CheckAddr(ip)
}

// CheckAddr reports whether ip may be connected to, naming the reason when it may not.
func (p Policy) CheckAddr(ip netip.Addr) error {
	// An IPv4-mapped v6 address is the same host as its v4 form, so ::ffff:169.254.169.254
	// has to be judged as 169.254.169.254 rather than as an unremarkable v6 address.
	ip = ip.Unmap()

	for _, allowed := range p.Allowed {
		if allowed.Contains(ip) {
			return nil
		}
	}
	if why := blockReason(ip); why != "" {
		return fmt.Errorf("%w: %s is %s", ErrBlocked, ip, why)
	}
	return nil
}

// blockReason names why ip is not a public destination, or returns "" if it is.
func blockReason(ip netip.Addr) string {
	switch {
	case !ip.IsValid():
		return "not a valid address"
	case ip.IsUnspecified():
		return "the unspecified address"
	case ip.IsLoopback():
		return "loopback"
	case ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast():
		// 169.254.0.0/16 and fe80::/10 — where every cloud's metadata endpoint lives.
		return "link-local"
	case ip.IsInterfaceLocalMulticast(), ip.IsMulticast():
		return "multicast"
	case ip.IsPrivate():
		// RFC 1918 and the fc00::/7 unique-local range.
		return "a private address"
	case !ip.IsGlobalUnicast():
		return "not a global unicast address"
	}
	for _, r := range reserved {
		if r.prefix.Contains(ip) {
			return r.why
		}
	}
	return ""
}

// reserved covers the ranges that look routable to netip's predicates but are not a public
// receiver, each of which is a documented way back into the local network.
var reserved = []struct {
	prefix netip.Prefix
	why    string
}{
	{netip.MustParsePrefix("100.64.0.0/10"), "carrier-grade NAT space"},
	{netip.MustParsePrefix("192.0.0.0/24"), "IETF protocol assignment space"},
	{netip.MustParsePrefix("198.18.0.0/15"), "benchmarking space"},
	// Both encode an IPv4 destination that a gateway unwraps, so a literal in either range
	// reaches whatever v4 address it carries — including the ones refused above.
	{netip.MustParsePrefix("64:ff9b::/96"), "a NAT64 translation prefix"},
	{netip.MustParsePrefix("2002::/16"), "a 6to4 translation prefix"},
}
