// Package field holds the pure rules for custom registration fields: the admin-defined profile
// questions collected at sign-up (affiliation, eligibility, a consent checkbox). It validates a
// field definition and canonicalizes an answer against the field's type.
//
// This package imports nothing outside the standard library. It knows nothing about SQL, HTTP, or
// the accounts model — the "is this a well-formed field" and "does this answer fit the type"
// decisions are made here, before anything reaches the database, so the login-gating rules cannot
// be smuggled past.
package field

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"
)

// The field types. This set is the CHECK on fields.field_type; it is closed by design (no plugin
// system), so the two spellings live here as the one source both the validator and the encoder read.
const (
	TypeText    = "text"
	TypeBoolean = "boolean"
)

// The owner kinds. This set is the CHECK on fields.applies_to. A field is answered by a user at
// registration, or by a team at team creation — never both, which the one-owner CHECK on
// field_entries enforces in turn.
const (
	AppliesUser = "user"
	AppliesTeam = "team"
)

// MaxNameLen and MaxDescriptionLen bound the operator-authored strings. A field name is a form
// label, not storage; the description is help text. Generous caps keep the admin surface readable
// without inviting a wall of text.
const (
	MaxNameLen        = 128
	MaxDescriptionLen = 500
	// MaxTextAnswerLen bounds a text answer. It is a profile field, not free-form content.
	MaxTextAnswerLen = 500
)

var (
	// ErrEmptyName rejects a field with no label. A nameless field cannot be rendered or answered.
	ErrEmptyName = errors.New("field: a name is required")
	// ErrNameTooLong rejects an over-long label.
	ErrNameTooLong = errors.New("field: name is too long")
	// ErrDescriptionTooLong rejects over-long help text.
	ErrDescriptionTooLong = errors.New("field: description is too long")
	// ErrUnknownType rejects a field_type outside the closed set.
	ErrUnknownType = errors.New("field: unknown field type")
	// ErrUnknownAppliesTo rejects an applies_to outside the closed set.
	ErrUnknownAppliesTo = errors.New("field: applies_to must be 'user' or 'team'")

	// ErrTypeMismatch rejects an answer whose JSON shape does not fit the field's type — a bool
	// for a text field, a string for a checkbox. Loud, never coerced: a silently-coerced answer
	// is a silently-wrong one.
	ErrTypeMismatch = errors.New("field: answer does not match the field type")
	// ErrTextTooLong rejects an over-long text answer.
	ErrTextTooLong = errors.New("field: text answer is too long")
	// ErrRequiredMissing rejects an empty answer to a required field. This is the whole point of
	// the register-time validation: a required field left blank must fail loudly at sign-up, not
	// pass and then brick the profile-complete login gate with no remedy.
	ErrRequiredMissing = errors.New("field: this field is required")
)

// ValidType reports whether t is a known field type.
func ValidType(t string) bool { return t == TypeText || t == TypeBoolean }

// ValidAppliesTo reports whether a is a known owner kind.
func ValidAppliesTo(a string) bool { return a == AppliesUser || a == AppliesTeam }

// CleanName trims and validates a field label, returning the value to store.
func CleanName(name string) (string, error) {
	cleaned := strings.TrimSpace(name)
	if cleaned == "" {
		return "", ErrEmptyName
	}
	if utf8.RuneCountInString(cleaned) > MaxNameLen {
		return "", ErrNameTooLong
	}
	return cleaned, nil
}

// CleanDescription trims optional help text. A nil pointer stays nil; a value that trims to empty
// becomes nil, so "no description" has one representation.
func CleanDescription(desc *string) (*string, error) {
	if desc == nil {
		return nil, nil
	}
	cleaned := strings.TrimSpace(*desc)
	if cleaned == "" {
		return nil, nil
	}
	if utf8.RuneCountInString(cleaned) > MaxDescriptionLen {
		return nil, ErrDescriptionTooLong
	}
	return &cleaned, nil
}

// Normalize validates a raw JSON answer against a field type and returns the canonical value to
// store plus whether that value counts as a present answer. `present` uses exactly the predicate
// the login gate applies (a NULL, a JSON null, or an empty string is absent), so "Normalize said
// present" and "the gate sees the required field answered" can never disagree.
//
// raw is the answer as it arrived on the wire; nil or a JSON null means "no answer given".
//
//   - text:    raw must be a JSON string (or null). Surrounding whitespace is trimmed; an empty
//     result is absent and stored as JSON null.
//   - boolean: raw must be a JSON bool (or null). true and false are both present answers.
func Normalize(fieldType string, raw json.RawMessage) (value json.RawMessage, present bool, err error) {
	if isJSONAbsent(raw) {
		return jsonNull, false, nil
	}
	switch fieldType {
	case TypeText:
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, false, ErrTypeMismatch
		}
		s = strings.TrimSpace(s)
		if s == "" {
			return jsonNull, false, nil
		}
		if utf8.RuneCountInString(s) > MaxTextAnswerLen {
			return nil, false, ErrTextTooLong
		}
		encoded, err := json.Marshal(s)
		if err != nil {
			return nil, false, err
		}
		return encoded, true, nil
	case TypeBoolean:
		var b bool
		if err := json.Unmarshal(raw, &b); err != nil {
			return nil, false, ErrTypeMismatch
		}
		if b {
			return jsonTrue, true, nil
		}
		return jsonFalse, true, nil
	default:
		return nil, false, ErrUnknownType
	}
}

// IsAnswered reports whether a stored value satisfies the required-field gate: not NULL, not a JSON
// null, not an empty string. It mirrors the SQL predicate in the login `profile_complete` query
// exactly, so the two cannot drift — the Go side that decides "this required field is answerable"
// and the SQL side that decides "this account may log past the gate" agree by construction.
func IsAnswered(value json.RawMessage) bool {
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) || bytes.Equal(trimmed, []byte(`""`)) {
		return false
	}
	return true
}

// The canonical stored encodings, so the write path never re-marshals a constant.
var (
	jsonNull  = json.RawMessage("null")
	jsonTrue  = json.RawMessage("true")
	jsonFalse = json.RawMessage("false")
)

// isJSONAbsent reports whether a raw wire value means "no answer": nil, empty, or a literal null.
func isJSONAbsent(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null"))
}
