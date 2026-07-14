package httpapi

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/starvy/flagfish/internal/domain/policy"
	"github.com/starvy/flagfish/internal/notify"
)

type adminCreateNotificationInput struct {
	Body struct {
		Title   string `json:"title" minLength:"1" maxLength:"256"`
		Content string `json:"content" minLength:"1"`
	}
}

type adminNotificationOutput struct {
	Body notificationBody
}

func (s *Server) registerAdminNotifications() {
	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-create-notification", Method: http.MethodPost, Path: "/notifications",
		DefaultStatus: http.StatusCreated,
		Summary:       "Publish a notification", Tags: []string{"admin/notifications"},
	}, s.adminCreateNotification)
}

func (s *Server) adminCreateNotification(ctx context.Context, in *adminCreateNotificationInput) (*adminNotificationOutput, error) {
	n, err := s.opts.Notify.Publish(ctx, s.adminActor(ctx), notify.New{
		Title: in.Body.Title, Content: in.Body.Content,
	})
	if err != nil {
		s.opts.Log.ErrorContext(ctx, "publish notification failed", "error", err)
		return nil, huma.Error500InternalServerError("could not publish notification")
	}
	return &adminNotificationOutput{Body: notificationBody{
		ID: n.ID, Title: n.Title, Content: n.Content, Date: n.Date,
	}}, nil
}
