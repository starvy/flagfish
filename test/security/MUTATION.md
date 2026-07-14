# Security suite — mutation results

A security test that cannot fail is a comment. Each test below was verified by breaking
the guard it defends and confirming the test goes red. Re-run these by hand after any
change to the auth path.

| Property | Guard removed | Result |
|---|---|---|
| Ban wall | `Decide` ban check → `if false`, **and** wall made cookie-only | ✅ FAILS: `banned + TOKEN: 200, want 403` |
| Password-change fingerprint | session fingerprint compare → `if false` | ✅ FAILS: old session survives password change |
| CSRF | CSRF compare → `if false` | ✅ FAILS: forged write accepted |
| CSRF exemption | exempt on `Authorization` header present, not on identity | ✅ FAILS: stapled-header bypass |
| Rate limit | atomic upsert → read-then-write | ✅ FAILS: lost increments, allowed > limit |
| User enumeration | drop the dummy-verify on unknown email | ✅ FAILS: timing ratio blows the oracle assertion |
| Secure cookie | `isSecure` derives Secure from TLS/proxy trust only | ✅ FAILS: session cookie issued without `Secure` |
| Per-client rate limit | `realIP` keeps the socket peer, ignoring a trusted `X-Forwarded-For` | ✅ FAILS: one client's flood limits the next client |
| Body limit | drop `limitBody` from the chain | ✅ FAILS: an unbounded body and an unbounded upload are both accepted |
| Auth error leak | auth failure → `401` with `err.Error()` | ✅ FAILS: 401 instead of 503, driver text in the body |
| Admin spec exposure | let Huma register the admin docs/schema (it bypasses the middleware) | ✅ FAILS: anonymous and player both read the admin OpenAPI document |
| Rate-limit bucket identity | bucket key → `r.Method + ":" + r.URL.Path` (the raw path) | ✅ FAILS: `/probe/007` and `/probe/+7` each get a fresh budget after `/probe/7` is spent |
| File download prerequisites | `downloadFile` checks only `meta.Hidden`, not `meta.PrereqsMet` | ✅ FAILS: the file of a prerequisite-locked challenge downloads by id (200, exact bytes) |
| Hint unlock prerequisites | drop the `challengePrereqsMet` gate from `UnlockHint` | ✅ FAILS: the hint of a prerequisite-locked challenge is sold, content and all |

The last two are integration tests (`test/integration`: `TestFileDownloadRequiresPrerequisites`,
`TestChallengePrerequisiteGatesHintUnlock`) — the guard needs the catalog, gameplay and object-storage
services wired, which the security fixture deliberately leaves out. They are mutation-verified the
same way, and they belong on this list because they are the same property as the board's: **a
prerequisite-locked challenge has no side door.** The read path enforced it; the download and the
unlock did not.

**Note the ban is enforced twice** — the middleware wall *and* `policy.Decide` — so a
single-point mutation still trips the other. That is defense in depth, not redundancy:
the ban wall only goes red when *both* are broken, which is the honest statement of the
property ("a banned principal is refused, however the refusal is spelled").
