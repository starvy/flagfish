package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/starvy/flagfish/internal/accounts"
	"github.com/starvy/flagfish/internal/audit"
	"github.com/starvy/flagfish/internal/domain/policy"
)

type createTeamInput struct {
	Body struct {
		Name string `json:"name" minLength:"1" maxLength:"128"`
		// Password is the join password, and it is required: team names are printed on the
		// scoreboard, so a team without one is open to anyone who reads the standings. The
		// minimum matches a user's own password.
		Password string `json:"password" minLength:"8" maxLength:"128"`
	}
}

// joinTeamInput deliberately carries no minimum: a short password is simply a wrong one, and
// answering it with a validation error rather than the usual denial would tell an attacker
// something the denial is written not to.
type joinTeamInput struct {
	Body struct {
		Name     string `json:"name" minLength:"1" maxLength:"128"`
		Password string `json:"password,omitempty" maxLength:"128" required:"false"`
	}
}

type setJoinSecretInput struct {
	Body struct {
		Password string `json:"password" minLength:"8" maxLength:"128"`
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
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// Email appears only on the own-team view; the public profile query never selects it.
	Email       *string      `json:"email,omitempty"`
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

	Register(s.Public, policy.ClassTeamSelf, huma.Operation{
		OperationID: "my-team", Method: http.MethodGet, Path: "/me/team",
		Summary: "The caller's team", Tags: []string{"teams"},
	}, s.myTeam)

	Register(s.Public, policy.ClassTeamSelf, huma.Operation{
		OperationID: "update-my-team", Method: http.MethodPatch, Path: "/me/team",
		Summary: "Update the caller's team (captain only)", Tags: []string{"teams"},
	}, s.updateMyTeam)

	Register(s.Public, policy.ClassTeamSelf, huma.Operation{
		OperationID: "set-team-join-password", Method: http.MethodPut, Path: "/me/team/password",
		Summary: "Rotate the team's join password (captain only)", Tags: []string{"teams"},
	}, s.setTeamJoinSecret)

	Register(s.Public, policy.ClassTeamEnrollment, huma.Operation{
		OperationID: "leave-team", Method: http.MethodPost, Path: "/me/team/leave",
		Summary: "Leave the caller's team", Tags: []string{"teams"},
	}, s.leaveTeam)

	Register(s.Public, policy.ClassTeamEnrollment, huma.Operation{
		OperationID: "kick-team-member", Method: http.MethodDelete, Path: "/me/team/members/{userID}",
		Summary: "Remove a member from the caller's team (captain only)", Tags: []string{"teams"},
	}, s.kickTeamMember)

	Register(s.Public, policy.ClassTeamSelf, huma.Operation{
		OperationID: "transfer-captaincy", Method: http.MethodPut, Path: "/me/team/captain",
		Summary: "Hand captaincy to another member (captain only)", Tags: []string{"teams"},
	}, s.transferCaptaincy)

	Register(s.Public, policy.ClassTeamEnrollment, huma.Operation{
		OperationID: "disband-team", Method: http.MethodDelete, Path: "/me/team",
		Summary: "Disband the caller's team (captain only, no history)", Tags: []string{"teams"},
	}, s.disbandTeam)
}

// callerActor stamps the acting principal onto a self-service mutation's audit trail, exactly as
// adminActor does for the admin surface — a captain's roster change records who made it.
func (s *Server) callerActor(ctx context.Context) audit.Actor {
	return audit.Actor{ID: AuthOf(ctx).Principal.UserID, IP: clientIPOf(ctx)}
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
		ID: t.ID, Name: t.Name, Email: t.Email,
		Website: t.Website, Affiliation: t.Affiliation, Country: t.Country,
		Score: t.Score, CreatedAt: t.CreatedAt, IsCaptain: t.IsCaptain, Members: members,
	}
}

// updateMyTeamInput is the captain-editable slice of the team, tri-state per field.
type updateMyTeamInput struct {
	Body struct {
		Email       Optional[string] `json:"email,omitempty" format:"email" maxLength:"255"`
		Website     Optional[string] `json:"website,omitempty" maxLength:"255"`
		Affiliation Optional[string] `json:"affiliation,omitempty" maxLength:"255"`
		Country     Optional[string] `json:"country,omitempty" maxLength:"64"`
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
	case errors.Is(err, accounts.ErrJoinSecretTooShort):
		return nil, huma.Error422UnprocessableEntity(err.Error())
	case err != nil:
		s.opts.Log.ErrorContext(ctx, "create team failed", "error", err)
		return nil, huma.Error500InternalServerError("could not create the team")
	}
	return &teamOutput{Body: teamBodyOf(t)}, nil
}

// setTeamJoinSecret rotates the join password. It is also how a team whose secret predates the
// requirement gets one: those rows are locked until a captain runs this.
func (s *Server) setTeamJoinSecret(ctx context.Context, in *setJoinSecretInput) (*leftTeamOutput, error) {
	err := s.opts.Accounts.SetJoinSecret(ctx, s.callerActor(ctx), in.Body.Password)
	switch {
	case errors.Is(err, accounts.ErrNotOnTeam):
		return nil, huma.Error404NotFound("you are not on a team")
	case errors.Is(err, accounts.ErrNotCaptain):
		return nil, huma.Error403Forbidden("only the captain can change the join password")
	case errors.Is(err, accounts.ErrJoinSecretTooShort):
		return nil, huma.Error422UnprocessableEntity(err.Error())
	case err != nil:
		s.opts.Log.ErrorContext(ctx, "set team join password failed", "error", err)
		return nil, huma.Error500InternalServerError("could not change the join password")
	}
	out := &leftTeamOutput{}
	out.Body.OK = true
	return out, nil
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

func (s *Server) updateMyTeam(ctx context.Context, in *updateMyTeamInput) (*teamOutput, error) {
	pr := AuthOf(ctx).Principal
	email, clearEmail := in.Body.Email.split()
	website, clearWebsite := in.Body.Website.split()
	affiliation, clearAffiliation := in.Body.Affiliation.split()
	country, clearCountry := in.Body.Country.split()

	t, err := s.opts.Accounts.UpdateOwnTeam(ctx, pr.UserID, accounts.TeamPatch{
		Email: email, Website: website, Affiliation: affiliation, Country: country,
		ClearEmail: clearEmail, ClearWebsite: clearWebsite,
		ClearAffiliation: clearAffiliation, ClearCountry: clearCountry,
	})
	switch {
	case errors.Is(err, accounts.ErrNotOnTeam):
		return nil, huma.Error404NotFound("you are not on a team")
	case errors.Is(err, accounts.ErrNotCaptain):
		return nil, huma.Error403Forbidden("only the captain can edit the team")
	case errors.Is(err, accounts.ErrTeamEmailTaken):
		return nil, huma.Error409Conflict("that team email is already in use")
	case err != nil:
		s.opts.Log.ErrorContext(ctx, "update team failed", "error", err)
		return nil, huma.Error500InternalServerError("could not update the team")
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

type kickMemberInput struct {
	UserID int64 `path:"userID"`
}

type transferCaptainInput struct {
	Body struct {
		UserID int64 `json:"user_id"`
	}
}

func (s *Server) kickTeamMember(ctx context.Context, in *kickMemberInput) (*teamOutput, error) {
	t, err := s.opts.Accounts.KickMember(ctx, s.callerActor(ctx), in.UserID)
	switch {
	case errors.Is(err, accounts.ErrNotOnTeam):
		return nil, huma.Error404NotFound("you are not on a team")
	case errors.Is(err, accounts.ErrNotCaptain):
		return nil, huma.Error403Forbidden("only the captain can remove members")
	case errors.Is(err, accounts.ErrCannotKickSelf):
		return nil, huma.Error409Conflict("use leave to remove yourself from the team")
	case errors.Is(err, accounts.ErrTargetNotMember):
		return nil, huma.Error404NotFound("that user is not on your team")
	case errors.Is(err, accounts.ErrTeamHasScored):
		return nil, huma.Error403Forbidden("you cannot remove members from a team that has solves")
	case err != nil:
		s.opts.Log.ErrorContext(ctx, "kick member failed", "error", err)
		return nil, huma.Error500InternalServerError("could not remove the member")
	}
	return &teamOutput{Body: teamBodyOf(t)}, nil
}

func (s *Server) transferCaptaincy(ctx context.Context, in *transferCaptainInput) (*teamOutput, error) {
	t, err := s.opts.Accounts.TransferCaptaincy(ctx, s.callerActor(ctx), in.Body.UserID)
	switch {
	case errors.Is(err, accounts.ErrNotOnTeam):
		return nil, huma.Error404NotFound("you are not on a team")
	case errors.Is(err, accounts.ErrNotCaptain):
		return nil, huma.Error403Forbidden("only the captain can transfer captaincy")
	case errors.Is(err, accounts.ErrTargetNotMember):
		return nil, huma.Error404NotFound("that user is not on your team")
	case err != nil:
		s.opts.Log.ErrorContext(ctx, "transfer captaincy failed", "error", err)
		return nil, huma.Error500InternalServerError("could not transfer captaincy")
	}
	return &teamOutput{Body: teamBodyOf(t)}, nil
}

func (s *Server) disbandTeam(ctx context.Context, _ *struct{}) (*leftTeamOutput, error) {
	err := s.opts.Accounts.DisbandTeam(ctx, s.callerActor(ctx))
	switch {
	case errors.Is(err, accounts.ErrNotOnTeam):
		return nil, huma.Error404NotFound("you are not on a team")
	case errors.Is(err, accounts.ErrNotCaptain):
		return nil, huma.Error403Forbidden("only the captain can disband the team")
	case errors.Is(err, accounts.ErrTeamHasHistory):
		return nil, huma.Error409Conflict("you cannot disband a team that has a scoreboard history")
	case err != nil:
		s.opts.Log.ErrorContext(ctx, "disband team failed", "error", err)
		return nil, huma.Error500InternalServerError("could not disband the team")
	}
	out := &leftTeamOutput{}
	out.Body.OK = true
	return out, nil
}
