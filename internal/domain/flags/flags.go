// Package flags holds the three flag comparison paths and the FlagIssuer seam.
//
// Two orthogonal axes, and keeping them apart is the whole design:
//
//   - Mode  (challenges.flag_mode) — how a flag is issued: shared, or one per account.
//   - Type  (flags.type)           — how a flag is compared: byte-equal, or regex.
//
// Type is only meaningful when Mode is ModeStatic: a regex flag cannot be
// pool-issued, because a pool entry is a concrete string that was baked into an
// artifact.
//
// This package imports nothing outside the standard library.
package flags

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Mode is how flags are issued for a challenge (challenges.flag_mode).
type Mode uint8

const (
	// ModeStatic: every account submits against the same set of flags.
	ModeStatic Mode = iota
	// ModeUnique: each account is lazily issued its own instance from a pool.
	// Comparison is by hash, and the hash identifies the account the flag was
	// issued to — which is what makes sharing detectable.
	ModeUnique
)

var modeNames = map[Mode]string{ModeStatic: "static", ModeUnique: "unique"}

func (m Mode) String() string {
	if s, ok := modeNames[m]; ok {
		return s
	}
	return fmt.Sprintf("Mode(%d)", uint8(m))
}

// ParseMode maps challenges.flag_mode onto a Mode.
func ParseMode(s string) (Mode, error) {
	for m, name := range modeNames {
		if name == s {
			return m, nil
		}
	}
	return 0, fmt.Errorf("flags: unknown flag_mode %q", s)
}

// Type is how a single stored flag is compared (flags.type).
type Type uint8

const (
	// TypeStatic compares bytes. Timing-safe (crypto/subtle).
	TypeStatic Type = iota
	// TypeRegex compares by pattern.
	//
	// Regex matching cannot be made timing-safe, and we do not pretend otherwise.
	// Regex flags are genuinely used — variable whitespace and case around flag{…} —
	// so the timing channel is accepted rather than papered over. "Timing-safe
	// comparison" in this product means TypeStatic and ModeUnique.
	TypeRegex
)

var typeNames = map[Type]string{TypeStatic: "static", TypeRegex: "regex"}

func (t Type) String() string {
	if s, ok := typeNames[t]; ok {
		return s
	}
	return fmt.Sprintf("Type(%d)", uint8(t))
}

// ParseType maps flags.type onto a Type.
func ParseType(s string) (Type, error) {
	for t, name := range typeNames {
		if name == s {
			return t, nil
		}
	}
	return 0, fmt.Errorf("flags: unknown flag type %q", s)
}

// TimingSafe reports whether this comparison path leaks nothing through timing.
// It is false for TypeRegex, on purpose, and this method exists so that fact is
// queryable rather than folklore.
func (t Type) TimingSafe() bool { return t == TypeStatic }

// Logic is how a challenge's several flags combine (challenges.logic). It is only
// meaningful for ModeStatic: a unique-flag challenge is one flag per account, so
// there is nothing to combine and the column is refused a non-default value there.
type Logic uint8

const (
	// LogicAny: any one flag satisfies the challenge. This is the default.
	LogicAny Logic = iota
	// LogicAll: every flag must be satisfied by the same submission.
	LogicAll
)

var logicNames = map[Logic]string{LogicAny: "any", LogicAll: "all"}

func (l Logic) String() string {
	if s, ok := logicNames[l]; ok {
		return s
	}
	return fmt.Sprintf("Logic(%d)", uint8(l))
}

// ParseLogic maps challenges.logic onto a Logic over the closed set the column
// allows; an unknown value is a corrupt row, and the caller treats it as a hard
// error rather than defaulting silently.
func ParseLogic(s string) (Logic, error) {
	for l, name := range logicNames {
		if name == s {
			return l, nil
		}
	}
	return 0, fmt.Errorf("flags: unknown logic %q", s)
}

// A Flag is one stored flag on a challenge.
type Flag struct {
	Type Type
	// Content is the saved flag: the literal string, or the regex pattern.
	// It is stored stripped of leading/trailing whitespace.
	Content string
	// CaseInsensitive is set only by an explicit "case_insensitive" marker at the
	// boundary; any other stored value — including "" and NULL — means case-sensitive.
	CaseInsensitive bool
}

// ErrBadPattern is returned when a regex flag's pattern does not compile.
var ErrBadPattern = errors.New("flags: regex does not compile")

// Validate compiles a regex flag's pattern so that a broken pattern is rejected
// at the write boundary — on flag create/update and on import. A pattern that
// only failed at submit time would surface as a 500 on a player's guess, and a
// wrong guess must never be able to 500 the hot path.
func (f Flag) Validate() error {
	if f.Content == "" {
		return errors.New("flags: content is empty")
	}
	if f.Type == TypeRegex {
		if _, err := f.compile(); err != nil {
			return err
		}
	}
	return nil
}

// Normalize prepares a submitted flag for comparison: leading/trailing
// whitespace is stripped, internal whitespace is preserved, and no Unicode
// normalization (NFC/NFKC) is performed — comparison is codepoint-wise.
func Normalize(provided string) string { return strings.TrimSpace(provided) }

// Match reports whether `provided` satisfies this flag. Callers pass the raw
// submission; Match normalizes it.
func (f Flag) Match(provided string) (bool, error) {
	p := Normalize(provided)

	switch f.Type {
	case TypeStatic:
		return matchStatic(f.Content, p, f.CaseInsensitive), nil

	case TypeRegex:
		re, err := f.compile()
		if err != nil {
			return false, err
		}
		// Anchored at both ends: a partial match must never pass. \A…\z says
		// that once, where it cannot be forgotten.
		return re.MatchString(p), nil

	default:
		return false, fmt.Errorf("flags: unknown flag type %d", f.Type)
	}
}

// matchStatic is the timing-safe path. It closes two Unicode defects: fold before
// comparing, not after (case folding can change length — 'İ' U+0130 lowercases to
// two codepoints — so length-checking raw and then folding can accept a prefix),
// and fold ASCII-only (strings.ToLower folds U+0130, U+212A and others onto
// 'i'/'k', silently widening what a "case insensitive" flag accepts).
func matchStatic(saved, provided string, caseInsensitive bool) bool {
	if caseInsensitive {
		saved, provided = foldASCII(saved), foldASCII(provided)
	}
	// ConstantTimeCompare returns 0 on a length mismatch. Length is not secret —
	// content is.
	return subtle.ConstantTimeCompare([]byte(saved), []byte(provided)) == 1
}

// foldASCII lowercases A-Z and touches nothing else.
//
// Byte-wise on purpose: length-preserving (unlike strings.ToLower) and with no
// case-folding collisions outside ASCII — the two properties matchStatic relies on.
func foldASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}

func (f Flag) compile() (*regexp.Regexp, error) {
	pattern := `\A(?:` + f.Content + `)\z`
	if f.CaseInsensitive {
		// Go's (?i) applies Unicode case folding — the widening foldASCII removes
		// from the static path (U+212A folds to 'k', and so on). Accepted here
		// because a regex flag is an arbitrary author-supplied pattern: an author
		// who wants a wider match writes it. A static flag is a literal, and
		// widening it behind the author's back is a defect.
		pattern = `(?i)` + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		// Both verbs wrap: errors.Is finds ErrBadPattern, and the regexp error stays
		// inspectable underneath it.
		return nil, fmt.Errorf("%w: %q: %w", ErrBadPattern, f.Content, err)
	}
	return re, nil
}

// MatchAny reports whether `provided` satisfies any of the challenge's flags,
// which is the default flag logic.
//
// It evaluates every flag rather than short-circuiting on the first match: with a
// list of static flags, short-circuiting would leak — through response latency —
// which flag matched, and a challenge with several flags of different lengths
// would tell an attacker which one they are close to. The whole point of
// crypto/subtle is lost if the loop above it branches on the result.
//
// A regex flag that fails to compile is a hard error (never a silent "incorrect"):
// it means the flag row is corrupt, which is an operator problem, not a player one.
func MatchAny(fs []Flag, provided string) (bool, error) {
	matched := false
	for _, f := range fs {
		ok, err := f.Match(provided)
		if err != nil {
			return false, err
		}
		matched = matched || ok
	}
	return matched, nil
}

// MatchAll reports whether `provided` satisfies every one of the challenge's
// flags, which is the challenges.logic='all' path.
//
// Like MatchAny it evaluates every flag and never short-circuits: bailing on the
// first flag that fails would leak, through response latency, which flag the
// submission fell short on — the same timing channel crypto/subtle closes on a
// single flag, reopened by the loop above it.
//
// MatchAll(nil) is false, symmetric with MatchAny(nil): "all of no flags" is a
// vacuous truth the submit path must never treat as a solve, so an empty set is
// unsatisfiable here rather than trivially satisfied. checkStatic already refuses
// a flagless challenge before this is reached; this keeps the domain honest on its
// own.
//
// A regex flag that fails to compile is a hard error, never a silent "incorrect":
// the flag row is corrupt, which is an operator problem, not a player one.
func MatchAll(fs []Flag, provided string) (bool, error) {
	if len(fs) == 0 {
		return false, nil
	}
	matched := true
	for _, f := range fs {
		ok, err := f.Match(provided)
		if err != nil {
			return false, err
		}
		matched = matched && ok
	}
	return matched, nil
}

// Hash is the ModeUnique comparison path in its entirety: sha256 of the
// normalized submission. The caller probes challenge_instances(challenge_id,
// value_hash) with it — one indexed lookup.
//
// This path never does a byte-wise comparison at all, so there is no per-byte
// timing channel to leak. It is O(1) in the number of accounts, not O(N), and it
// is stronger than a constant-time compare rather than a compromise: constant-time
// comparison against a pool of 500 flags is not viable, and it is not necessary.
//
// The plaintext flag is never stored — only this hash is.
func Hash(provided string) [sha256.Size]byte {
	return sha256.Sum256([]byte(Normalize(provided)))
}
