package httpapi

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/starvy/flagfish/internal/adminops"
	"github.com/starvy/flagfish/internal/db"
	"github.com/starvy/flagfish/internal/domain/policy"
)

type adminBracketBody struct {
	ID          int64   `json:"id"`
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
	AppliesTo   string  `json:"applies_to"`
}

type adminBracketOutput struct {
	Body adminBracketBody
}

func adminBracket(b db.Bracket) *adminBracketOutput {
	return &adminBracketOutput{Body: adminBracketBody{
		ID: b.ID, Name: b.Name, Description: b.Description, AppliesTo: b.AppliesTo,
	}}
}

type adminListBracketsOutput struct {
	Body struct {
		Brackets []adminBracketBody `json:"brackets"`
	}
}

type adminCreateBracketInput struct {
	Body struct {
		Name        string  `json:"name" minLength:"1" maxLength:"128"`
		Description *string `json:"description,omitempty"`
		AppliesTo   string  `json:"applies_to" enum:"users,teams"`
	}
}

type adminUpdateBracketInput struct {
	ID   int64 `path:"id"`
	Body struct {
		Name        *string `json:"name,omitempty" minLength:"1" maxLength:"128"`
		Description *string `json:"description,omitempty"`
	}
}

type adminBracketIDInput struct {
	ID int64 `path:"id"`
}

// adminAssignBracketInput moves an account into a bracket. A null bracket_id clears the assignment.
type adminAssignBracketInput struct {
	AccountID int64 `path:"accountID"`
	Body      struct {
		BracketID *int64 `json:"bracket_id"`
	}
}

type adminAssignBracketOutput struct {
	Body struct {
		AccountID int64  `json:"account_id"`
		Name      string `json:"name"`
		BracketID *int64 `json:"bracket_id"`
	}
}

func (s *Server) registerAdminBrackets() {
	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-create-bracket", Method: http.MethodPost, Path: "/brackets",
		DefaultStatus: http.StatusCreated,
		Summary:       "Create a bracket", Tags: []string{"admin/brackets"},
	}, s.adminCreateBracket)

	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-list-brackets", Method: http.MethodGet, Path: "/brackets",
		Summary: "List brackets", Tags: []string{"admin/brackets"},
	}, s.adminListBrackets)

	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-update-bracket", Method: http.MethodPatch, Path: "/brackets/{id}",
		Summary: "Update a bracket (partial)", Tags: []string{"admin/brackets"},
	}, s.adminUpdateBracket)

	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-delete-bracket", Method: http.MethodDelete, Path: "/brackets/{id}",
		DefaultStatus: http.StatusNoContent,
		Summary:       "Delete a bracket (members are unassigned, not deleted)", Tags: []string{"admin/brackets"},
	}, s.adminDeleteBracket)

	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-assign-bracket", Method: http.MethodPut, Path: "/accounts/{accountID}/bracket",
		Summary: "Assign an account to a bracket (or clear it)", Tags: []string{"admin/brackets"},
	}, s.adminAssignBracket)
}

func (s *Server) adminCreateBracket(ctx context.Context, in *adminCreateBracketInput) (*adminBracketOutput, error) {
	b, err := s.opts.AdminOps.CreateBracket(ctx, s.adminActor(ctx), adminops.NewBracket{
		Name: in.Body.Name, Description: in.Body.Description, AppliesTo: in.Body.AppliesTo,
	})
	if err != nil {
		return nil, s.adminOpsError(ctx, err, "create bracket")
	}
	return adminBracket(b), nil
}

func (s *Server) adminListBrackets(ctx context.Context, _ *struct{}) (*adminListBracketsOutput, error) {
	list, err := s.opts.AdminOps.ListBrackets(ctx)
	if err != nil {
		return nil, s.adminOpsError(ctx, err, "list brackets")
	}
	out := &adminListBracketsOutput{}
	out.Body.Brackets = make([]adminBracketBody, len(list))
	for i, b := range list {
		out.Body.Brackets[i] = adminBracketBody{ID: b.ID, Name: b.Name, Description: b.Description, AppliesTo: b.AppliesTo}
	}
	return out, nil
}

func (s *Server) adminUpdateBracket(ctx context.Context, in *adminUpdateBracketInput) (*adminBracketOutput, error) {
	b, err := s.opts.AdminOps.UpdateBracket(ctx, s.adminActor(ctx), in.ID, adminops.BracketPatch{
		Name: in.Body.Name, Description: in.Body.Description,
	})
	if err != nil {
		return nil, s.adminOpsError(ctx, err, "update bracket")
	}
	return adminBracket(b), nil
}

func (s *Server) adminDeleteBracket(ctx context.Context, in *adminBracketIDInput) (*adminDeleteOutput, error) {
	if err := s.opts.AdminOps.DeleteBracket(ctx, s.adminActor(ctx), in.ID); err != nil {
		return nil, s.adminOpsError(ctx, err, "delete bracket")
	}
	return &adminDeleteOutput{}, nil
}

func (s *Server) adminAssignBracket(ctx context.Context, in *adminAssignBracketInput) (*adminAssignBracketOutput, error) {
	mode := s.opts.Config.Current().Mode
	row, err := s.opts.AdminOps.AssignBracket(ctx, s.adminActor(ctx), mode, in.AccountID, in.Body.BracketID)
	if err != nil {
		return nil, s.adminOpsError(ctx, err, "assign bracket")
	}
	out := &adminAssignBracketOutput{}
	out.Body.AccountID, out.Body.Name, out.Body.BracketID = row.AccountID, row.Name, row.BracketID
	return out, nil
}
