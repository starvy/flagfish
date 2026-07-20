package catalog

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ErrPageNotFound is returned when no page carries the requested route. It says nothing about draft
// state: the row is fetched whatever its flags, and the policy gate — not this read — decides whether
// a draft or auth-gated page is visible to this caller.
var ErrPageNotFound = errors.New("catalog: page not found")

// Page is a CMS page's full public read: the body plus the two flags the policy gate needs. Content
// is markdown, rendered client-side; this service never turns it into HTML.
type Page struct {
	Route        string
	Title        string
	Content      string
	Format       string
	Draft        bool
	AuthRequired bool
}

// PageLink is one published page in the nav: enough to render a link and mark the ones that will ask
// an anonymous visitor to log in.
type PageLink struct {
	Route        string
	Title        string
	AuthRequired bool
}

// PageByRoute reads a page by its slug, flags and all. The caller passes the result to policy.PageView
// to decide visibility — a draft is fetched here and hidden there, so the gate stays the one place
// that knows the rule.
func (s *Service) PageByRoute(ctx context.Context, route string) (Page, error) {
	p, err := s.q.GetPageByRoute(ctx, route)
	if errors.Is(err, pgx.ErrNoRows) {
		return Page{}, ErrPageNotFound
	} else if err != nil {
		return Page{}, fmt.Errorf("catalog: page %q: %w", route, err)
	}
	return Page{
		Route: p.Route, Title: p.Title, Content: p.Content, Format: p.Format,
		Draft: p.Draft, AuthRequired: p.AuthRequired,
	}, nil
}

// PublishedPages lists the published pages for the nav; drafts never appear.
func (s *Service) PublishedPages(ctx context.Context) ([]PageLink, error) {
	rows, err := s.q.ListPublishedPages(ctx)
	if err != nil {
		return nil, fmt.Errorf("catalog: list published pages: %w", err)
	}
	out := make([]PageLink, len(rows))
	for i, r := range rows {
		out[i] = PageLink{Route: r.Route, Title: r.Title, AuthRequired: r.AuthRequired}
	}
	return out, nil
}
