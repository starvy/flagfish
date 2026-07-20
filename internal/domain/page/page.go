// Package page holds the pure validation rules for a CMS page: the rules/FAQ/sponsors content an
// organiser publishes. It imports nothing outside the standard library — no SQL, no HTTP — so the
// rules here are total and testable on their own, and they are what a page must satisfy before it
// ever reaches the table.
package page

import (
	"errors"
	"strings"
	"unicode/utf8"
)

const (
	// MaxRouteLen bounds the slug. It is a URL path segment, not storage.
	MaxRouteLen = 64
	// MaxTitleLen bounds the display title.
	MaxTitleLen = 200
	// MaxContentBytes bounds the markdown body. Generous — a rules page is long — but finite, so a
	// single page cannot be used to stuff the row past what a client will render.
	MaxContentBytes = 262144 // 256 KiB

	// FormatMarkdown is the only stored format in v1. Markdown is rendered client-side; the server
	// never emits HTML, so it never sanitises author output.
	FormatMarkdown = "markdown"
)

var (
	// ErrEmptyRoute rejects a missing slug: a page with no route has no URL to live at.
	ErrEmptyRoute = errors.New("page: a route is required")
	// ErrRouteTooLong rejects an over-long slug.
	ErrRouteTooLong = errors.New("page: route is too long")
	// ErrRouteInvalid rejects a slug that is not a clean lowercase URL segment. The shape is fixed
	// here so a route can be dropped into a path without escaping, and so two slugs that differ only
	// in case can never collide with the case-sensitive UNIQUE index.
	ErrRouteInvalid = errors.New("page: route must be lowercase letters, digits and single hyphens")

	// ErrEmptyTitle rejects a missing title.
	ErrEmptyTitle = errors.New("page: a title is required")
	// ErrTitleTooLong rejects an over-long title.
	ErrTitleTooLong = errors.New("page: title is too long")

	// ErrContentTooLong rejects an over-large body.
	ErrContentTooLong = errors.New("page: content is too long")
	// ErrUnknownFormat rejects a format this version does not render.
	ErrUnknownFormat = errors.New("page: unknown format")
)

// Content is a validated page ready to persist: the trimmed route/title, the body verbatim, and the
// format. It is what Validate returns so a caller stores cleaned values rather than raw input.
type Content struct {
	Route  string
	Title  string
	Body   string
	Format string
}

// Validate checks a whole page (the create path) and returns the cleaned content to store. It is
// total: every rejection is one of the sentinels above. route and title are trimmed; body is stored
// verbatim (leading and trailing whitespace in markdown can be meaningful). An empty format defaults
// to markdown. The per-field validators below are the same checks, exposed for the partial update
// path where a field may be absent.
func Validate(route, title, body, format string) (Content, error) {
	r, err := ValidateRoute(route)
	if err != nil {
		return Content{}, err
	}
	t, err := ValidateTitle(title)
	if err != nil {
		return Content{}, err
	}
	b, err := ValidateBody(body)
	if err != nil {
		return Content{}, err
	}
	f, err := ValidateFormat(format)
	if err != nil {
		return Content{}, err
	}
	return Content{Route: r, Title: t, Body: b, Format: f}, nil
}

// ValidateRoute trims and checks a slug, returning the cleaned value to store.
func ValidateRoute(route string) (string, error) {
	r := strings.TrimSpace(route)
	if r == "" {
		return "", ErrEmptyRoute
	}
	if len(r) > MaxRouteLen {
		return "", ErrRouteTooLong
	}
	if !validRoute(r) {
		return "", ErrRouteInvalid
	}
	return r, nil
}

// ValidateTitle trims and checks a title, returning the cleaned value to store.
func ValidateTitle(title string) (string, error) {
	t := strings.TrimSpace(title)
	if t == "" {
		return "", ErrEmptyTitle
	}
	if utf8.RuneCountInString(t) > MaxTitleLen {
		return "", ErrTitleTooLong
	}
	return t, nil
}

// ValidateBody bounds the markdown body and returns it verbatim — the body is stored exactly as
// written, since leading and trailing whitespace can be meaningful in markdown.
func ValidateBody(body string) (string, error) {
	if len(body) > MaxContentBytes {
		return "", ErrContentTooLong
	}
	return body, nil
}

// ValidateFormat resolves and checks the format. An empty format defaults to markdown.
func ValidateFormat(format string) (string, error) {
	f := format
	if f == "" {
		f = FormatMarkdown
	}
	if f != FormatMarkdown {
		return "", ErrUnknownFormat
	}
	return f, nil
}

// validRoute is the slug grammar: one or more lowercase alphanumeric segments joined by single
// hyphens, no leading, trailing or doubled hyphen. Kept as an explicit scan rather than a regexp so
// the rule is obvious and allocates nothing.
func validRoute(s string) bool {
	prevHyphen := false
	for i, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			prevHyphen = false
		case c == '-':
			if i == 0 || prevHyphen {
				return false // no leading hyphen, no doubled hyphen
			}
			prevHyphen = true
		default:
			return false
		}
	}
	return !prevHyphen // no trailing hyphen
}
