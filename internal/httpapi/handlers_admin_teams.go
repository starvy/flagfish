package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/starvy/flagfish/internal/accounts"
	"github.com/starvy/flagfish/internal/adminops"
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

type adminCreateTeamInput struct {
	Body struct {
		Name string `json:"name" minLength:"1" maxLength:"128"`
		// Password is the join password. Empty means the team has none and admits members on an
		// empty password — the same contract as the self-serve create.
		Password    string  `json:"password,omitempty" maxLength:"128" required:"false"`
		Email       *string `json:"email,omitempty" format:"email" maxLength:"255"`
		Website     *string `json:"website,omitempty" maxLength:"255"`
		Affiliation *string `json:"affiliation,omitempty" maxLength:"255"`
		Country     *string `json:"country,omitempty" maxLength:"64"`
	}
}

type adminUpdateTeamInput struct {
	ID   int64 `path:"id"`
	Body struct {
		Name *string `json:"name,omitempty" minLength:"1" maxLength:"128"`
		// Three-state: omit to keep, null to clear, a value to set. banned/hidden/captain_id are
		// deliberately absent — flipping those is a different decision with its own route.
		Email       Optional[string] `json:"email,omitempty" format:"email" maxLength:"255"`
		Website     Optional[string] `json:"website,omitempty" maxLength:"255"`
		Affiliation Optional[string] `json:"affiliation,omitempty" maxLength:"255"`
		Country     Optional[string] `json:"country,omitempty" maxLength:"64"`
	}
}

type adminTeamBanInput struct {
	ID   int64 `path:"id"`
	Body struct {
		Banned bool `json:"banned"`
	}
}

// Narrow like the user ban response: the row that changed, nothing a stale read could invent.
type adminTeamBanOutput struct {
	Body struct {
		ID     int64  `json:"id"`
		Name   string `json:"name"`
		Banned bool   `json:"banned"`
	}
}

type adminTeamHiddenInput struct {
	ID   int64 `path:"id"`
	Body struct {
		Hidden bool `json:"hidden"`
	}
}

type adminTeamHiddenOutput struct {
	Body struct {
		ID     int64  `json:"id"`
		Name   string `json:"name"`
		Hidden bool   `json:"hidden"`
	}
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

	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-create-team", Method: http.MethodPost, Path: "/teams",
		DefaultStatus: http.StatusCreated,
		Summary:       "Create a team (captainless until someone joins)", Tags: []string{"admin/teams"},
	}, s.adminCreateTeam)

	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-update-team", Method: http.MethodPatch, Path: "/teams/{id}",
		Summary: "Update a team's profile (partial)", Tags: []string{"admin/teams"},
	}, s.adminUpdateTeam)

	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-set-team-banned", Method: http.MethodPut, Path: "/teams/{id}/ban",
		Summary: "Ban or unban a team (a ban walls every member)", Tags: []string{"admin/teams"},
	}, s.adminSetTeamBanned)

	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-set-team-hidden", Method: http.MethodPut, Path: "/teams/{id}/hidden",
		Summary: "Hide or unhide a team on the public surfaces", Tags: []string{"admin/teams"},
	}, s.adminSetTeamHidden)
}

func (s *Server) adminCreateTeam(ctx context.Context, in *adminCreateTeamInput) (*adminTeamOutput, error) {
	b := in.Body
	var hash *string
	if b.Password != "" {
		h, err := accounts.Hash(b.Password)
		if err != nil {
			s.opts.Log.ErrorContext(ctx, "hashing a team password failed", "error", err)
			return nil, huma.Error500InternalServerError("could not create team")
		}
		hash = &h
	}
	t, err := s.opts.AdminOps.CreateTeam(ctx, s.adminActor(ctx), adminops.NewTeam{
		Name: b.Name, PasswordHash: hash, Email: b.Email,
		Website: b.Website, Affiliation: b.Affiliation, Country: b.Country,
	})
	if err != nil {
		return nil, s.adminOpsError(ctx, err, "create team")
	}
	return &adminTeamOutput{Body: adminTeamBody{
		ID: t.ID, Name: t.Name, Email: t.Email,
		Website: t.Website, Affiliation: t.Affiliation, Country: t.Country,
		BracketID: t.BracketID, CaptainID: t.CaptainID,
		Hidden: t.Hidden, Banned: t.Banned,
		MemberCount: 0, CreatedAt: t.CreatedAt.Time,
	}}, nil
}

func (s *Server) adminUpdateTeam(ctx context.Context, in *adminUpdateTeamInput) (*adminTeamOutput, error) {
	b := in.Body
	email, clearEmail := b.Email.split()
	website, clearWebsite := b.Website.split()
	affiliation, clearAffiliation := b.Affiliation.split()
	country, clearCountry := b.Country.split()

	t, err := s.opts.AdminOps.UpdateTeam(ctx, s.adminActor(ctx), in.ID, adminops.TeamPatch{
		Name: b.Name, Email: email, Website: website, Affiliation: affiliation, Country: country,
		ClearEmail: clearEmail, ClearWebsite: clearWebsite,
		ClearAffiliation: clearAffiliation, ClearCountry: clearCountry,
	})
	if err != nil {
		return nil, s.adminOpsError(ctx, err, "update team")
	}
	return s.adminGetTeam(ctx, &adminTeamIDInput{ID: t.ID})
}

func (s *Server) adminSetTeamBanned(ctx context.Context, in *adminTeamBanInput) (*adminTeamBanOutput, error) {
	row, err := s.opts.AdminOps.SetTeamBanned(ctx, s.adminActor(ctx), in.ID, in.Body.Banned)
	if err != nil {
		return nil, s.adminOpsError(ctx, err, "set team ban")
	}
	out := &adminTeamBanOutput{}
	out.Body.ID, out.Body.Name, out.Body.Banned = row.ID, row.Name, row.Banned
	return out, nil
}

func (s *Server) adminSetTeamHidden(ctx context.Context, in *adminTeamHiddenInput) (*adminTeamHiddenOutput, error) {
	row, err := s.opts.AdminOps.SetTeamHidden(ctx, s.adminActor(ctx), in.ID, in.Body.Hidden)
	if err != nil {
		return nil, s.adminOpsError(ctx, err, "set team hidden")
	}
	out := &adminTeamHiddenOutput{}
	out.Body.ID, out.Body.Name, out.Body.Hidden = row.ID, row.Name, row.Hidden
	return out, nil
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
	for i := range page.Teams {
		t := &page.Teams[i]
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
