package geo_test

import (
	"errors"
	"testing"

	"github.com/starvy/flagfish/internal/domain/geo"
)

func TestParseAlpha2(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      string
		want    string
		wantErr error
	}{
		{name: "assigned code", in: "CZ", want: "CZ"},
		{name: "assigned code, last alphabetically", in: "ZW", want: "ZW"},
		{name: "recently assigned", in: "SS", want: "SS"}, // South Sudan, 2011
		{name: "territory with its own code", in: "AX", want: "AX"},

		// Lowercase is a rejection, not something to repair: the stored form has to be one thing
		// for a renderer to key on.
		{name: "lowercase", in: "cz", wantErr: geo.ErrNotAlpha2},
		{name: "mixed case", in: "Cz", wantErr: geo.ErrNotAlpha2},
		{name: "alpha-3", in: "CZE", wantErr: geo.ErrNotAlpha2},
		{name: "one letter", in: "C", wantErr: geo.ErrNotAlpha2},
		{name: "empty", in: "", wantErr: geo.ErrNotAlpha2},
		{name: "digits", in: "12", wantErr: geo.ErrNotAlpha2},
		{name: "padded", in: "CZ ", wantErr: geo.ErrNotAlpha2},
		{name: "non-ascii", in: "ČZ", wantErr: geo.ErrNotAlpha2},

		// Well shaped but not a country: the two failures are distinguishable on purpose, because
		// they call for different fixes from whoever typed them.
		{name: "unassigned", in: "XX", wantErr: geo.ErrUnassigned},
		{name: "user-assigned range", in: "QM", wantErr: geo.ErrUnassigned},
		{name: "withdrawn code", in: "SU", wantErr: geo.ErrUnassigned}, // Soviet Union
		{name: "common mistake for GB", in: "UK", wantErr: geo.ErrUnassigned},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := geo.ParseAlpha2(tt.in)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("ParseAlpha2(%q) error = %v, want %v", tt.in, err, tt.wantErr)
			}
			if string(got) != tt.want {
				t.Errorf("ParseAlpha2(%q) = %q, want %q", tt.in, got, tt.want)
			}
			if valid := geo.ValidAlpha2(tt.in); valid != (tt.wantErr == nil) {
				t.Errorf("ValidAlpha2(%q) = %v, but ParseAlpha2 disagrees", tt.in, valid)
			}
		})
	}
}

// The list is data, and data edited by hand loses a row eventually. A dropped country is invisible
// until an author's challenge will not save, so the count is pinned here.
func TestAssignedCodeCount(t *testing.T) {
	t.Parallel()

	const want = 249
	if got := geo.Count(); got != want {
		t.Errorf("Count() = %d, want %d — a code was added or lost; update this number deliberately", got, want)
	}
}
