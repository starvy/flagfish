package accounts

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// TokenPrefix marks an API token on sight, so a leaked one is greppable in a log and
// recognizable in a paste.
const TokenPrefix = "flagfish_"

// SessionCookie is the cookie the SPA carries.
const SessionCookie = "flagfish_session"

// secretLen is 32 bytes of entropy for both credentials. Anything a browser or a CI job
// holds long-term gets a full-strength secret; there is no reason to be clever here.
const secretLen = 32

// newSecret returns a URL-safe random string and its sha256.
//
// Only the digest is ever stored. The plaintext exists in memory once, is handed to the
// caller once, and is never recoverable — so neither a database dump nor an export of one
// is a bag of live credentials.
func newSecret() (secret string, digest []byte, err error) {
	b := make([]byte, secretLen)
	if _, err := rand.Read(b); err != nil {
		return "", nil, fmt.Errorf("accounts: random: %w", err)
	}
	secret = hex.EncodeToString(b)
	h := sha256.Sum256([]byte(secret))
	return secret, h[:], nil
}

// digestOf hashes a presented credential for lookup. Presented secrets are compared by
// INDEXED DIGEST, never by scanning and byte-comparing rows — so there is no per-byte
// timing channel and the lookup is O(1) in the number of live credentials.
func digestOf(secret string) []byte {
	h := sha256.Sum256([]byte(secret))
	return h[:]
}

// NewSessionID mints a session id and the value to store.
func NewSessionID() (id string, idHash []byte, err error) { return newSecret() }

// NewAPIToken mints an API token and the value to store.
func NewAPIToken() (token string, tokenHash []byte, err error) {
	b := make([]byte, secretLen)
	if _, err := rand.Read(b); err != nil {
		return "", nil, fmt.Errorf("accounts: random: %w", err)
	}
	// The digest covers the token AS PRESENTED, prefix included, so lookup hashes exactly
	// the bytes the client sent and there is no normalization step to get wrong.
	token = TokenPrefix + hex.EncodeToString(b)
	return token, digestOf(token), nil
}

// bearerToken extracts a token from an Authorization header.
//
// The `Bearer` scheme is REQUIRED, and the header is honored uniformly on every request:
// authentication must not depend on the Content-Type, or the same token works on JSON and
// fails on an upload.
func bearerToken(header string) (string, bool) {
	scheme, rest, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	tok := strings.TrimSpace(rest)
	if tok == "" || !strings.HasPrefix(tok, TokenPrefix) {
		return "", false
	}
	return tok, true
}

// pwFingerprint is sha256 of the user's stored password hash, recorded on the session at
// issue.
//
// Comparing it on every request is what makes "changing your password logs out your
// other sessions" true without a revocation list: the fingerprint of a session minted
// against the old password simply stops matching. There is no fan-out delete to miss and
// no window in which a stolen cookie outlives the password it was minted against.
func pwFingerprint(passwordHash *string) []byte {
	if passwordHash == nil {
		return nil
	}
	h := sha256.Sum256([]byte(*passwordHash))
	return h[:]
}
