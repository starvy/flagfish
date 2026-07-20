package httpapi

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/starvy/flagfish/internal/domain/policy"
)

// A tag is a (challenge_id, value) row; the same value on many challenges is one tag with several
// uses. The board-wide endpoints manage a tag by its value — list, merge, remove everywhere — and
// the per-challenge sub-resource attaches or detaches one use.

type adminTag struct {
	Value string `json:"value"`
	Uses  int64  `json:"uses"`
}

type adminListTagsOutput struct {
	Body struct {
		Tags []adminTag `json:"tags"`
	}
}

type adminMergeTagInput struct {
	Value string `path:"value"`
	Body  struct {
		Into string `json:"into" minLength:"1"`
	}
}

type adminMergeTagOutput struct{}

type adminDeleteTagInput struct {
	Value string `path:"value"`
	// A tag attached to challenges is refused unless force is set, so the whole-board removal is
	// always a deliberate choice rather than a mistyped path.
	Force bool `query:"force"`
}

type adminDeleteTagOutput struct{}

type adminAttachTagInput struct {
	ID   int64 `path:"id"`
	Body struct {
		Value string `json:"value" minLength:"1" maxLength:"128"`
	}
}

type adminChallengeTagBody struct {
	ID          int64  `json:"id"`
	ChallengeID int64  `json:"challenge_id"`
	Value       string `json:"value"`
}

type adminAttachTagOutput struct {
	Body adminChallengeTagBody
}

type adminDetachTagInput struct {
	ID    int64  `path:"id"`
	Value string `path:"value"`
}

type adminDetachTagOutput struct{}

func (s *Server) registerAdminTags() {
	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-list-tags", Method: http.MethodGet, Path: "/tags",
		Summary: "List every tag with its usage count", Tags: []string{"admin/tags"},
	}, s.adminListTags)

	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-merge-tag", Method: http.MethodPost, Path: "/tags/{value}/merge",
		DefaultStatus: http.StatusNoContent,
		Summary:       "Rename a tag, merging it into the destination", Tags: []string{"admin/tags"},
	}, s.adminMergeTag)

	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-delete-tag", Method: http.MethodDelete, Path: "/tags/{value}",
		DefaultStatus: http.StatusNoContent,
		Summary:       "Remove a tag from every challenge (force required while in use)", Tags: []string{"admin/tags"},
	}, s.adminDeleteTag)

	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-attach-tag", Method: http.MethodPost, Path: "/challenges/{id}/tags",
		DefaultStatus: http.StatusCreated,
		Summary:       "Attach a tag to a challenge", Tags: []string{"admin/tags"},
	}, s.adminAttachTag)

	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-detach-tag", Method: http.MethodDelete, Path: "/challenges/{id}/tags/{value}",
		DefaultStatus: http.StatusNoContent,
		Summary:       "Detach a tag from a challenge", Tags: []string{"admin/tags"},
	}, s.adminDetachTag)
}

func (s *Server) adminAttachTag(ctx context.Context, in *adminAttachTagInput) (*adminAttachTagOutput, error) {
	tag, err := s.opts.AdminOps.AddTag(ctx, s.adminActor(ctx), in.ID, in.Body.Value)
	if err != nil {
		return nil, s.adminOpsError(ctx, err, "attach tag")
	}
	return &adminAttachTagOutput{Body: adminChallengeTagBody{
		ID: tag.ID, ChallengeID: tag.ChallengeID, Value: tag.Value,
	}}, nil
}

func (s *Server) adminDetachTag(ctx context.Context, in *adminDetachTagInput) (*adminDetachTagOutput, error) {
	if err := s.opts.AdminOps.RemoveTag(ctx, s.adminActor(ctx), in.ID, in.Value); err != nil {
		return nil, s.adminOpsError(ctx, err, "detach tag")
	}
	return &adminDetachTagOutput{}, nil
}

func (s *Server) adminListTags(ctx context.Context, _ *struct{}) (*adminListTagsOutput, error) {
	rows, err := s.opts.AdminOps.ListTags(ctx)
	if err != nil {
		return nil, s.adminOpsError(ctx, err, "list tags")
	}
	out := &adminListTagsOutput{}
	out.Body.Tags = make([]adminTag, len(rows))
	for i, r := range rows {
		out.Body.Tags[i] = adminTag{Value: r.Value, Uses: r.Uses}
	}
	return out, nil
}

func (s *Server) adminMergeTag(ctx context.Context, in *adminMergeTagInput) (*adminMergeTagOutput, error) {
	if err := s.opts.AdminOps.MergeTag(ctx, s.adminActor(ctx), in.Value, in.Body.Into); err != nil {
		return nil, s.adminOpsError(ctx, err, "merge tag")
	}
	return &adminMergeTagOutput{}, nil
}

func (s *Server) adminDeleteTag(ctx context.Context, in *adminDeleteTagInput) (*adminDeleteTagOutput, error) {
	if err := s.opts.AdminOps.DeleteTag(ctx, s.adminActor(ctx), in.Value, in.Force); err != nil {
		return nil, s.adminOpsError(ctx, err, "delete tag")
	}
	return &adminDeleteTagOutput{}, nil
}
