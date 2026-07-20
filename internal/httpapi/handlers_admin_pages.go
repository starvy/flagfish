package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/starvy/flagfish/internal/adminops"
	"github.com/starvy/flagfish/internal/db"
	"github.com/starvy/flagfish/internal/domain/page"
	"github.com/starvy/flagfish/internal/domain/policy"
)

// The admin CMS surface: full CRUD over pages, drafts included. The public surface (handlers_pages.go)
// only ever sees published, gate-passing pages; this is where they are written and where a draft is
// visible before it is published.

// adminPageBody is one page with its full body — the create/update echo and the editor read.
type adminPageBody struct {
	ID           int64     `json:"id"`
	Route        string    `json:"route"`
	Title        string    `json:"title"`
	Content      string    `json:"content"`
	Format       string    `json:"format"`
	Draft        bool      `json:"draft"`
	AuthRequired bool      `json:"auth_required"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// adminPageListItem is one row of the admin index: no body, since the list does not render it.
type adminPageListItem struct {
	ID           int64     `json:"id"`
	Route        string    `json:"route"`
	Title        string    `json:"title"`
	Format       string    `json:"format"`
	Draft        bool      `json:"draft"`
	AuthRequired bool      `json:"auth_required"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type adminListPagesOutput struct {
	Body struct {
		Pages []adminPageListItem `json:"pages"`
	}
}

type adminPageOutput struct {
	Body adminPageBody
}

type adminPageIDInput struct {
	ID int64 `path:"id"`
}

type adminCreatePageInput struct {
	Body struct {
		Route   string `json:"route" minLength:"1" maxLength:"64"`
		Title   string `json:"title" minLength:"1" maxLength:"200"`
		Content string `json:"content" maxLength:"262144"`
		Format  string `json:"format,omitempty" enum:"markdown"`
		// Draft defaults to true when omitted: publishing is the deliberate act, so a page created
		// without saying so lands unpublished rather than live. auth_required defaults to false.
		Draft        *bool `json:"draft,omitempty"`
		AuthRequired *bool `json:"auth_required,omitempty"`
	}
}

// adminUpdatePageInput is a partial update: a nil field is left untouched, mirroring the COALESCE
// query. Pointers, not zero values, so "clear the body to empty" and "do not touch the body" are
// distinguishable — a bool draft flag cannot be toggled otherwise.
type adminUpdatePageInput struct {
	ID   int64 `path:"id"`
	Body struct {
		Route        *string `json:"route,omitempty" minLength:"1" maxLength:"64"`
		Title        *string `json:"title,omitempty" minLength:"1" maxLength:"200"`
		Content      *string `json:"content,omitempty" maxLength:"262144"`
		Format       *string `json:"format,omitempty" enum:"markdown"`
		Draft        *bool   `json:"draft,omitempty"`
		AuthRequired *bool   `json:"auth_required,omitempty"`
	}
}

func (s *Server) registerAdminPages() {
	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-list-pages", Method: http.MethodGet, Path: "/pages",
		Summary: "List every page, drafts included", Tags: []string{"admin/pages"},
	}, s.adminListPages)

	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-get-page", Method: http.MethodGet, Path: "/pages/{id}",
		Summary: "Get one page with its full body", Tags: []string{"admin/pages"},
	}, s.adminGetPage)

	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-create-page", Method: http.MethodPost, Path: "/pages",
		DefaultStatus: http.StatusCreated,
		Summary:       "Create a page", Tags: []string{"admin/pages"},
	}, s.adminCreatePage)

	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-update-page", Method: http.MethodPatch, Path: "/pages/{id}",
		Summary: "Update a page", Tags: []string{"admin/pages"},
	}, s.adminUpdatePage)

	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-delete-page", Method: http.MethodDelete, Path: "/pages/{id}",
		DefaultStatus: http.StatusNoContent,
		Summary:       "Delete a page", Tags: []string{"admin/pages"},
	}, s.adminDeletePage)
}

func pageBody(p db.Page) adminPageBody {
	return adminPageBody{
		ID: p.ID, Route: p.Route, Title: p.Title, Content: p.Content, Format: p.Format,
		Draft: p.Draft, AuthRequired: p.AuthRequired,
		CreatedAt: p.CreatedAt.Time, UpdatedAt: p.UpdatedAt.Time,
	}
}

func (s *Server) adminListPages(ctx context.Context, _ *struct{}) (*adminListPagesOutput, error) {
	rows, err := s.opts.AdminOps.ListPages(ctx)
	if err != nil {
		return nil, s.adminOpsError(ctx, err, "list pages")
	}
	out := &adminListPagesOutput{}
	out.Body.Pages = make([]adminPageListItem, len(rows))
	for i, r := range rows {
		out.Body.Pages[i] = adminPageListItem{
			ID: r.ID, Route: r.Route, Title: r.Title, Format: r.Format,
			Draft: r.Draft, AuthRequired: r.AuthRequired,
			CreatedAt: r.CreatedAt.Time, UpdatedAt: r.UpdatedAt.Time,
		}
	}
	return out, nil
}

func (s *Server) adminGetPage(ctx context.Context, in *adminPageIDInput) (*adminPageOutput, error) {
	p, err := s.opts.AdminOps.GetPage(ctx, in.ID)
	if err != nil {
		return nil, s.adminOpsError(ctx, err, "get page")
	}
	return &adminPageOutput{Body: pageBody(p)}, nil
}

func (s *Server) adminCreatePage(ctx context.Context, in *adminCreatePageInput) (*adminPageOutput, error) {
	draft := true
	if in.Body.Draft != nil {
		draft = *in.Body.Draft
	}
	authRequired := in.Body.AuthRequired != nil && *in.Body.AuthRequired
	p, err := s.opts.AdminOps.CreatePage(ctx, s.adminActor(ctx), adminops.PageInput{
		Route: in.Body.Route, Title: in.Body.Title, Content: in.Body.Content,
		Format: in.Body.Format, Draft: draft, AuthRequired: authRequired,
	})
	if err != nil {
		return nil, s.adminOpsError(ctx, err, "create page")
	}
	return &adminPageOutput{Body: pageBody(p)}, nil
}

func (s *Server) adminUpdatePage(ctx context.Context, in *adminUpdatePageInput) (*adminPageOutput, error) {
	p, err := s.opts.AdminOps.UpdatePage(ctx, s.adminActor(ctx), in.ID, adminops.PagePatch{
		Route: in.Body.Route, Title: in.Body.Title, Content: in.Body.Content,
		Format: in.Body.Format, Draft: in.Body.Draft, AuthRequired: in.Body.AuthRequired,
	})
	if err != nil {
		return nil, s.adminOpsError(ctx, err, "update page")
	}
	return &adminPageOutput{Body: pageBody(p)}, nil
}

func (s *Server) adminDeletePage(ctx context.Context, in *adminPageIDInput) (*adminDeleteOutput, error) {
	if err := s.opts.AdminOps.DeletePage(ctx, s.adminActor(ctx), in.ID); err != nil {
		return nil, s.adminOpsError(ctx, err, "delete page")
	}
	return &adminDeleteOutput{}, nil
}

// Keep the wire caps and the domain caps one number, so a drift is a compile error, not a
// runtime surprise where the API accepts what the validator will reject.
var (
	_ = [1]struct{}{}[page.MaxRouteLen-64]
	_ = [1]struct{}{}[page.MaxTitleLen-200]
	_ = [1]struct{}{}[page.MaxContentBytes-262144]
)
