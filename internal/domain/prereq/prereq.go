// Package prereq models challenge and hint prerequisites: the `requirements` JSON stamped on a
// challenge or hint row, and how a row whose prerequisites the viewer has not met is shown.
//
// Whether the prerequisites are satisfied is a set-membership question answered in SQL against the
// account's solves (challenges) or unlocks (hints). This package owns only the pure part: decoding
// the JSON and turning the anonymize flag into a viewing decision.
package prereq

import (
	"encoding/json"
	"fmt"
)

// Visibility is what a viewer sees of a challenge whose prerequisites are unmet.
type Visibility uint8

const (
	// Hidden is the default: the locked challenge is invisible — absent from the board, not found
	// on detail, and not found on attempt, so its existence is never disclosed.
	Hidden Visibility = iota
	// Masked: the locked challenge appears on the board with its name replaced, carrying no
	// solvable content and rejecting attempts.
	Masked
	// Preview: like Masked, but the real name is shown so players can plan a route to it.
	Preview
)

// Visible reports whether a row with unmet prerequisites is shown at all.
func (v Visibility) Visible() bool { return v != Hidden }

// Requirements is the decoded `requirements` column of a challenge or hint.
type Requirements struct {
	// Prerequisites are the ids that must all be solved (for a challenge) or unlocked (for a hint)
	// before the row is playable.
	Prerequisites []int64
	// Visibility is meaningful only for challenges; hints carry no anonymize flag.
	Visibility Visibility
}

// Parse decodes a `requirements` jsonb value. An empty or absent value means no prerequisites.
func Parse(raw []byte) (Requirements, error) {
	if len(raw) == 0 {
		return Requirements{}, nil
	}
	var doc struct {
		Prerequisites []int64         `json:"prerequisites"`
		Anonymize     json.RawMessage `json:"anonymize"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return Requirements{}, fmt.Errorf("prereq: parse requirements: %w", err)
	}
	vis, err := parseVisibility(doc.Anonymize)
	if err != nil {
		return Requirements{}, err
	}
	return Requirements{Prerequisites: doc.Prerequisites, Visibility: vis}, nil
}

// parseVisibility reads the `anonymize` flag, which is bool | "preview". A malformed value fails
// loudly rather than silently defaulting a challenge to the wrong visibility.
func parseVisibility(raw json.RawMessage) (Visibility, error) {
	if len(raw) == 0 {
		return Hidden, nil
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err == nil {
		if b {
			return Masked, nil
		}
		return Hidden, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil && s == "preview" {
		return Preview, nil
	}
	return Hidden, fmt.Errorf("prereq: unrecognized anonymize value %q", string(raw))
}
