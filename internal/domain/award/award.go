// Package award holds the pure validation rules for a manual, out-of-band point adjustment: the
// admin action behind a cheating penalty or a live-dispute correction.
//
// This package imports nothing outside the standard library. It knows nothing about SQL, HTTP or the
// accounts model, and it never touches a database — the rules here are what make a manual award
// meaningful before it ever reaches the ledger.
package award

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// MaxReasonLen bounds the justification. It is a display/audit field, not free-form storage; a
// generous cap keeps the audit trail readable without inviting a wall of text.
const MaxReasonLen = 500

var (
	// ErrZeroValue rejects a zero adjustment. A zero award moves no standings and is pure ledger
	// noise — the scoreboard's `value <> 0` predicate skips it anyway, so it would sit in the
	// ledger and the audit trail doing nothing but confusing the next reader.
	ErrZeroValue = errors.New("award: adjustment must be non-zero")
	// ErrEmptyReason rejects a missing justification. An out-of-band score change is an admin
	// decision that must always carry its reason into the audit trail; a blank reason is exactly
	// the silent, unexplained correction this surface exists to prevent.
	ErrEmptyReason = errors.New("award: a reason is required")
	// ErrReasonTooLong rejects an over-long justification.
	ErrReasonTooLong = errors.New("award: reason is too long")
)

// ValidateManual checks a manual adjustment and returns the cleaned reason to store. It is total:
// every rejection is one of the sentinels above, and the returned string is the trimmed reason the
// caller should persist. value is the point delta (may be negative); reason is the operator's
// justification.
func ValidateManual(value int32, reason string) (string, error) {
	if value == 0 {
		return "", ErrZeroValue
	}
	cleaned := strings.TrimSpace(reason)
	if cleaned == "" {
		return "", ErrEmptyReason
	}
	if utf8.RuneCountInString(cleaned) > MaxReasonLen {
		return "", ErrReasonTooLong
	}
	return cleaned, nil
}
