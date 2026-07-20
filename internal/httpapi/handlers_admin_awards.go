package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/starvy/flagfish/internal/domain/award"
	"github.com/starvy/flagfish/internal/domain/policy"
)

// adminAwardBody is one manual adjustment on the wire. account_id is the scoring account it moved —
// a team in teams mode, a user in users mode.
type adminAwardBody struct {
	ID        int64     `json:"id"`
	AccountID int64     `json:"account_id"`
	Value     int32     `json:"value"`
	Reason    string    `json:"reason"`
	Date      time.Time `json:"date"`
}

type adminAwardOutput struct {
	Body adminAwardBody
}

type adminGrantAwardInput struct {
	Body struct {
		AccountID int64  `json:"account_id" minimum:"1"`
		Value     int32  `json:"value"`
		Reason    string `json:"reason" minLength:"1" maxLength:"500"`
	}
}

type adminListAwardsInput struct {
	AccountID int64 `query:"account_id" minimum:"1"`
}

type adminListAwardsOutput struct {
	Body struct {
		Awards []adminAwardBody `json:"awards"`
	}
}

type adminAwardIDInput struct {
	ID int64 `path:"id"`
}

func (s *Server) registerAdminAwards() {
	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-grant-award", Method: http.MethodPost, Path: "/awards",
		DefaultStatus: http.StatusCreated,
		Summary:       "Grant a manual point adjustment", Tags: []string{"admin/awards"},
	}, s.adminGrantAward)

	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-list-awards", Method: http.MethodGet, Path: "/awards",
		Summary: "List an account's manual adjustments", Tags: []string{"admin/awards"},
	}, s.adminListAwards)

	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-revoke-award", Method: http.MethodDelete, Path: "/awards/{id}",
		DefaultStatus: http.StatusNoContent,
		Summary:       "Revoke a manual point adjustment", Tags: []string{"admin/awards"},
	}, s.adminRevokeAward)
}

func (s *Server) adminGrantAward(ctx context.Context, in *adminGrantAwardInput) (*adminAwardOutput, error) {
	mode := s.opts.Config.Current().Mode
	a, err := s.opts.AdminOps.GrantAward(ctx, s.adminActor(ctx), mode, in.Body.AccountID, in.Body.Value, in.Body.Reason)
	if err != nil {
		return nil, s.adminOpsError(ctx, err, "grant award")
	}
	return &adminAwardOutput{Body: adminAwardBody{
		ID: a.ID, AccountID: in.Body.AccountID, Value: a.Value, Reason: a.Reason, Date: a.Date,
	}}, nil
}

func (s *Server) adminListAwards(ctx context.Context, in *adminListAwardsInput) (*adminListAwardsOutput, error) {
	mode := s.opts.Config.Current().Mode
	list, err := s.opts.AdminOps.ListManualAwards(ctx, mode, in.AccountID)
	if err != nil {
		return nil, s.adminOpsError(ctx, err, "list awards")
	}
	out := &adminListAwardsOutput{}
	out.Body.Awards = make([]adminAwardBody, len(list))
	for i, a := range list {
		out.Body.Awards[i] = adminAwardBody{ID: a.ID, AccountID: in.AccountID, Value: a.Value, Reason: a.Reason, Date: a.Date}
	}
	return out, nil
}

func (s *Server) adminRevokeAward(ctx context.Context, in *adminAwardIDInput) (*adminDeleteOutput, error) {
	if err := s.opts.AdminOps.RevokeAward(ctx, s.adminActor(ctx), in.ID); err != nil {
		return nil, s.adminOpsError(ctx, err, "revoke award")
	}
	return &adminDeleteOutput{}, nil
}

// Keep the domain cap and the wire cap on the reason one number, so a change in one is a compile
// error in the other rather than a silent skew.
var _ = [1]struct{}{}[award.MaxReasonLen-500]
