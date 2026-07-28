package annotation_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/starvy/flagfish/internal/domain/annotation"
	"github.com/starvy/flagfish/internal/domain/geo"
)

func TestParseKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      string
		wantErr error
	}{
		{name: "single letter", in: "c"},
		{name: "word", in: "country"},
		{name: "underscored", in: "map_region"},
		{name: "digits after the first character", in: "osi_layer7"},
		{name: "at the length limit", in: strings.Repeat("k", annotation.MaxKeyLen)},

		{name: "empty", in: "", wantErr: annotation.ErrKeyShape},
		{name: "leading digit", in: "1country", wantErr: annotation.ErrKeyShape},
		{name: "leading underscore", in: "_country", wantErr: annotation.ErrKeyShape},
		{name: "uppercase", in: "Country", wantErr: annotation.ErrKeyShape},
		{name: "hyphenated", in: "country-code", wantErr: annotation.ErrKeyShape},
		{name: "dotted", in: "geo.country", wantErr: annotation.ErrKeyShape},
		{name: "spaced", in: "country code", wantErr: annotation.ErrKeyShape},
		{name: "trailing space", in: "country ", wantErr: annotation.ErrKeyShape},
		{name: "past the length limit", in: strings.Repeat("k", annotation.MaxKeyLen+1), wantErr: annotation.ErrKeyShape},
		{name: "non-ascii", in: "země", wantErr: annotation.ErrKeyShape},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := annotation.ParseKey(tt.in)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("ParseKey(%q) error = %v, want %v", tt.in, err, tt.wantErr)
			}
			if tt.wantErr == nil && string(got) != tt.in {
				t.Errorf("ParseKey(%q) = %q, want the key unchanged", tt.in, got)
			}
		})
	}
}

func TestClean(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		key     annotation.Key
		value   string
		want    string
		wantErr error
	}{
		// An unknown key is stored verbatim: this package does not improve a string whose meaning
		// it does not know.
		{name: "unknown key passes through", key: "difficulty", value: "  Hard  ", want: "  Hard  "},
		{name: "unknown key, unicode", key: "note", value: "šifra", want: "šifra"},
		{name: "at the value length limit", key: "note", value: strings.Repeat("v", annotation.MaxValueLen), want: strings.Repeat("v", annotation.MaxValueLen)},

		{name: "empty value", key: "note", value: "", wantErr: annotation.ErrValueEmpty},
		{name: "past the value length limit", key: "note", value: strings.Repeat("v", annotation.MaxValueLen+1), wantErr: annotation.ErrValueTooLong},
		// The cap counts characters, not bytes, matching the column's length() CHECK.
		{name: "multibyte counts as characters", key: "note", value: strings.Repeat("š", annotation.MaxValueLen), want: strings.Repeat("š", annotation.MaxValueLen)},

		{name: "country accepts an assigned code", key: annotation.KeyCountry, value: "CZ", want: "CZ"},
		{name: "country rejects lowercase", key: annotation.KeyCountry, value: "cz", wantErr: geo.ErrNotAlpha2},
		{name: "country rejects alpha-3", key: annotation.KeyCountry, value: "CZE", wantErr: geo.ErrNotAlpha2},
		{name: "country rejects an unassigned code", key: annotation.KeyCountry, value: "XX", wantErr: geo.ErrUnassigned},
		{name: "country rejects prose", key: annotation.KeyCountry, value: "Czechia", wantErr: geo.ErrNotAlpha2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := annotation.Clean(tt.key, tt.value)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Clean(%q, %q) error = %v, want %v", tt.key, tt.value, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("Clean(%q, %q) = %q, want %q", tt.key, tt.value, got, tt.want)
			}
		})
	}
}

// The point of the well-known registry is that it is the only thing that has to change to teach the
// product a new interpreted key, so what is in it is worth pinning.
func TestWellKnown(t *testing.T) {
	t.Parallel()

	if !annotation.WellKnown(annotation.KeyCountry) {
		t.Error("country is not registered as well-known, so nothing validates the globe's only input")
	}
	if annotation.WellKnown("difficulty") {
		t.Error("an unregistered key reported as well-known")
	}
}
