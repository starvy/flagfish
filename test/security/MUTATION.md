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
| Team ban wall covers both credentials | `Decide` team-ban check → `if false`, **and** the middleware wall's team case disabled | ✅ FAILS: `member token after team ban: 200, want 403` (both members) |
| Team ban kills member sessions in-tx | drop the `DeleteTeamSessions` call from `SetTeamBanned` | ✅ FAILS: `member still holds 1 live sessions after the team ban` |
| Self-team-ban refusal | neutralize the actor-membership check in `SetTeamBanned` | ✅ FAILS: `self-team-ban: 200, want 409`, own team banned, admin walled out (401) |
| Masked team page is a 404 | drop `hidden = false AND banned = false` from `GetTeamPublicProfile` | ✅ FAILS: `ghost/outlaw team page: 200, want 404` |
| Masked teams off the public board | team-scoreboard `WHERE (admin OR NOT masked)` → `true` | ✅ FAILS: `public scoreboard shows map[ghost… outlaw…], want honest only` |
| Team PATCH mass assignment | wire `banned` through the PATCH (body field + `SET banned = COALESCE(…)`) | ✅ FAILS: `PATCH {"banned":true} was accepted` + landed in the row |
| User PATCH mass assignment | wire `role` through the PATCH (body field + `SET role = COALESCE(…)`) | ✅ FAILS: `PATCH {"role":"admin"} was accepted` + row changed |
| Forced-change wall | `Decide` forced-change gate → `if false` | ✅ FAILS: `forced user reached an ordinary route: 200, want 403` |
| Forced-change exit | drop `exemptFromPasswordChange` from `ClassPasswordChange` | ✅ FAILS: `the forced-change exit is walled: 403 … password-change-required` |
| Forced-change discharge | ChangePassword back to `UpdatePasswordHash` (hash only, no clear) | ✅ FAILS: flag survives the change, post-change session still walled |
| Login rehash leaves the flag | rehash path switched to the clearing query | ✅ FAILS: `the login rehash cleared must_change_password` |
| Audit feed redacts config secrets | `redactConfigAudit` → return raw | ✅ FAILS: S17 secret sweep — `GET /api/v1/admin/audit echoes the stored secret` (this is how the leak was found: wiring AdminOps into the fixture put /admin/audit inside S17's OpenAPI-driven sweep) |
| File download prerequisites | `downloadFile` checks only `meta.Hidden`, not `meta.PrereqsMet` | ✅ FAILS: the file of a prerequisite-locked challenge downloads by id (200, exact bytes) |
| Hint unlock prerequisites | drop the `challengePrereqsMet` gate from `UnlockHint` | ✅ FAILS: the hint of a prerequisite-locked challenge is sold, content and all |
| Security headers on every response | drop `securityHeaders` from the chain | ✅ FAILS (S23): nosniff / X-Frame-Options / CSP absent across the route sweep |
| CSP keeps scripts to 'self' | widen `spaCSP` script-src to add `'unsafe-inline'` | ✅ FAILS (S23): `script-src = "'self' 'unsafe-inline'", want 'self' exactly` |
| HSTS only on a TLS request | `securityHeaders` HSTS gate → `if true` (drop `servedOverTLS`) | ✅ FAILS (S23b): HSTS present on a plain-HTTP request |
| Auth routes limited tighter | drop `authRateLimit` from the chain | ✅ FAILS (S24): login runs to the general budget instead of the tighter one |
| Request-id returned + correlated | drop the `w.Header().Set(requestIDHeader, …)` in `requestID` | ✅ FAILS (S25): no X-Request-Id on the response, nothing to correlate |
| Request-id inbound trust | adopt an inbound id without the `peerTrusted` check | ✅ FAILS (S25): an untrusted client's forged id is echoed as the request id |

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
