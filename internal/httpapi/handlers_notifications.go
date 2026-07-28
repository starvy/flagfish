package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/starvy/flagfish/internal/domain/policy"
	"github.com/starvy/flagfish/internal/notify"
)

const (
	// replayLimit caps how many past notifications a fresh stream replays. Enough to catch a client
	// up on a normal event without turning connect into a paging operation.
	replayLimit = 50
	// heartbeat keeps proxies and load balancers from reaping an idle stream, and lets the server
	// notice a dead client on the next write.
	heartbeat = 25 * time.Second
)

func (s *Server) registerNotifications() {
	Register(s.Public, policy.ClassNotifications, huma.Operation{
		OperationID: "list-notifications", Method: http.MethodGet, Path: "/notifications",
		Summary: "List notifications (paginated)", Tags: []string{"notifications"},
	}, s.listNotifications)
}

// registerNotificationStream mounts the SSE endpoint as a raw chi route: server-sent events are not
// a JSON operation Huma can type, so it sits on the gated sub-router with an explicit policy gate.
func (s *Server) registerNotificationStream() {
	s.gated.With(s.httpGate(policy.ClassSSE, policy.SurfacePublic)).
		Get("/api/v1/notifications/stream", s.streamNotifications)
}

type notificationBody struct {
	ID      int64     `json:"id"`
	Title   string    `json:"title"`
	Content string    `json:"content"`
	Date    time.Time `json:"date"`
}

type listNotificationsInput struct {
	Page    int `query:"page" minimum:"1" maximum:"1000000" default:"1"`
	PerPage int `query:"per_page" minimum:"1" maximum:"100" default:"50"`
}

type listNotificationsOutput struct {
	Body struct {
		Notifications []notificationBody `json:"notifications"`
		Total         int64              `json:"total"`
		Page          int                `json:"page"`
		PerPage       int                `json:"per_page"`
	}
}

func (s *Server) listNotifications(ctx context.Context, in *listNotificationsInput) (*listNotificationsOutput, error) {
	page, err := s.opts.Notify.List(ctx, in.Page, in.PerPage)
	if err != nil {
		s.opts.Log.ErrorContext(ctx, "list notifications failed", "error", err)
		return nil, huma.Error500InternalServerError("could not list notifications")
	}
	out := &listNotificationsOutput{}
	out.Body.Total = page.Total
	out.Body.Page = in.Page
	out.Body.PerPage = in.PerPage
	out.Body.Notifications = make([]notificationBody, len(page.Items))
	for i, n := range page.Items {
		out.Body.Notifications[i] = notificationBody{ID: n.ID, Title: n.Title, Content: n.Content, Date: n.Date}
	}
	return out, nil
}

// streamNotifications is the server-sent-events endpoint. It replays the recent persisted
// notifications so a just-connected client is caught up, then streams live ones off the broadcaster
// until the client disconnects or the server shuts down.
func (s *Server) streamNotifications(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		// No flusher means no streaming: fail loudly rather than buffer a stream nobody receives.
		problem(w, http.StatusInternalServerError, "streaming-unsupported", "streaming is not supported")
		return
	}

	ctx := r.Context()

	// Subscribe before replaying: a notification published in the gap between the replay query and
	// the subscription would otherwise be lost. The overlap can replay one notification that also
	// arrives live — clients key on id, so a repeat is harmless, a gap is not.
	ch, unsubscribe := s.opts.Broadcaster.Subscribe()
	defer unsubscribe()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Tell a buffering reverse proxy (nginx) to pass events straight through.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	if recent, err := s.opts.Notify.Recent(ctx, replayLimit); err != nil {
		// The 200 is already on the wire, so this cannot become an error response. Log it and go
		// live: a stream without replay still delivers, and the client can poll the list to backfill.
		s.opts.Log.ErrorContext(ctx, "notification replay failed", "error", err)
	} else {
		// Recent is newest-first; replay oldest-first so ids arrive in order.
		for i := len(recent) - 1; i >= 0; i-- {
			if writeSSE(w, recent[i]) != nil {
				return
			}
		}
	}
	flusher.Flush()

	ticker := time.NewTicker(heartbeat)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// Client disconnected or the request was cancelled. The deferred unsubscribe runs and
			// the subscriber is gone — no leak.
			return
		case n, ok := <-ch:
			if !ok {
				// The broadcaster dropped us (fell behind) or is shutting down.
				return
			}
			if writeSSE(w, n) != nil {
				return
			}
			flusher.Flush()
		case <-ticker.C:
			if _, err := io.WriteString(w, ": keep-alive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// writeSSE emits one notification as an SSE event: a stable id the client can dedupe on, a named
// event type, and the JSON payload.
func writeSSE(w io.Writer, n notify.Notification) error {
	payload, err := json.Marshal(notificationBody{ID: n.ID, Title: n.Title, Content: n.Content, Date: n.Date})
	if err != nil {
		return fmt.Errorf("notify: marshal event: %w", err)
	}
	if _, err := fmt.Fprintf(w, "id: %d\nevent: notification\ndata: %s\n\n", n.ID, payload); err != nil {
		return fmt.Errorf("notify: write event: %w", err)
	}
	return nil
}
