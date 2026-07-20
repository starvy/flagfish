package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/starvy/flagfish/internal/catalog"
	"github.com/starvy/flagfish/internal/domain/policy"
)

// The public CMS surface: a list of published pages for the nav, and one page by its slug. The body
// is markdown the client renders — the server never emits HTML. Draft and auth_required are enforced
// here, per row, by policy.PageView; the route class only gets a caller this far.

type pageLink struct {
	Route        string `json:"route"`
	Title        string `json:"title"`
	AuthRequired bool   `json:"auth_required"`
}

type listPagesOutput struct {
	Body struct {
		Pages []pageLink `json:"pages"`
	}
}

type pageRouteInput struct {
	Route string `path:"route" maxLength:"64"`
}

type pageOutput struct {
	Body struct {
		Route   string `json:"route"`
		Title   string `json:"title"`
		Content string `json:"content"`
		Format  string `json:"format"`
	}
}

func (s *Server) registerPages() {
	Register(s.Public, policy.ClassPages, huma.Operation{
		OperationID: "list-pages", Method: http.MethodGet, Path: "/pages",
		Summary: "List published content pages", Tags: []string{"pages"},
	}, s.listPages)

	Register(s.Public, policy.ClassPages, huma.Operation{
		OperationID: "get-page", Method: http.MethodGet, Path: "/pages/{route}",
		Summary: "Get one published content page by its route", Tags: []string{"pages"},
	}, s.getPage)
}

func (s *Server) listPages(ctx context.Context, _ *struct{}) (*listPagesOutput, error) {
	rows, err := s.opts.Catalog.PublishedPages(ctx)
	if err != nil {
		s.opts.Log.ErrorContext(ctx, "list pages failed", "error", err)
		return nil, huma.Error500InternalServerError("could not load pages")
	}
	out := &listPagesOutput{}
	out.Body.Pages = make([]pageLink, len(rows))
	for i, r := range rows {
		out.Body.Pages[i] = pageLink{Route: r.Route, Title: r.Title, AuthRequired: r.AuthRequired}
	}
	return out, nil
}

func (s *Server) getPage(ctx context.Context, in *pageRouteInput) (*pageOutput, error) {
	p, err := s.opts.Catalog.PageByRoute(ctx, in.Route)
	switch {
	case errors.Is(err, catalog.ErrPageNotFound):
		return nil, huma.Error404NotFound("page not found")
	case err != nil:
		s.opts.Log.ErrorContext(ctx, "page detail failed", "route", in.Route, "error", err)
		return nil, huma.Error500InternalServerError("could not load page")
	}

	// The one place draft/auth_required are decided. A draft reads as absent (404), an auth-gated
	// page refuses an anonymous caller (403) — same decision the SPA route redirects on.
	if out := policy.PageView(p.Draft, p.AuthRequired, AuthOf(ctx).Principal); out.Denied() {
		if out.Status == http.StatusNotFound {
			return nil, huma.Error404NotFound("page not found")
		}
		return nil, huma.Error403Forbidden(out.Reason.String())
	}

	out := &pageOutput{}
	out.Body.Route = p.Route
	out.Body.Title = p.Title
	out.Body.Content = p.Content
	out.Body.Format = p.Format
	return out, nil
}
