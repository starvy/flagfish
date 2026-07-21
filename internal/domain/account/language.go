package account

import "strings"

// WellFormedLanguageTag reports whether s is a syntactically well-formed IETF BCP 47
// language tag (RFC 5646). It checks shape, not registration: "de", "zh-Hans", and
// "en-US" pass, and so does any tag that could one day name a locale. flagfish ships no
// message catalogs yet, so a closed list of accepted languages here would be an invented
// limit and would reject perfectly valid imported preferences; when catalogs exist, the
// closed list belongs with them, not on this column.
//
// The empty string is not a tag. Clearing the preference is a null, which never reaches
// here — an empty value is a malformed tag and is rejected as loudly as any other.
func WellFormedLanguageTag(s string) bool {
	if s == "" || len(s) > 63 {
		return false
	}
	parts := strings.Split(s, "-")
	for _, p := range parts {
		// A double hyphen, a leading/trailing hyphen, or a subtag over 8 chars is malformed
		// before the grammar even runs; so is anything that is not a plain ASCII alphanumeric.
		if l := len(p); l == 0 || l > 8 {
			return false
		}
		if !isAlphaNum(p) {
			return false
		}
	}
	if isGrandfathered(s) {
		return true
	}
	if isPrivateUseOnly(parts) {
		return true
	}
	return isLangtag(parts)
}

// isLangtag matches the RFC 5646 langtag production positionally: language, then an
// optional script, region, variants, extensions, and a trailing private-use section, each
// subtag class being unambiguous by shape given the ones before it.
func isLangtag(parts []string) bool {
	n := len(parts)
	var i int

	switch p := parts[0]; {
	case isAlphaLen(p, 2, 3):
		i = 1
		// extlang: up to three 3-alpha subtags. Nothing else at this position is 3-alpha,
		// so consuming them greedily can never swallow a script, region, or variant.
		for ext := 0; i < n && ext < 3 && isAlphaLen(parts[i], 3, 3); ext++ {
			i++
		}
	case isAlphaLen(p, 4, 4), isAlphaLen(p, 5, 8):
		i = 1
	default:
		return false
	}

	if i < n && isAlphaLen(parts[i], 4, 4) { // script
		i++
	}
	if i < n && (isAlphaLen(parts[i], 2, 2) || isDigitLen(parts[i], 3, 3)) { // region
		i++
	}
	for i < n && isVariant(parts[i]) {
		i++
	}
	for i < n && isSingleton(parts[i]) { // extension: singleton then 1+ subtags
		i++
		got := 0
		for i < n && isAlphaNumLen(parts[i], 2) {
			i++
			got++
		}
		if got == 0 {
			return false
		}
	}
	if i < n && isX(parts[i]) { // trailing private use
		i++
		got := 0
		for i < n && isAlphaNumLen(parts[i], 1) {
			i++
			got++
		}
		if got == 0 {
			return false
		}
	}
	return i == n
}

// isPrivateUseOnly matches a whole tag that is nothing but a private-use section ("x-...").
func isPrivateUseOnly(parts []string) bool {
	if len(parts) < 2 || !isX(parts[0]) {
		return false
	}
	for _, p := range parts[1:] {
		if !isAlphaNumLen(p, 1) {
			return false
		}
	}
	return true
}

// grandfathered irregular tags predate the langtag grammar and do not parse under it, so
// they are matched literally. The regular grandfathered tags all satisfy isLangtag and
// need no special case.
var grandfatheredIrregular = map[string]struct{}{
	"en-gb-oed": {}, "i-ami": {}, "i-bnn": {}, "i-default": {}, "i-enochian": {},
	"i-hak": {}, "i-klingon": {}, "i-lux": {}, "i-mingo": {}, "i-navajo": {}, "i-pwn": {},
	"i-tao": {}, "i-tay": {}, "i-tsu": {}, "sgn-be-fr": {}, "sgn-be-nl": {}, "sgn-ch-de": {},
}

func isGrandfathered(s string) bool {
	_, ok := grandfatheredIrregular[strings.ToLower(s)]
	return ok
}

func isVariant(s string) bool {
	if isAlphaNumLen(s, 5) {
		return true
	}
	return len(s) == 4 && isDigit(s[0]) && isAlphaNum(s)
}

func isSingleton(s string) bool {
	return len(s) == 1 && isAlphaNumByte(s[0]) && s[0] != 'x' && s[0] != 'X'
}

func isX(s string) bool { return s == "x" || s == "X" }

func isAlphaLen(s string, lo, hi int) bool {
	if len(s) < lo || len(s) > hi {
		return false
	}
	for i := range len(s) {
		if !isAlpha(s[i]) {
			return false
		}
	}
	return true
}

func isDigitLen(s string, lo, hi int) bool {
	if len(s) < lo || len(s) > hi {
		return false
	}
	for i := range len(s) {
		if !isDigit(s[i]) {
			return false
		}
	}
	return true
}

// maxSubtag is the RFC 5646 ceiling on a single alphanumeric subtag; every caller bounds by it.
const maxSubtag = 8

func isAlphaNumLen(s string, lo int) bool {
	if len(s) < lo || len(s) > maxSubtag {
		return false
	}
	return isAlphaNum(s)
}

func isAlphaNum(s string) bool {
	for i := range len(s) {
		if !isAlphaNumByte(s[i]) {
			return false
		}
	}
	return true
}

func isAlpha(b byte) bool        { return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') }
func isDigit(b byte) bool        { return b >= '0' && b <= '9' }
func isAlphaNumByte(b byte) bool { return isAlpha(b) || isDigit(b) }
