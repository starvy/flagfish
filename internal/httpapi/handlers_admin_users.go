package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/starvy/flagfish/internal/adminops"
	"github.com/starvy/flagfish/internal/domain/policy"
)

type adminUserBody struct {
	ID                 int64     `json:"id"`
	Name               string    `json:"name"`
	Email              string    `json:"email"`
	Role               string    `json:"role"`
	Verified           bool      `json:"verified"`
	Banned             bool      `json:"banned"`
	Hidden             bool      `json:"hidden"`
	TeamID             *int64    `json:"team_id,omitempty"`
	Website            *string   `json:"website,omitempty"`
	Affiliation        *string   `json:"affiliation,omitempty"`
	Country            *string   `json:"country,omitempty"`
	MustChangePassword bool      `json:"must_change_password"`
	CreatedAt          time.Time `json:"created_at"`
}

type adminListUsersInput struct {
	Page    int `query:"page" minimum:"1" maximum:"1000000" default:"1"`
	PerPage int `query:"per_page" minimum:"1" maximum:"100" default:"50"`
	// One optional search: q is the needle, field the column it runs against. email is
	// admin-only data, which is why it is searchable here and nowhere public.
	Q     string `query:"q" required:"false" maxLength:"256"`
	Field string `query:"field" required:"false" enum:"name,email,website,affiliation,country" default:"name"`
}

type adminUserIDInput struct {
	ID int64 `path:"id"`
}

type adminUserOutput struct {
	Body adminUserBody
}

type adminUpdateUserInput struct {
	ID   int64 `path:"id"`
	Body struct {
		Name *string `json:"name,omitempty" minLength:"1" maxLength:"128"`
		// Three-state: omit to keep, null to clear, a value to set. email, role, banned, hidden
		// and team_id are deliberately absent — each moves through its own route or not at all.
		Website     Optional[string] `json:"website,omitempty" maxLength:"255"`
		Affiliation Optional[string] `json:"affiliation,omitempty" maxLength:"255"`
		Country     Optional[string] `json:"country,omitempty" maxLength:"64"`
	}
}

type adminUserHiddenInput struct {
	ID   int64 `path:"id"`
	Body struct {
		Hidden bool `json:"hidden"`
	}
}

type adminUserHiddenOutput struct {
	Body struct {
		ID     int64  `json:"id"`
		Name   string `json:"name"`
		Hidden bool   `json:"hidden"`
	}
}

type adminForcePasswordChangeOutput struct {
	Body struct {
		ID                 int64  `json:"id"`
		Name               string `json:"name"`
		MustChangePassword bool   `json:"must_change_password"`
	}
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
		OperationID: "admin-get-user", Method: http.MethodGet, Path: "/users/{id}",
		Summary: "Get one user", Tags: []string{"admin/users"},
	}, s.adminGetUser)

	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-update-user", Method: http.MethodPatch, Path: "/users/{id}",
		Summary: "Update a user's profile (partial)", Tags: []string{"admin/users"},
	}, s.adminUpdateUser)

	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-set-user-hidden", Method: http.MethodPut, Path: "/users/{id}/hidden",
		Summary: "Hide or unhide a user on the public surfaces", Tags: []string{"admin/users"},
	}, s.adminSetUserHidden)

	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-set-user-banned", Method: http.MethodPut, Path: "/users/{id}/ban",
		Summary: "Ban or unban a user", Tags: []string{"admin/users"},
	}, s.adminSetUserBanned)

	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-force-password-change", Method: http.MethodPut, Path: "/users/{id}/force-password-change",
		Summary: "Force a user to choose a new password (kills their sessions)", Tags: []string{"admin/users"},
	}, s.adminForcePasswordChange)

	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-set-user-role", Method: http.MethodPut, Path: "/users/{id}/role",
		Summary: "Promote or demote a user", Tags: []string{"admin/users"},
	}, s.adminSetUserRole)
}

func (s *Server) adminListUsers(ctx context.Context, in *adminListUsersInput) (*adminListUsersOutput, error) {
	page, err := s.opts.AdminOps.ListUsers(ctx, in.Page, in.PerPage, in.Q, in.Field)
	if err != nil {
		return nil, s.adminOpsError(ctx, err, "list users")
	}
	out := &adminListUsersOutput{}
	out.Body.Total = page.Total
	out.Body.Page = in.Page
	out.Body.PerPage = in.PerPage
	out.Body.Users = make([]adminUserBody, len(page.Users))
	for i := range page.Users {
		u := &page.Users[i]
		out.Body.Users[i] = adminUserBody{
			ID: u.ID, Name: u.Name, Email: u.Email, Role: u.Role,
			Verified: u.Verified, Banned: u.Banned, Hidden: u.Hidden,
			TeamID: u.TeamID, Website: u.Website, Affiliation: u.Affiliation,
			Country: u.Country, MustChangePassword: u.MustChangePassword,
			CreatedAt: u.CreatedAt.Time,
		}
	}
	return out, nil
}

func (s *Server) adminGetUser(ctx context.Context, in *adminUserIDInput) (*adminUserOutput, error) {
	u, err := s.opts.AdminOps.GetUser(ctx, in.ID)
	if err != nil {
		return nil, s.adminOpsError(ctx, err, "get user")
	}
	return &adminUserOutput{Body: adminUserBody{
		ID: u.ID, Name: u.Name, Email: u.Email, Role: u.Role,
		Verified: u.Verified, Banned: u.Banned, Hidden: u.Hidden,
		TeamID: u.TeamID, Website: u.Website, Affiliation: u.Affiliation,
		Country: u.Country, MustChangePassword: u.MustChangePassword,
		CreatedAt: u.CreatedAt.Time,
	}}, nil
}

func (s *Server) adminUpdateUser(ctx context.Context, in *adminUpdateUserInput) (*adminUserOutput, error) {
	b := in.Body
	website, clearWebsite := b.Website.split()
	affiliation, clearAffiliation := b.Affiliation.split()
	country, clearCountry := b.Country.split()

	u, err := s.opts.AdminOps.UpdateUser(ctx, s.adminActor(ctx), in.ID, adminops.UserPatch{
		Name: b.Name, Website: website, Affiliation: affiliation, Country: country,
		ClearWebsite: clearWebsite, ClearAffiliation: clearAffiliation, ClearCountry: clearCountry,
	})
	if err != nil {
		return nil, s.adminOpsError(ctx, err, "update user")
	}
	return &adminUserOutput{Body: adminUserBody{
		ID: u.ID, Name: u.Name, Email: u.Email, Role: u.Role,
		Verified: u.Verified, Banned: u.Banned, Hidden: u.Hidden,
		TeamID: u.TeamID, Website: u.Website, Affiliation: u.Affiliation,
		Country: u.Country, MustChangePassword: u.MustChangePassword,
		CreatedAt: u.CreatedAt.Time,
	}}, nil
}

func (s *Server) adminSetUserHidden(ctx context.Context, in *adminUserHiddenInput) (*adminUserHiddenOutput, error) {
	row, err := s.opts.AdminOps.SetUserHidden(ctx, s.adminActor(ctx), in.ID, in.Body.Hidden)
	if err != nil {
		return nil, s.adminOpsError(ctx, err, "set user hidden")
	}
	out := &adminUserHiddenOutput{}
	out.Body.ID, out.Body.Name, out.Body.Hidden = row.ID, row.Name, row.Hidden
	return out, nil
}

func (s *Server) adminForcePasswordChange(ctx context.Context, in *adminUserIDInput) (*adminForcePasswordChangeOutput, error) {
	row, err := s.opts.AdminOps.ForcePasswordChange(ctx, s.adminActor(ctx), in.ID)
	if err != nil {
		return nil, s.adminOpsError(ctx, err, "force password change")
	}
	out := &adminForcePasswordChangeOutput{}
	out.Body.ID, out.Body.Name, out.Body.MustChangePassword = row.ID, row.Name, row.MustChangePassword
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
