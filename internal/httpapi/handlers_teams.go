package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/starvy/flagfish/internal/accounts"
	"github.com/starvy/flagfish/internal/domain/policy"
)

type createTeamInput struct {
	Body struct {
		Name string `json:"name" minLength:"1" maxLength:"128"`
		// Password is the join password. Empty means the team has none and admits
		// members on an empty password.
		Password string `json:"password,omitempty" maxLength:"128" required:"false"`
	}
}

type joinTeamInput struct {
	Body struct {
		Name     string `json:"name" minLength:"1" maxLength:"128"`
		Password string `json:"password,omitempty" maxLength:"128" required:"false"`
	}
}

type teamIDInput struct {
	ID int64 `path:"id"`
}

type teamMember struct {
	UserID     int64  `json:"user_id"`
	Name       string `json:"name"`
	Captain    bool   `json:"captain"`
	SolveCount int64  `json:"solve_count"`
	Points     int64  `json:"points"`
}

// teamBody is flat on purpose: Huma does not promote anonymously embedded struct fields, so a
// composed body would silently drop everything but is_captain over the wire. is_captain is omitted
// on the public profile, where it is always false.
type teamBody struct {
	ID          int64        `json:"id"`
	Name        string       `json:"name"`
	Website     *string      `json:"website,omitempty"`
	Affiliation *string      `json:"affiliation,omitempty"`
	Country     *string      `json:"country,omitempty"`
	Score       int64        `json:"score"`
	CreatedAt   time.Time    `json:"created_at"`
	IsCaptain   bool         `json:"is_captain,omitempty"`
	Members     []teamMember `json:"members"`
}

type teamOutput struct {
	Body teamBody
}

type leftTeamOutput struct {
	Body struct {
		OK bool `json:"ok"`
	}
}

func (s *Server) registerTeams() {
	Register(s.Public, policy.ClassTeamCreate, huma.Operation{
		OperationID: "create-team", Method: http.MethodPost, Path: "/teams",
		Summary: "Create a team and become its captain", Tags: []string{"teams"},
	}, s.createTeam)

	Register(s.Public, policy.ClassTeamEnrollment, huma.Operation{
		OperationID: "join-team", Method: http.MethodPost, Path: "/teams/join",
		Summary: "Join a team by name and password", Tags: []string{"teams"},
	}, s.joinTeam)

	Register(s.Public, policy.ClassTeamDetail, huma.Operation{
		OperationID: "team-detail", Method: http.MethodGet, Path: "/teams/{id}",
		Summary: "A team's public profile", Tags: []string{"teams"},
	}, s.teamDetail)

	Register(s.Public, policy.ClassTeamEnrollment, huma.Operation{
		OperationID: "my-team", Method: http.MethodGet, Path: "/me/team",
		Summary: "The caller's team", Tags: []string{"teams"},
	}, s.myTeam)

	Register(s.Public, policy.ClassTeamEnrollment, huma.Operation{
		OperationID: "leave-team", Method: http.MethodPost, Path: "/me/team/leave",
		Summary: "Leave the caller's team", Tags: []string{"teams"},
	}, s.leaveTeam)
}

func teamBodyOf(t accounts.Team) teamBody {
	members := make([]teamMember, 0, len(t.Members))
	for _, m := range t.Members {
		members = append(members, teamMember{
			UserID: m.UserID, Name: m.Name, Captain: m.Captain,
			SolveCount: m.SolveCount, Points: m.Points,
		})
	}
	return teamBody{
		ID: t.ID, Name: t.Name,
		Website: t.Website, Affiliation: t.Affiliation, Country: t.Country,
		Score: t.Score, CreatedAt: t.CreatedAt, IsCaptain: t.IsCaptain, Members: members,
	}
}

func (s *Server) createTeam(ctx context.Context, in *createTeamInput) (*teamOutput, error) {
	pr := AuthOf(ctx).Principal
	t, err := s.opts.Accounts.CreateTeam(ctx, pr.UserID, in.Body.Name, in.Body.Password)
	switch {
	case errors.Is(err, accounts.ErrTeamNameTaken):
		return nil, huma.Error409Conflict("that team name is already taken")
	case errors.Is(err, accounts.ErrTeamCapReached):
		return nil, huma.Error403Forbidden("team registration is full")
	case errors.Is(err, accounts.ErrAlreadyOnTeam):
		return nil, huma.Error403Forbidden("you are already on a team")
	case errors.Is(err, accounts.ErrTeamFull):
		return nil, huma.Error403Forbidden("the team is full")
	case err != nil:
		s.opts.Log.ErrorContext(ctx, "create team failed", "error", err)
		return nil, huma.Error500InternalServerError("could not create the team")
	}
	return &teamOutput{Body: teamBodyOf(t)}, nil
}

func (s *Server) joinTeam(ctx context.Context, in *joinTeamInput) (*teamOutput, error) {
	pr := AuthOf(ctx).Principal
	t, err := s.opts.Accounts.JoinTeam(ctx, pr.UserID, in.Body.Name, in.Body.Password)
	switch {
	case errors.Is(err, accounts.ErrTeamJoinDenied):
		// One answer for "no such team" and "wrong password".
		return nil, huma.Error403Forbidden("that information is incorrect")
	case errors.Is(err, accounts.ErrTeamFull):
		return nil, huma.Error403Forbidden("the team is full")
	case errors.Is(err, accounts.ErrAlreadyOnTeam):
		return nil, huma.Error403Forbidden("you are already on a team")
	case err != nil:
		s.opts.Log.ErrorContext(ctx, "join team failed", "error", err)
		return nil, huma.Error500InternalServerError("could not join the team")
	}
	return &teamOutput{Body: teamBodyOf(t)}, nil
}

// teamDetail is a public scoreboard row with a roster attached, so it clamps to the freeze exactly
// as the board does. myTeam below does not: an account's own live score is the deliberate exception.
func (s *Server) teamDetail(ctx context.Context, in *teamIDInput) (*teamOutput, error) {
	t, err := s.opts.Accounts.TeamProfile(ctx, in.ID, freezeCutoff(PolicyOf(ctx)))
	switch {
	case errors.Is(err, accounts.ErrTeamNotFound):
		return nil, huma.Error404NotFound("team not found")
	case err != nil:
		s.opts.Log.ErrorContext(ctx, "team profile failed", "error", err)
		return nil, huma.Error500InternalServerError("could not load the team")
	}
	return &teamOutput{Body: teamBodyOf(t)}, nil
}

func (s *Server) myTeam(ctx context.Context, _ *struct{}) (*teamOutput, error) {
	pr := AuthOf(ctx).Principal
	t, err := s.opts.Accounts.OwnTeam(ctx, pr.UserID)
	switch {
	case errors.Is(err, accounts.ErrNotOnTeam):
		return nil, huma.Error404NotFound("you are not on a team")
	case err != nil:
		s.opts.Log.ErrorContext(ctx, "own team failed", "error", err)
		return nil, huma.Error500InternalServerError("could not load your team")
	}
	return &teamOutput{Body: teamBodyOf(t)}, nil
}

func (s *Server) leaveTeam(ctx context.Context, _ *struct{}) (*leftTeamOutput, error) {
	pr := AuthOf(ctx).Principal
	err := s.opts.Accounts.LeaveTeam(ctx, pr.UserID)
	switch {
	case errors.Is(err, accounts.ErrNotOnTeam):
		return nil, huma.Error409Conflict("you are not on a team")
	case errors.Is(err, accounts.ErrTeamHasScored):
		return nil, huma.Error403Forbidden("you cannot leave a team that has solves")
	case err != nil:
		s.opts.Log.ErrorContext(ctx, "leave team failed", "error", err)
		return nil, huma.Error500InternalServerError("could not leave the team")
	}
	out := &leftTeamOutput{}
	out.Body.OK = true
	return out, nil
}
