package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/starvy/flagfish/internal/domain/policy"
	"github.com/starvy/flagfish/internal/solvefeed"
)

// registerSolveFeedStream mounts the live solve pulse as a raw chi route: server-sent events are not
// a JSON operation Huma can type, so it sits on the gated sub-router behind an explicit policy gate,
// exactly like the notifications stream.
func (s *Server) registerSolveFeedStream() {
	s.gated.With(s.httpGate(policy.ClassSolveFeed, policy.SurfacePublic)).
		Get("/api/v1/events/solves", s.streamSolves)
}

// streamSolves streams solves as they land. There is no replay on connect: a pulse is a thing that
// flashes at the moment it happens, so a client that was not connected missed nothing it is owed.
func (s *Server) streamSolves(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		// No flusher means no streaming: fail loudly rather than buffer a stream nobody receives.
		problem(w, http.StatusInternalServerError, "streaming-unsupported", "streaming is not supported")
		return
	}

	ctx := r.Context()

	ch, unsubscribe := s.opts.SolveFeed.Subscribe()
	defer unsubscribe()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Tell a buffering reverse proxy (nginx) to pass events straight through.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ticker := time.NewTicker(heartbeat)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// Client disconnected or the request was cancelled. The deferred unsubscribe runs and
			// the subscriber is gone — no leak.
			return
		case ev, ok := <-ch:
			if !ok {
				// The broadcaster dropped us (fell behind) or is shutting down.
				return
			}
			if s.solveSuppressedByFreeze(ev) {
				continue
			}
			if writeSolveSSE(w, ev) != nil {
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

// solveSuppressedByFreeze decides, per event, whether the freeze hides this solve.
//
// It has to be per event rather than per connection: the freeze horizon is a point in time, so an
// open stream carries solves from before it and must withhold the ones from after. The snapshot is
// re-read on every event for the same reason the announcement worker re-reads it at send time — a
// freeze configured while somebody is watching has to take effect on the next pulse, not on their
// next reconnect.
//
// The exemption is nobody's. An admin who wants live solves has the admin surfaces; widening it here
// would put the frozen board's contents on a channel that never shows a URL saying so.
func (s *Server) solveSuppressedByFreeze(e solvefeed.Event) bool {
	return policy.SuppressedByFreeze(s.opts.Config.Current().Freeze, e.SolvedAt)
}

// writeSolveSSE emits one solve as a named SSE event.
//
// No `id:` line, deliberately. An id is what a browser echoes back as Last-Event-ID to resume a
// stream, and this feed has nothing to resume from — advertising one would promise a replay that
// does not exist.
func writeSolveSSE(w io.Writer, e solvefeed.Event) error {
	payload, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("solvefeed: marshal event: %w", err)
	}
	if _, err := fmt.Fprintf(w, "event: solve\ndata: %s\n\n", payload); err != nil {
		return fmt.Errorf("solvefeed: write event: %w", err)
	}
	return nil
}
