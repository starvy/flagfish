package flags

import (
	"context"
	"errors"
)

// An Issue is one account's assignment from a challenge's instance pool.
//
// It is a bundle, not a flag: the flag (as a hash — the plaintext is never
// stored), optionally a per-account artifact, and the per-account variables that
// the challenge description template renders against.
type Issue struct {
	InstanceID int64
	// ValueHash is sha256(flag). There is deliberately no plaintext field: the
	// platform cannot leak a flag it does not hold.
	ValueHash [32]byte
	// ArtifactID is the per-account binary/VM/PDF, if the author uploaded one.
	ArtifactID *int64
}

// An Attribution is the answer to "who was this flag issued to?".
//
// AccountID is the account the submitted flag was issued to — which is not
// necessarily the account that submitted it. That difference is the entire
// anti-cheat signal, and it is stamped onto submissions.attributed_account_id
// inside the submit transaction. Stamp the fact; don't recompute it.
type Attribution struct {
	// Correct: the flag is valid for this challenge. Note this is independent of
	// who it was issued to. A player submitting someone else's valid flag is
	// correct — accepted, solved, and silently flagged for admin review. A
	// detector that announces itself is not a detector.
	Correct bool
	// AccountID is the issuing account, or 0 if the flag matched nothing.
	AccountID int64
	// InstanceID is the pool entry that carried this flag, or 0.
	InstanceID int64
}

// Issuer is the only thing the gameplay layer knows about how unique flags come
// to exist.
//
// v1 implements it with a pre-generated pool + lazy assignment on first access.
// A future HMAC-templated implementation — flag = f(serverKey, challengeID,
// accountID), no pool table at all — is a drop-in replacement for exactly these
// two methods, and because attribution is stamped at submit time rather than
// joined at query time, swapping it leaves every query, report and audit view
// untouched.
//
// Both methods take a transaction-scoped querier from the caller; this interface
// deliberately does not know what a database is.
type Issuer interface {
	// IssueFor assigns an unused instance to an account, idempotently.
	//
	// Called on an account's first access to a unique-flag challenge (view or
	// artifact download) — the one place in the product where reading a challenge
	// mutates state. Concurrency safety is not this method's business: it belongs
	// to PRIMARY KEY (challenge_id, account_id) and UNIQUE (instance_id), which
	// make double-issuance unrepresentable rather than merely unlikely.
	//
	// Pool exhaustion is a hard failure (ErrPoolExhausted), never a fallback to a
	// shared flag: a silent fallback destroys the uniqueness property for exactly
	// the late registrants you were most suspicious of.
	IssueFor(ctx context.Context, challengeID, accountID int64) (Issue, error)

	// Attribute hashes the submitted flag, probes the challenge's instance pool,
	// and reports whether it is correct and who it was issued to.
	Attribute(ctx context.Context, challengeID int64, provided string) (Attribution, error)
}

// ErrPoolExhausted is returned by IssueFor when every instance in the pool is
// already assigned. The challenge becomes unavailable to that account (503) and an
// admin alert fires. The real fix is prevention: the pre-event utilisation gauge
// (`flagfishctl pool stats`).
var ErrPoolExhausted = errors.New("flags: challenge instance pool exhausted — no unissued instance remains")
