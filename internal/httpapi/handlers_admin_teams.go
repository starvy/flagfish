package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/starvy/flagfish/internal/domain/policy"
)

type adminTeamBody struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Email       *string   `json:"email,omitempty"`
	Website     *string   `json:"website,omitempty"`
	Affiliation *string   `json:"affiliation,omitempty"`
	Country     *string   `json:"country,omitempty"`
	BracketID   *int64    `json:"bracket_id,omitempty"`
	CaptainID   *int64    `json:"captain_id,omitempty"`
	Hidden      bool      `json:"hidden"`
	Banned      bool      `json:"banned"`
	MemberCount int64     `json:"member_count"`
	CreatedAt   time.Time `json:"created_at"`
}

type adminTeamOutput struct {
	Body adminTeamBody
}

type adminListTeamsInput struct {
	Page    int `query:"page" minimum:"1" maximum:"1000000" default:"1"`
	PerPage int `query:"per_page" minimum:"1" maximum:"100" default:"50"`
	// One optional search: q is the needle, field says which column it runs against.
	// email is admin-only data, which is why it is searchable here and nowhere public.
	Q     string `query:"q" required:"false" maxLength:"256"`
	Field string `query:"field" required:"false" enum:"name,email,website,affiliation,country" default:"name"`
}

type adminListTeamsOutput struct {
	Body struct {
		Teams   []adminTeamBody `json:"teams"`
		Total   int64           `json:"total"`
		Page    int             `json:"page"`
		PerPage int             `json:"per_page"`
	}
}

type adminTeamIDInput struct {
	ID int64 `path:"id"`
}

func (s *Server) registerAdminTeams() {
	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-list-teams", Method: http.MethodGet, Path: "/teams",
		Summary: "List teams (paginated, searchable)", Tags: []string{"admin/teams"},
	}, s.adminListTeams)

	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-get-team", Method: http.MethodGet, Path: "/teams/{id}",
		Summary: "Get one team", Tags: []string{"admin/teams"},
	}, s.adminGetTeam)
}

func (s *Server) adminListTeams(ctx context.Context, in *adminListTeamsInput) (*adminListTeamsOutput, error) {
	page, err := s.opts.AdminOps.ListTeams(ctx, in.Page, in.PerPage, in.Q, in.Field)
	if err != nil {
		return nil, s.adminOpsError(ctx, err, "list teams")
	}
	out := &adminListTeamsOutput{}
	out.Body.Total = page.Total
	out.Body.Page = in.Page
	out.Body.PerPage = in.PerPage
	out.Body.Teams = make([]adminTeamBody, len(page.Teams))
	for i, t := range page.Teams {
		out.Body.Teams[i] = adminTeamBody{
			ID: t.ID, Name: t.Name, Email: t.Email,
			Website: t.Website, Affiliation: t.Affiliation, Country: t.Country,
			BracketID: t.BracketID, CaptainID: t.CaptainID,
			Hidden: t.Hidden, Banned: t.Banned,
			MemberCount: t.MemberCount, CreatedAt: t.CreatedAt.Time,
		}
	}
	return out, nil
}

func (s *Server) adminGetTeam(ctx context.Context, in *adminTeamIDInput) (*adminTeamOutput, error) {
	t, err := s.opts.AdminOps.GetTeam(ctx, in.ID)
	if err != nil {
		return nil, s.adminOpsError(ctx, err, "get team")
	}
	return &adminTeamOutput{Body: adminTeamBody{
		ID: t.ID, Name: t.Name, Email: t.Email,
		Website: t.Website, Affiliation: t.Affiliation, Country: t.Country,
		BracketID: t.BracketID, CaptainID: t.CaptainID,
		Hidden: t.Hidden, Banned: t.Banned,
		MemberCount: t.MemberCount, CreatedAt: t.CreatedAt.Time,
	}}, nil
}
