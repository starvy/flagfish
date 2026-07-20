package httpapi

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/starvy/flagfish/internal/adminops"
	"github.com/starvy/flagfish/internal/db"
	"github.com/starvy/flagfish/internal/domain/policy"
)

// adminFieldBody is one custom registration field on the wire.
type adminFieldBody struct {
	ID          int64   `json:"id"`
	Name        string  `json:"name"`
	AppliesTo   string  `json:"applies_to"`
	FieldType   string  `json:"field_type"`
	Description *string `json:"description,omitempty"`
	Required    bool    `json:"required"`
	Public      bool    `json:"public"`
	Editable    bool    `json:"editable"`
	Position    int32   `json:"position"`
}

func adminField(f db.Field) adminFieldBody {
	return adminFieldBody{
		ID: f.ID, Name: f.Name, AppliesTo: f.AppliesTo, FieldType: f.FieldType,
		Description: f.Description, Required: f.Required, Public: f.Public,
		Editable: f.Editable, Position: f.Position,
	}
}

type adminFieldOutput struct {
	Body adminFieldBody
}

type adminListFieldsOutput struct {
	Body struct {
		Fields []adminFieldBody `json:"fields"`
	}
}

type adminCreateFieldInput struct {
	Body struct {
		Name        string  `json:"name" minLength:"1" maxLength:"128"`
		AppliesTo   string  `json:"applies_to" enum:"user,team"`
		FieldType   string  `json:"field_type" enum:"text,boolean"`
		Description *string `json:"description,omitempty" maxLength:"500"`
		Required    bool    `json:"required,omitempty" default:"false"`
		Public      bool    `json:"public,omitempty" default:"false"`
		Editable    bool    `json:"editable,omitempty" default:"false"`
		Position    int32   `json:"position,omitempty" default:"0"`
	}
}

type adminUpdateFieldInput struct {
	ID   int64 `path:"id"`
	Body struct {
		Name        *string `json:"name,omitempty" minLength:"1" maxLength:"128"`
		Description *string `json:"description,omitempty" maxLength:"500"`
		Required    *bool   `json:"required,omitempty"`
		Public      *bool   `json:"public,omitempty"`
		Editable    *bool   `json:"editable,omitempty"`
		Position    *int32  `json:"position,omitempty"`
	}
}

type adminFieldIDInput struct {
	ID int64 `path:"id"`
}

func (s *Server) registerAdminFields() {
	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-create-field", Method: http.MethodPost, Path: "/fields",
		DefaultStatus: http.StatusCreated,
		Summary:       "Create a custom registration field", Tags: []string{"admin/fields"},
	}, s.adminCreateField)

	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-list-fields", Method: http.MethodGet, Path: "/fields",
		Summary: "List custom registration fields", Tags: []string{"admin/fields"},
	}, s.adminListFields)

	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-update-field", Method: http.MethodPatch, Path: "/fields/{id}",
		Summary: "Update a custom field (partial)", Tags: []string{"admin/fields"},
	}, s.adminUpdateField)

	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-delete-field", Method: http.MethodDelete, Path: "/fields/{id}",
		DefaultStatus: http.StatusNoContent,
		Summary:       "Delete a custom field (its answers are removed with it)", Tags: []string{"admin/fields"},
	}, s.adminDeleteField)
}

func (s *Server) adminCreateField(ctx context.Context, in *adminCreateFieldInput) (*adminFieldOutput, error) {
	f, err := s.opts.AdminOps.CreateField(ctx, s.adminActor(ctx), adminops.NewField{
		Name: in.Body.Name, AppliesTo: in.Body.AppliesTo, FieldType: in.Body.FieldType,
		Description: in.Body.Description, Required: in.Body.Required, Public: in.Body.Public,
		Editable: in.Body.Editable, Position: in.Body.Position,
	})
	if err != nil {
		return nil, s.adminOpsError(ctx, err, "create field")
	}
	return &adminFieldOutput{Body: adminField(f)}, nil
}

func (s *Server) adminListFields(ctx context.Context, _ *struct{}) (*adminListFieldsOutput, error) {
	list, err := s.opts.AdminOps.ListFields(ctx)
	if err != nil {
		return nil, s.adminOpsError(ctx, err, "list fields")
	}
	out := &adminListFieldsOutput{}
	out.Body.Fields = make([]adminFieldBody, len(list))
	for i, f := range list {
		out.Body.Fields[i] = adminField(f)
	}
	return out, nil
}

func (s *Server) adminUpdateField(ctx context.Context, in *adminUpdateFieldInput) (*adminFieldOutput, error) {
	f, err := s.opts.AdminOps.UpdateField(ctx, s.adminActor(ctx), in.ID, adminops.FieldPatch{
		Name: in.Body.Name, Description: in.Body.Description,
		Required: in.Body.Required, Public: in.Body.Public,
		Editable: in.Body.Editable, Position: in.Body.Position,
	})
	if err != nil {
		return nil, s.adminOpsError(ctx, err, "update field")
	}
	return &adminFieldOutput{Body: adminField(f)}, nil
}

func (s *Server) adminDeleteField(ctx context.Context, in *adminFieldIDInput) (*adminDeleteOutput, error) {
	if err := s.opts.AdminOps.DeleteField(ctx, s.adminActor(ctx), in.ID); err != nil {
		return nil, s.adminOpsError(ctx, err, "delete field")
	}
	return &adminDeleteOutput{}, nil
}
