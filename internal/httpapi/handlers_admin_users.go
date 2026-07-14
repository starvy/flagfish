package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/starvy/flagfish/internal/domain/policy"
)

type adminUserBody struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Email     string    `json:"email"`
	Role      string    `json:"role"`
	Verified  bool      `json:"verified"`
	Banned    bool      `json:"banned"`
	Hidden    bool      `json:"hidden"`
	TeamID    *int64    `json:"team_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type adminListUsersInput struct {
	Page    int `query:"page" minimum:"1" maximum:"1000000" default:"1"`
	PerPage int `query:"per_page" minimum:"1" maximum:"100" default:"50"`
}

type adminListUsersOutput struct {
	Body struct {
		Users   []adminUserBody `json:"users"`
		Total   int64           `json:"total"`
		Page    int             `json:"page"`
		PerPage int             `json:"per_page"`
	}
}

type adminBanInput struct {
	ID   int64 `path:"id"`
	Body struct {
		Banned bool `json:"banned"`
	}
}

type adminRoleInput struct {
	ID   int64 `path:"id"`
	Body struct {
		Role string `json:"role" enum:"user,admin"`
	}
}

// The ban and role responses are deliberately narrow: the mutation returns the row it touched, not
// the full user, so a reader sees exactly what changed and nothing that a stale read would invent.
type adminBanOutput struct {
	Body struct {
		ID     int64  `json:"id"`
		Name   string `json:"name"`
		Banned bool   `json:"banned"`
	}
}

type adminRoleOutput struct {
	Body struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
		Role string `json:"role"`
	}
}

func (s *Server) registerAdminUsers() {
	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-list-users", Method: http.MethodGet, Path: "/users",
		Summary: "List users (paginated)", Tags: []string{"admin/users"},
	}, s.adminListUsers)

	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-set-user-banned", Method: http.MethodPut, Path: "/users/{id}/ban",
		Summary: "Ban or unban a user", Tags: []string{"admin/users"},
	}, s.adminSetUserBanned)

	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-set-user-role", Method: http.MethodPut, Path: "/users/{id}/role",
		Summary: "Promote or demote a user", Tags: []string{"admin/users"},
	}, s.adminSetUserRole)
}

func (s *Server) adminListUsers(ctx context.Context, in *adminListUsersInput) (*adminListUsersOutput, error) {
	page, err := s.opts.AdminOps.ListUsers(ctx, in.Page, in.PerPage)
	if err != nil {
		return nil, s.adminOpsError(ctx, err, "list users")
	}
	out := &adminListUsersOutput{}
	out.Body.Total = page.Total
	out.Body.Page = in.Page
	out.Body.PerPage = in.PerPage
	out.Body.Users = make([]adminUserBody, len(page.Users))
	for i, u := range page.Users {
		out.Body.Users[i] = adminUserBody{
			ID: u.ID, Name: u.Name, Email: u.Email, Role: u.Role,
			Verified: u.Verified, Banned: u.Banned, Hidden: u.Hidden,
			TeamID: u.TeamID, CreatedAt: u.CreatedAt.Time,
		}
	}
	return out, nil
}

func (s *Server) adminSetUserBanned(ctx context.Context, in *adminBanInput) (*adminBanOutput, error) {
	row, err := s.opts.AdminOps.SetBanned(ctx, s.adminActor(ctx), in.ID, in.Body.Banned)
	if err != nil {
		return nil, s.adminOpsError(ctx, err, "set user ban")
	}
	out := &adminBanOutput{}
	out.Body.ID, out.Body.Name, out.Body.Banned = row.ID, row.Name, row.Banned
	return out, nil
}

func (s *Server) adminSetUserRole(ctx context.Context, in *adminRoleInput) (*adminRoleOutput, error) {
	row, err := s.opts.AdminOps.SetRole(ctx, s.adminActor(ctx), in.ID, in.Body.Role)
	if err != nil {
		return nil, s.adminOpsError(ctx, err, "set user role")
	}
	out := &adminRoleOutput{}
	out.Body.ID, out.Body.Name, out.Body.Role = row.ID, row.Name, row.Role
	return out, nil
}
