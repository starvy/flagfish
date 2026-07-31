package httpapi

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/starvy/flagfish/internal/adminops"
	"github.com/starvy/flagfish/internal/domain/policy"
)

// An annotation is a (key, value) pair on one challenge — the keyed sibling of a tag. It is managed
// per challenge and per key, because the key is what a renderer asks for and the challenge is what
// answers, so there is no board-wide surface here the way there is for tags.
//
// PUT, not POST: setting a key is idempotent, and the URL already names the resource being written.

type adminSetAnnotationInput struct {
	ID  int64  `path:"id"`
	Key string `path:"key" maxLength:"64"`
	// Validation lives in the domain layer, not in these tags: the CLI and any future importer write
	// annotations too, and a rule spelled only in an OpenAPI schema is one they do not get.
	Body struct {
		Value string `json:"value" minLength:"1" maxLength:"256"`
	}
}

type adminAnnotationBody struct {
	ChallengeID int64  `json:"challenge_id"`
	Key         string `json:"key"`
	Value       string `json:"value"`
}

type adminSetAnnotationOutput struct {
	Body adminAnnotationBody
}

type adminDeleteAnnotationInput struct {
	ID  int64  `path:"id"`
	Key string `path:"key" maxLength:"64"`
}

type adminDeleteAnnotationOutput struct{}

func (s *Server) registerAdminAnnotations() {
	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-set-annotation", Method: http.MethodPut,
		Path:    "/challenges/{id}/annotations/{key}",
		Summary: "Set a challenge annotation (create or replace)", Tags: []string{"admin/annotations"},
	}, s.adminSetAnnotation)

	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-delete-annotation", Method: http.MethodDelete,
		Path:          "/challenges/{id}/annotations/{key}",
		DefaultStatus: http.StatusNoContent,
		Summary:       "Remove a challenge annotation", Tags: []string{"admin/annotations"},
	}, s.adminDeleteAnnotation)
}

func (s *Server) adminSetAnnotation(ctx context.Context, in *adminSetAnnotationInput) (*adminSetAnnotationOutput, error) {
	a, err := s.opts.AdminOps.SetAnnotation(ctx, s.adminActor(ctx), in.ID, in.Key, in.Body.Value)
	if err != nil {
		return nil, s.adminOpsError(ctx, err, "set annotation")
	}
	return &adminSetAnnotationOutput{Body: adminAnnotationBody{
		ChallengeID: in.ID, Key: a.Key, Value: a.Value,
	}}, nil
}

func (s *Server) adminDeleteAnnotation(ctx context.Context, in *adminDeleteAnnotationInput) (*adminDeleteAnnotationOutput, error) {
	if err := s.opts.AdminOps.RemoveAnnotation(ctx, s.adminActor(ctx), in.ID, in.Key); err != nil {
		return nil, s.adminOpsError(ctx, err, "delete annotation")
	}
	return &adminDeleteAnnotationOutput{}, nil
}

// adminAnnotationMap flattens the service's list into the object the API serves. Always non-nil:
// the field is declared as always present, and a client that has to handle both null and {} will
// eventually handle one of them wrong.
func adminAnnotationMap(list []adminops.Annotation) map[string]string {
	out := make(map[string]string, len(list))
	for _, a := range list {
		out[a.Key] = a.Value
	}
	return out
}
