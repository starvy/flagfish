package page_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/starvy/flagfish/internal/domain/page"
)

func TestValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		route      string
		title      string
		body       string
		format     string
		wantRoute  string
		wantTitle  string
		wantFormat string
		wantErr    error
	}{
		{"plain slug", "rules", "The Rules", "# hello", "markdown", "rules", "The Rules", "markdown", nil},
		{"hyphenated slug", "code-of-conduct", "CoC", "", "", "code-of-conduct", "CoC", "markdown", nil},
		{"digits allowed", "faq-2026", "FAQ", "", "markdown", "faq-2026", "FAQ", "markdown", nil},
		{"route and title trimmed", "  rules  ", "  The Rules  ", "", "", "rules", "The Rules", "markdown", nil},
		{"empty format defaults to markdown", "rules", "R", "", "", "rules", "R", "markdown", nil},

		{"empty route refused", "", "R", "", "", "", "", "", page.ErrEmptyRoute},
		{"whitespace route refused", "   ", "R", "", "", "", "", "", page.ErrEmptyRoute},
		{"route over cap refused", strings.Repeat("a", page.MaxRouteLen+1), "R", "", "", "", "", "", page.ErrRouteTooLong},
		{"uppercase route refused", "Rules", "R", "", "", "", "", "", page.ErrRouteInvalid},
		{"leading hyphen refused", "-rules", "R", "", "", "", "", "", page.ErrRouteInvalid},
		{"trailing hyphen refused", "rules-", "R", "", "", "", "", "", page.ErrRouteInvalid},
		{"doubled hyphen refused", "code--of", "R", "", "", "", "", "", page.ErrRouteInvalid},
		{"slash refused", "rules/eu", "R", "", "", "", "", "", page.ErrRouteInvalid},
		{"space refused", "the rules", "R", "", "", "", "", "", page.ErrRouteInvalid},

		{"empty title refused", "rules", "", "", "", "", "", "", page.ErrEmptyTitle},
		{"whitespace title refused", "rules", "   ", "", "", "", "", "", page.ErrEmptyTitle},
		{"title over cap refused", "rules", strings.Repeat("t", page.MaxTitleLen+1), "", "", "", "", "", page.ErrTitleTooLong},
		{"multibyte title counts runes", "rules", strings.Repeat("é", page.MaxTitleLen), "", "", "rules", strings.Repeat("é", page.MaxTitleLen), "markdown", nil},

		{"content over cap refused", "rules", "R", strings.Repeat("x", page.MaxContentBytes+1), "", "", "", "", page.ErrContentTooLong},
		{"content at cap accepted", "rules", "R", strings.Repeat("x", page.MaxContentBytes), "", "rules", "R", "markdown", nil},

		{"unknown format refused", "rules", "R", "", "html", "", "", "", page.ErrUnknownFormat},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := page.Validate(tc.route, tc.title, tc.body, tc.format)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr != nil {
				return
			}
			if got.Route != tc.wantRoute {
				t.Errorf("route = %q, want %q", got.Route, tc.wantRoute)
			}
			if got.Title != tc.wantTitle {
				t.Errorf("title = %q, want %q", got.Title, tc.wantTitle)
			}
			if got.Format != tc.wantFormat {
				t.Errorf("format = %q, want %q", got.Format, tc.wantFormat)
			}
			if got.Body != tc.body {
				t.Errorf("body = %q, want %q (verbatim)", got.Body, tc.body)
			}
		})
	}
}
