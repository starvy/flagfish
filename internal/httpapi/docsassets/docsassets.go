// Package docsassets vendors the Stoplight Elements bundle that renders the admin
// API reference. Serving it from the binary instead of a CDN keeps the reference
// working on the isolated networks CTF events routinely run on, and removes an
// external origin from the admin page's CSP.
//
// Pinned at v9.0.15. To update: fetch web-components.min.js, styles.min.css and the
// matching LICENSE from the same version tag, drop them in here, and bump Version.
package docsassets

import "embed"

// Version is the upstream @stoplight/elements release these files were taken from.
const Version = "9.0.15"

//go:embed stoplight-elements.min.js stoplight-elements.min.css
var FS embed.FS
