//go:build integration

package security

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The credential budgets are keyed on what is being attacked, not on where it comes from.
//
// A per-source counter cannot be both: tight enough to blunt guessing, and loose enough for a
// university NAT or a venue's wifi, where a whole room is one source. So the strict budget is keyed
// on the target — the account named by the submitted email, or the submitted token — and the source
// keeps a strict budget on FAILURES plus a generous one on everything. These four tests pin each
// half of that: guessing one account is throttled from anywhere, a shared address is not throttled
// at all, the counters fail closed, and none of it tells a caller who has an account.

// loginBody is a login attempt on the wire.
func loginBody(email, password string) []byte {
	return fmt.Appendf(nil, `{"email":%q,"password":%q}`, email, password)
}

// registerBody is a registration on the wire.
func registerBody(name, email, password string) []byte {
	return fmt.Appendf(nil, `{"name":%q,"email":%q,"password":%q}`, name, email, password)
}

// Guessing ONE account is throttled however many addresses the guesses arrive from.
//
// This is the property a per-source counter never had: an attacker who wants a bigger budget rents
// a second address, and a botnet has as many as it likes. Keyed on the target instead, the budget
// is spent by the account under attack, so the guesses cost the attacker the same wherever they
// originate — and an account nobody is attacking is unaffected.
func TestS26_CredentialGuessingIsThrottledPerTargetNotPerSource(t *testing.T) {
	const budget = 5
	wholeWindow(t, 20*time.Second)
	f := setup(t, withAuthLimit(budget), withTrustedProxy())
	f.user("victim", pw)
	f.user("bystander", pw)

	// Every guess comes from a different address, and every one of them counts.
	guess := func(email string, n int) int {
		return f.do(
			http.MethodPost, "/api/v1/login",
			withBody("application/json", loginBody(email, "wrong")),
			withForwardedFor(fmt.Sprintf("203.0.113.%d", n)),
		).StatusCode
	}

	for i := 1; i <= budget; i++ {
		if got := guess("victim@ctf.test", i); got == http.StatusTooManyRequests {
			t.Fatalf("guess %d from address %d was limited before the budget of %d was spent", i, i, budget)
		}
	}
	if got := guess("victim@ctf.test", budget+1); got != http.StatusTooManyRequests {
		t.Fatalf("guess %d: %d, want 429. It came from an address that has never been seen, so a "+
			"budget keyed on the source lets it through — and an attacker with %d addresses has "+
			"an unlimited number of guesses against this account", budget+1, got, budget+1)
	}

	// Case is folded, or varying it is the whole bypass: the account lookup folds too, so
	// VICTIM@ctf.test and victim@ctf.test are one account and must be one budget.
	if got := guess("VICTIM@ctf.test", 99); got != http.StatusTooManyRequests {
		t.Errorf("VICTIM@ctf.test: %d, want 429 — it is the same account whose budget is spent, "+
			"so spelling the address differently mints a fresh one", got)
	}

	// A body with no target at all keys on the source instead, or omitting the field is the bypass.
	spent := 0
	for i := 1; i <= budget+1; i++ {
		if f.do(
			http.MethodPost, "/api/v1/login",
			withBody("application/json", []byte(`{"password":"wrong"}`)),
			withForwardedFor("198.51.100.9"),
		).StatusCode == http.StatusTooManyRequests {
			spent++
		}
	}
	if spent == 0 {
		t.Error("a login body naming no account was never limited — omitting the email field is " +
			"an unlimited number of attempts")
	}

	// And the account nobody attacked still has its own full budget.
	if got := guess("bystander@ctf.test", 50); got == http.StatusTooManyRequests {
		t.Errorf("bystander@ctf.test: %d — a budget spent against one account must not deny "+
			"another. The key collapsed to something global", got)
	}
}

// A whole team behind ONE shared address registers and logs in normally.
//
// This is the regression test for the shipped default punishing exactly the users it is for.
// University and corporate NAT put a whole room behind one outbound address, and so does a venue's
// wifi; with the credential budget keyed on that address, a ten-person team spent it just signing
// in at the start of an event. Here every request comes from one address, and none of them is
// denied, because none of them fails: the per-source failure budget is refunded on success and the
// per-target budget is spent by a different account each time.
func TestS27_OneSharedAddressCarriesAWholeTeam(t *testing.T) {
	// The shipped defaults, not generous test values: the point is that the numbers an operator
	// actually gets are the ones that hold.
	const players = 15
	f := setup(
		t,
		withAuthLimit(10),        // FLAGFISH_AUTH_RATE_LIMIT
		withAuthFailureLimit(60), // FLAGFISH_AUTH_IP_FAILURE_LIMIT
		withAuthIPLimit(300),     // FLAGFISH_AUTH_IP_RATE_LIMIT
		withTrustedProxy(),
	)

	const nat = "203.0.113.77" // the team's one public address

	for i := range players {
		email := fmt.Sprintf("player%d@ctf.test", i)

		r := f.do(http.MethodPost, "/api/v1/register",
			withBody("application/json", registerBody(fmt.Sprintf("player%d", i), email, pw)),
			withForwardedFor(nat))
		if r.StatusCode != http.StatusOK {
			t.Fatalf("player %d could not register from the team's shared address: %d %s. "+
				"A credential budget keyed on the source address is spent by the teammates who "+
				"signed in first, which at the start of an event looks like an outage",
				i, r.StatusCode, r.Body)
		}

		r = f.do(http.MethodPost, "/api/v1/login",
			withBody("application/json", loginBody(email, pw)),
			withForwardedFor(nat))
		if r.StatusCode != http.StatusOK {
			t.Fatalf("player %d could not log in from the team's shared address: %d %s",
				i, r.StatusCode, r.Body)
		}
	}

	// The flood ceiling is still a ceiling: it counts every attempt, refunded or not, so it is what
	// stands between a stranger and an unbounded run of password verifications. Summed across
	// windows — this loop is slow enough to straddle one, and each window is its own row.
	if got := f.count(`SELECT COALESCE(sum(n), 0) FROM rate_limits WHERE bucket LIKE 'auth:flood:%'`); got < 2*players {
		t.Errorf("the per-source flood counter reached %d after %d credential requests — it is not "+
			"counting successes, so nothing bounds the work a single source can ask for", got, 2*players)
	}
}

// A credential budget that cannot reach its counter denies. Each of the three, one at a time.
//
// The budgets are spent in order, so a single fixture would only prove the first one fails closed.
// A limiter that failed open would disappear under exactly the load it exists to shed — which is
// the load an attacker is most likely to have caused.
func TestS28_CredentialLimitersFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name string
		slot string
	}{
		{"the per-source flood budget", slotFlood},
		{"the per-source failure budget", slotFailure},
		{"the per-target budget", slotTarget},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t, withBrokenCredentialLimiter(tc.slot))
			f.user("victim", pw)

			// The correct password, so nothing but the limiter can be what denies this.
			r := f.do(http.MethodPost, "/api/v1/login",
				withBody("application/json", loginBody("victim@ctf.test", pw)))

			if r.StatusCode != http.StatusServiceUnavailable {
				t.Errorf("status = %d, want 503 — %s could not reach its counter and let the "+
					"request through anyway", r.StatusCode, tc.name)
			}
			if strings.Contains(r.Body, "connection refused") {
				t.Errorf("the response quotes the driver error: %s", r.Body)
			}
		})
	}
}

// Keying on a submitted address must not turn the limiter into an account oracle.
//
// The budget is keyed on a hash of whatever was submitted, never on a lookup, so an address that
// belongs to nobody has a bucket exactly like one that does. Both run out after the same number of
// attempts and answer with the same body — otherwise the 429 itself would say "this account
// exists", which is the thing the login and reset responses go out of their way not to say.
func TestS29_CredentialLimitingLeaksNoAccountExistence(t *testing.T) {
	const budget = 4
	f := setup(t, withAuthLimit(budget), withTrustedProxy())
	f.user("real", pw)

	// Each address is probed from its own source, so nothing but the target distinguishes them.
	probe := func(email, source string) []resp {
		out := make([]resp, 0, budget+1)
		for i := 0; i <= budget; i++ {
			out = append(out, f.do(http.MethodPost, "/api/v1/login",
				withBody("application/json", loginBody(email, "wrong")),
				withForwardedFor(source)))
		}
		return out
	}

	live := probe("real@ctf.test", "203.0.113.20")
	dead := probe("nobody@ctf.test", "203.0.113.21")

	for i := range live {
		switch {
		case live[i].StatusCode != dead[i].StatusCode:
			t.Errorf("attempt %d: an account that exists answered %d and one that does not "+
				"answered %d. The status alone enumerates the user table",
				i+1, live[i].StatusCode, dead[i].StatusCode)
		case live[i].Body != dead[i].Body:
			t.Errorf("attempt %d: the bodies differ (%q vs %q) — the response says which "+
				"addresses are registered", i+1, live[i].Body, dead[i].Body)
		}
	}

	// …and the budget really did run out for both, or the loop above compared two runs of 401s
	// and proved nothing about the limiter at all.
	if live[budget].StatusCode != http.StatusTooManyRequests {
		t.Fatalf("attempt %d: %d, want 429 — the per-target budget of %d never bit, so this test "+
			"never compared a rate-limited response", budget+1, live[budget].StatusCode, budget)
	}
}
