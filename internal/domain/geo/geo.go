// Package geo is ISO 3166-1 alpha-2 country codes. It imports nothing outside the standard library.
//
// The list is closed on purpose. A challenge annotated with a country is going to be drawn on a map,
// and a renderer that is handed "UK" or "gb" or "GBR" has no country to draw — it has a string that
// looks like one. Deciding that here, once, at the write boundary, is the difference between an
// author finding out immediately and a player finding a challenge missing from the globe.
package geo

import (
	"errors"
	"fmt"
	"strings"
)

// ErrNotAlpha2 rejects a string that is not shaped like an alpha-2 code: exactly two characters,
// both uppercase ASCII letters.
var ErrNotAlpha2 = errors.New("geo: not an ISO 3166-1 alpha-2 code")

// ErrUnassigned rejects a well-shaped code that ISO 3166-1 does not assign to a country.
var ErrUnassigned = errors.New("geo: unassigned ISO 3166-1 alpha-2 code")

// An Alpha2 is an assigned ISO 3166-1 alpha-2 country code, always uppercase. The zero value is not
// a country; values only come from ParseAlpha2.
type Alpha2 string

func (c Alpha2) String() string { return string(c) }

// ParseAlpha2 returns the code for s, or an error naming why it is not one.
//
// It does NOT uppercase its input. A lowercase code is a rejection, not a value to fix up: the
// stored form has to be one thing for a renderer to key on, and quietly repairing input means the
// only place the canonical form is enforced is whichever writer happened to remember. Callers that
// want to be lenient can uppercase before calling and own that choice explicitly.
func ParseAlpha2(s string) (Alpha2, error) {
	if !wellShaped(s) {
		return "", fmt.Errorf("%w: %q (want two uppercase letters, e.g. \"CZ\")", ErrNotAlpha2, s)
	}
	if _, ok := assigned[s]; !ok {
		return "", fmt.Errorf("%w: %q", ErrUnassigned, s)
	}
	return Alpha2(s), nil
}

// ValidAlpha2 reports whether s is an assigned alpha-2 code, for callers that want a predicate
// rather than a value.
func ValidAlpha2(s string) bool {
	_, err := ParseAlpha2(s)
	return err == nil
}

func wellShaped(s string) bool {
	if len(s) != 2 {
		return false
	}
	return s[0] >= 'A' && s[0] <= 'Z' && s[1] >= 'A' && s[1] <= 'Z'
}

// codes is every alpha-2 code ISO 3166-1 currently assigns, alphabetical.
//
// Kept as one string rather than a Go map literal so that a revision of the standard — a country
// added, a code withdrawn — is a diff a reviewer can read as a change to a list, which is what it is.
// User-assigned ranges (AA, QM-QZ, XA-XZ, ZZ) are deliberately absent: they mean whatever a private
// agreement says they mean, and a map cannot draw one.
const codes = "AD AE AF AG AI AL AM AO AQ AR AS AT AU AW AX AZ " +
	"BA BB BD BE BF BG BH BI BJ BL BM BN BO BQ BR BS BT BV BW BY BZ " +
	"CA CC CD CF CG CH CI CK CL CM CN CO CR CU CV CW CX CY CZ " +
	"DE DJ DK DM DO DZ " +
	"EC EE EG EH ER ES ET " +
	"FI FJ FK FM FO FR " +
	"GA GB GD GE GF GG GH GI GL GM GN GP GQ GR GS GT GU GW GY " +
	"HK HM HN HR HT HU " +
	"ID IE IL IM IN IO IQ IR IS IT " +
	"JE JM JO JP " +
	"KE KG KH KI KM KN KP KR KW KY KZ " +
	"LA LB LC LI LK LR LS LT LU LV LY " +
	"MA MC MD ME MF MG MH MK ML MM MN MO MP MQ MR MS MT MU MV MW MX MY MZ " +
	"NA NC NE NF NG NI NL NO NP NR NU NZ " +
	"OM " +
	"PA PE PF PG PH PK PL PM PN PR PS PT PW PY " +
	"QA " +
	"RE RO RS RU RW " +
	"SA SB SC SD SE SG SH SI SJ SK SL SM SN SO SR SS ST SV SX SY SZ " +
	"TC TD TF TG TH TJ TK TL TM TN TO TR TT TV TW TZ " +
	"UA UG UM US UY UZ " +
	"VA VC VE VG VI VN VU " +
	"WF WS " +
	"YE YT " +
	"ZA ZM ZW"

var assigned = func() map[string]struct{} {
	fields := strings.Fields(codes)
	m := make(map[string]struct{}, len(fields))
	for _, c := range fields {
		m[c] = struct{}{}
	}
	return m
}()

// Count is how many codes are assigned. Exported so a test can pin the list against an accidental
// edit — a dropped country is otherwise invisible until somebody's challenge will not save.
func Count() int { return len(assigned) }
