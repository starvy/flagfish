// Package e2e is a black-box end-to-end suite: a generated OpenAPI client drives a
// running flagfish server over HTTP. The tests live behind the `e2e` build tag and are
// run against a deployed stack by `task e2e` (or against any base URL via E2E_BASE_URL).
//
// The suite talks to the public API through the generated client in e2e/client and to
// the admin API — a separate Huma document not in the published openapi.yaml — through a
// small typed HTTP helper. It touches the database only for out-of-band setup that has
// no API: marking the instance set up and promoting the first admin. Every assertion
// goes over the wire.
//
// The client is generated from the repo-root openapi.yaml. Regenerate it with:
//
//	task e2e-gen
package e2e
