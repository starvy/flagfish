package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/starvy/flagfish/internal/adminops"
	"github.com/starvy/flagfish/internal/domain/policy"
)

type adminTeamMemberBody struct {
	UserID  int64  `json:"user_id"`
	Name    string `json:"name"`
	Email   string `json:"email"`
	Captain bool   `json:"captain"`
	Banned  bool   `json:"banned"`
	Hidden  bool   `json:"hidden"`
}

type adminListTeamMembersInput struct {
	ID int64 `path:"id"`
}

type adminListTeamMembersOutput struct {
	Body struct {
		Members []adminTeamMemberBody `json:"members"`
	}
}

// Both writes answer 204: the roster they changed is a fresh GET away, and echoing a stale copy of
// it is how a console ends up showing a lineup nobody has.
type adminRosterOutput struct{}

type adminRemoveTeamMemberInput struct {
	ID     int64 `path:"id"`
	UserID int64 `path:"user_id"`
}

type adminMoveTeamMemberInput struct {
	ID     int64 `path:"id"`
	UserID int64 `path:"user_id"`
	Body   struct {
		ToTeamID int64 `json:"to_team_id" minimum:"1"`
	}
}

func (s *Server) registerAdminTeamRoster() {
	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-list-team-members", Method: http.MethodGet, Path: "/teams/{id}/members",
		Summary: "List a team's roster", Tags: []string{"admin/teams"},
	}, s.adminListTeamMembers)

	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-remove-team-member", Method: http.MethodDelete,
		Path:          "/teams/{id}/members/{user_id}",
		DefaultStatus: http.StatusNoContent,
		Summary:       "Remove a member from a team (their past solves stay with the team)",
		Tags:          []string{"admin/teams"},
	}, s.adminRemoveTeamMember)

	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-move-team-member", Method: http.MethodPost,
		Path:          "/teams/{id}/members/{user_id}/move",
		DefaultStatus: http.StatusNoContent,
		Summary:       "Move a member to another team (their past solves stay with the old team)",
		Tags:          []string{"admin/teams"},
	}, s.adminMoveTeamMember)
}

func (s *Server) adminListTeamMembers(ctx context.Context, in *adminListTeamMembersInput) (*adminListTeamMembersOutput, error) {
	members, err := s.opts.AdminOps.ListTeamMembers(ctx, s.opts.Config.Current().Mode, in.ID)
	if err != nil {
		return nil, s.rosterError(ctx, err, "list team members")
	}
	out := &adminListTeamMembersOutput{}
	out.Body.Members = make([]adminTeamMemberBody, len(members))
	for i, m := range members {
		out.Body.Members[i] = adminTeamMemberBody{
			UserID: m.UserID, Name: m.Name, Email: m.Email,
			Captain: m.Captain, Banned: m.Banned, Hidden: m.Hidden,
		}
	}
	return out, nil
}

func (s *Server) adminRemoveTeamMember(ctx context.Context, in *adminRemoveTeamMemberInput) (*adminRosterOutput, error) {
	mode := s.opts.Config.Current().Mode
	if err := s.opts.AdminOps.RemoveTeamMember(ctx, s.adminActor(ctx), mode, in.ID, in.UserID); err != nil {
		return nil, s.rosterError(ctx, err, "remove team member")
	}
	return &adminRosterOutput{}, nil
}

func (s *Server) adminMoveTeamMember(ctx context.Context, in *adminMoveTeamMemberInput) (*adminRosterOutput, error) {
	mode := s.opts.Config.Current().Mode
	err := s.opts.AdminOps.MoveTeamMember(ctx, s.adminActor(ctx), mode, in.ID, in.UserID, in.Body.ToTeamID)
	if err != nil {
		return nil, s.rosterError(ctx, err, "move team member")
	}
	return &adminRosterOutput{}, nil
}

// rosterError names the refusals unique to roster edits and hands everything else to the shared
// admin mapping, so a caller always learns which fact was false rather than getting a bare 500.
func (s *Server) rosterError(ctx context.Context, err error, action string) error {
	switch {
	case errors.Is(err, adminops.ErrNotTeamsMode):
		return huma.Error409Conflict("this instance scores users, not teams: there is no roster to edit")
	case errors.Is(err, adminops.ErrUserNotOnTeam):
		return huma.Error409Conflict("that user is not a member of this team")
	case errors.Is(err, adminops.ErrSameTeam):
		return huma.Error409Conflict("that user is already on the destination team")
	case errors.Is(err, adminops.ErrTeamFull):
		return huma.Error409Conflict("the destination team is at the team size cap")
	}
	return s.adminOpsError(ctx, err, action)
}
