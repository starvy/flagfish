package flags_test

import (
	"errors"
	"testing"

	"github.com/starvy/flagfish/internal/domain/flags"
)

func TestStaticMatch(t *testing.T) {
	for _, tc := range []struct {
		name     string
		flag     flags.Flag
		provided string
		want     bool
	}{
		{"exact", flags.Flag{Content: "CTF{h3ap}"}, "CTF{h3ap}", true},
		{"wrong", flags.Flag{Content: "CTF{h3ap}"}, "CTF{nope}", false},
		{"case matters by default", flags.Flag{Content: "CTF{h3ap}"}, "ctf{h3ap}", false},
		{"case folded when asked", flags.Flag{Content: "CTF{h3ap}", CaseInsensitive: true}, "ctf{H3AP}", true},
		{"surrounding whitespace is stripped", flags.Flag{Content: "CTF{h3ap}"}, "  CTF{h3ap}\n", true},
		{"internal whitespace is preserved", flags.Flag{Content: "CTF{two words}"}, "CTF{two  words}", false},
		{"prefix is not a match", flags.Flag{Content: "CTF{h3ap}"}, "CTF{h3ap", false},
		{"superstring is not a match", flags.Flag{Content: "CTF{h3ap}"}, "CTF{h3ap}x", false},
		{"empty submission", flags.Flag{Content: "CTF{h3ap}"}, "", false},
	} {
		got, err := tc.flag.Match(tc.provided)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got != tc.want {
			t.Errorf("%s: Match(%q) = %v, want %v", tc.name, tc.provided, got, tc.want)
		}
	}
}

// Regression test for the folded-length prefix bug. Case folding can change a
// string's length ('İ' U+0130 lowercases to two codepoints), so a compare that
// length-checks the raw strings and then folds accepts a prefix as the flag. Fails
// if matchStatic is ever reduced to a raw-length guard plus a folded compare.
func TestCaseInsensitiveUnicodePrefixIsNotAMatch(t *testing.T) {
	// U+0130 LATIN CAPITAL LETTER I WITH DOT ABOVE. Same rune count as "IX" on the
	// raw side; a different length once lowercased.
	f := flags.Flag{Content: "CTF{İ}", CaseInsensitive: true}

	for _, provided := range []string{
		"CTF{i}", // the naive prefix that must not be accepted
		"CTF{I}",
		"CTF{ix}",
	} {
		got, err := f.Match(provided)
		if err != nil {
			t.Fatal(err)
		}
		if got {
			t.Errorf("Match(%q) accepted a non-flag: the folded-length prefix bug is back", provided)
		}
	}

	// The flag itself must still work, case-folded, or we broke the feature while
	// fixing the bug.
	if ok, err := f.Match("CTF{İ}"); err != nil || !ok {
		t.Errorf("the actual flag no longer matches itself: %v %v", ok, err)
	}
}

func TestRegexMatch(t *testing.T) {
	for _, tc := range []struct {
		name     string
		flag     flags.Flag
		provided string
		want     bool
	}{
		{"anchored pattern", flags.Flag{Type: flags.TypeRegex, Content: `^[A-Z]\d{3}$`}, "A123", true},
		{"anchored pattern rejects", flags.Flag{Type: flags.TypeRegex, Content: `^[A-Z]\d{3}$`}, "invalid", false},
		{"unanchored pattern still spans the whole input", flags.Flag{Type: flags.TypeRegex, Content: `[A-Z]\d{3}`}, "A123", true},
		// The critical one: a partial match must not pass.
		{"partial match is rejected", flags.Flag{Type: flags.TypeRegex, Content: `[A-Z]\d{3}`}, "A123trailing", false},
		{"leading junk is rejected", flags.Flag{Type: flags.TypeRegex, Content: `[A-Z]\d{3}`}, "xxA123", false},
		{"case sensitive by default", flags.Flag{Type: flags.TypeRegex, Content: `^[a-z]\d{3}$`}, "A123", false},
		{"case insensitive when asked", flags.Flag{Type: flags.TypeRegex, Content: `^[a-z]\d{3}$`, CaseInsensitive: true}, "A123", true},
		{"whitespace-tolerant flag", flags.Flag{Type: flags.TypeRegex, Content: `flag\{\s*w00t\s*\}`}, "flag{ w00t }", true},
		// A newline must not sneak past the end anchor: \z, not $.
		{"trailing newline inside the flag body", flags.Flag{Type: flags.TypeRegex, Content: `^[A-Z]\d{3}$`}, "A123\nX", false},
	} {
		got, err := tc.flag.Match(tc.provided)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got != tc.want {
			t.Errorf("%s: Match(%q) = %v, want %v", tc.name, tc.provided, got, tc.want)
		}
	}
}

// A broken pattern must be caught at the write boundary, so it can never reach a
// player's submission and 500 the hot path.
func TestBadRegexIsRejectedAtValidate(t *testing.T) {
	f := flags.Flag{Type: flags.TypeRegex, Content: `^[A-Z`}

	if err := f.Validate(); !errors.Is(err, flags.ErrBadPattern) {
		t.Fatalf("Validate() = %v, want ErrBadPattern", err)
	}
	// And if one somehow reaches submit anyway (a hand-edited row), it is a hard
	// error — never a silent "incorrect", which would tell the player their guess
	// was wrong when the truth is that the challenge is broken.
	if _, err := f.Match("anything"); !errors.Is(err, flags.ErrBadPattern) {
		t.Fatalf("Match() = %v, want ErrBadPattern", err)
	}
}

func TestValidateAcceptsGoodFlags(t *testing.T) {
	for _, f := range []flags.Flag{
		{Type: flags.TypeStatic, Content: "CTF{ok}"},
		{Type: flags.TypeRegex, Content: `^CTF\{\w+\}$`},
	} {
		if err := f.Validate(); err != nil {
			t.Errorf("Validate(%+v) = %v, want nil", f, err)
		}
	}
	if err := (flags.Flag{Content: ""}).Validate(); err == nil {
		t.Error("an empty flag must not validate")
	}
}

func TestMatchAny(t *testing.T) {
	fs := []flags.Flag{
		{Content: "CTF{one}"},
		{Content: "CTF{two}"},
		{Type: flags.TypeRegex, Content: `CTF\{thr(3|e)e\}`},
	}

	for _, tc := range []struct {
		provided string
		want     bool
	}{
		{"CTF{one}", true},
		{"CTF{two}", true},
		{"CTF{thr3e}", true},
		{"CTF{four}", false},
	} {
		got, err := flags.MatchAny(fs, tc.provided)
		if err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Errorf("MatchAny(%q) = %v, want %v", tc.provided, got, tc.want)
		}
	}

	if _, err := flags.MatchAny([]flags.Flag{{Type: flags.TypeRegex, Content: "("}}, "x"); !errors.Is(err, flags.ErrBadPattern) {
		t.Error("MatchAny must surface a corrupt pattern, not swallow it as 'incorrect'")
	}
	if got, err := flags.MatchAny(nil, "x"); err != nil || got {
		t.Error("a challenge with no flags cannot be solved")
	}
}

// The unique path: hash the submission, probe an index. Same flag -> same hash,
// normalization included; different flag -> different hash. That is the entire
// contract the caller depends on.
func TestHash(t *testing.T) {
	h := flags.Hash("CTF{unique_1}")

	if h != flags.Hash("  CTF{unique_1}  ") {
		t.Error("Hash must normalize whitespace exactly as Match does, or a valid flag with a stray newline misses the index")
	}
	if h == flags.Hash("CTF{unique_2}") {
		t.Error("distinct flags collided")
	}
	if h == flags.Hash("ctf{unique_1}") {
		t.Error("the unique path is exact-match by construction — case folding is not available to it")
	}
	if h == ([32]byte{}) {
		t.Error("zero hash")
	}
}

func TestParseModeAndType(t *testing.T) {
	if m, err := flags.ParseMode("unique"); err != nil || m != flags.ModeUnique {
		t.Errorf("ParseMode(unique) = %v, %v", m, err)
	}
	if _, err := flags.ParseMode("hmac"); err == nil {
		t.Error("an unknown flag_mode must not parse")
	}
	if ty, err := flags.ParseType("regex"); err != nil || ty != flags.TypeRegex {
		t.Errorf("ParseType(regex) = %v, %v", ty, err)
	}
	if _, err := flags.ParseType("static "); err == nil {
		t.Error("an unknown flag type must not parse")
	}
}

// The timing-safety caveat, made queryable so it cannot quietly become folklore.
func TestTimingSafetyIsHonestlyAdvertised(t *testing.T) {
	if !flags.TypeStatic.TimingSafe() {
		t.Error("static compare must be timing-safe")
	}
	if flags.TypeRegex.TimingSafe() {
		t.Error("regex compare is NOT timing-safe and must not claim to be")
	}
}
