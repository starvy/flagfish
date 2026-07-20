package account_test

import (
	"testing"

	"github.com/starvy/flagfish/internal/domain/account"
)

func TestWellFormedLanguageTag(t *testing.T) {
	for _, tc := range []struct {
		tag  string
		want bool
	}{
		// The bread-and-butter tags a player picks, and the ones CTFd imports carry.
		{"de", true},
		{"zh", true},
		{"en", true},
		{"pl", true},
		{"es", true},
		{"en-US", true},
		{"pt-BR", true},
		{"zh-Hans", true},
		{"zh-Hant-HK", true},
		{"es-419", true}, // UN M.49 numeric region
		{"sr-Latn-RS", true},
		{"de-CH-1996", true}, // variant
		{"en-a-bbb-x-y-z", true},
		{"x-private", true},  // private-use only
		{"i-klingon", true},  // grandfathered irregular
		{"EN-us", true},      // case-insensitive
		{"zh-min-nan", true}, // regular grandfathered, parses as langtag

		{"", false},
		{"e", false},              // primary subtag too short
		{"toolongprimary", false}, // subtag over 8 chars
		{"en_US", false},          // underscore is not a separator
		{"en-", false},            // trailing hyphen ⇒ empty subtag
		{"-en", false},            // leading hyphen ⇒ empty subtag
		{"en--US", false},         // double hyphen ⇒ empty subtag
		{"de!", false},            // non-alphanumeric
		{"a", false},              // bare singleton is not a tag
		{"123", false},            // a language cannot be all digits
		{"en-a", false},           // extension singleton with no subtag
		{"x", false},              // private-use prefix with nothing after it
	} {
		if got := account.WellFormedLanguageTag(tc.tag); got != tc.want {
			t.Errorf("WellFormedLanguageTag(%q) = %v, want %v", tc.tag, got, tc.want)
		}
	}
}
