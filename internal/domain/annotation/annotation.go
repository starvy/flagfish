// Package annotation is the shape rules for challenge annotations: a small, open namespace of
// (key, value) pairs an author hangs off a challenge for a renderer to read. It imports nothing
// outside the standard library and its sibling domain packages.
//
// Most keys mean nothing here — an annotation is a string a client agreed to look for, and this
// package only guarantees the key is a usable identifier and the value fits. A few keys mean
// something specific, and those are declared in one place (see wellKnown) so that teaching the
// product about a new one is a line of code, not a search for every writer.
package annotation

import (
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/starvy/flagfish/internal/domain/geo"
)

const (
	// MaxKeyLen and MaxValueLen mirror the CHECK constraints on challenge_annotations. They are
	// duplicated rather than derived because the database cannot tell a caller *why* it refused,
	// and an operator deserves the reason before the write, not a constraint name after it.
	MaxKeyLen   = 64
	MaxValueLen = 256
)

var (
	// ErrKeyShape rejects a key that is not a lowercase machine identifier. Keys are looked up by
	// literal name in client code, so "Country", "country " and "country-code" are three different
	// keys that all look like the one the renderer wants — the narrow shape is what stops that.
	ErrKeyShape = errors.New("annotation: a key must be 1-64 characters of a-z, 0-9 and _, starting with a letter")

	// ErrValueEmpty rejects a blank value. Absence is expressed by deleting the annotation, so an
	// empty value is a second, silent spelling of "not set" and is refused.
	ErrValueEmpty = errors.New("annotation: a value is required")

	// ErrValueTooLong rejects a value past MaxValueLen.
	ErrValueTooLong = errors.New("annotation: value is too long")
)

// A Key is a validated annotation key: lowercase, machine-readable, and already known to match the
// column's CHECK. The zero value is not a key; values only come from ParseKey.
type Key string

func (k Key) String() string { return string(k) }

// KeyCountry places a challenge on the map. Its value is an ISO 3166-1 alpha-2 code, uppercase.
const KeyCountry Key = "country"

// ParseKey validates the key's shape.
func ParseKey(s string) (Key, error) {
	if len(s) == 0 || len(s) > MaxKeyLen {
		return "", fmt.Errorf("%w: got %q", ErrKeyShape, s)
	}
	if s[0] < 'a' || s[0] > 'z' {
		return "", fmt.Errorf("%w: got %q", ErrKeyShape, s)
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' {
			return "", fmt.Errorf("%w: got %q", ErrKeyShape, s)
		}
	}
	return Key(s), nil
}

// wellKnown is the one place a key carries meaning beyond "some string the client agreed on".
//
// Adding a key here is what teaches the whole product about it: every write goes through Clean, so
// the admin endpoint, any future importer and the tests all pick the rule up from this map. A key
// that is absent is not an error — it is simply an annotation nothing in this binary interprets,
// which is the normal case and the reason the namespace is open at all.
var wellKnown = map[Key]func(string) (string, error){
	KeyCountry: func(v string) (string, error) {
		c, err := geo.ParseAlpha2(v)
		if err != nil {
			return "", err
		}
		return c.String(), nil
	},
}

// Clean validates a value for a key and returns the form to store.
//
// The return is the canonical value, not the input: a well-known key decides its own storage form,
// and returning it here means no caller has to remember to normalize. For every other key the value
// is stored exactly as given — this package does not trim, case-fold or otherwise improve a string
// whose meaning it does not know.
func Clean(k Key, value string) (string, error) {
	if value == "" {
		return "", ErrValueEmpty
	}
	if n := utf8.RuneCountInString(value); n > MaxValueLen {
		return "", fmt.Errorf("%w: %d characters, the maximum is %d", ErrValueTooLong, n, MaxValueLen)
	}
	if clean, ok := wellKnown[k]; ok {
		v, err := clean(value)
		if err != nil {
			return "", fmt.Errorf("annotation %q: %w", k, err)
		}
		return v, nil
	}
	return value, nil
}

// WellKnown reports whether this package interprets the key, i.e. whether Clean applies a rule
// beyond the generic length check.
func WellKnown(k Key) bool {
	_, ok := wellKnown[k]
	return ok
}
