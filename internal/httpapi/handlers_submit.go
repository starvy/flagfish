package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/starvy/flagfish/internal/domain/account"
	"github.com/starvy/flagfish/internal/domain/policy"
	"github.com/starvy/flagfish/internal/gameplay"
)

type attemptInput struct {
	ID   int64 `path:"id"`
	Body struct {
		Flag string `json:"flag" maxLength:"512"`
	}
}

type attemptOutput struct {
	Body struct {
		Status     string `json:"status"`
		FirstBlood bool   `json:"first_blood"`
		Value      int32  `json:"value"`
		// NextID is the suggested-next challenge, present only on a fresh correct solve.
		NextID *int64 `json:"next_id,omitempty"`
	}
}

type unlockInput struct {
	ID     int64 `path:"id"`
	HintID int64 `path:"hint_id"`
}

type unlockOutput struct {
	Body struct {
		HintID  int64  `json:"hint_id"`
		Content string `json:"content"`
		Charged int32  `json:"charged"`
		Score   int64  `json:"score"`
	}
}

func (s *Server) registerSubmit() {
	Register(s.Public, policy.ClassChallengeAttempt, huma.Operation{
		OperationID: "attempt", Method: http.MethodPost, Path: "/challenges/{id}/attempt",
		Summary: "Submit a flag for a challenge", Tags: []string{"gameplay"},
	}, s.attempt)

	Register(s.Public, policy.ClassHintUnlock, huma.Operation{
		OperationID: "unlock-hint", Method: http.MethodPost, Path: "/challenges/{id}/hints/{hint_id}/unlock",
		Summary: "Unlock a hint", Tags: []string{"gameplay"},
	}, s.unlockHint)
}

func (s *Server) attempt(ctx context.Context, in *attemptInput) (*attemptOutput, error) {
	// Time the whole submit transaction at this boundary. The observation happens after Submit
	// returns, so it adds nothing to the wrong-answer pre-lock leg inside the hot path, and the
	// gameplay package stays free of any metrics dependency.
	start := time.Now()
	res, err := s.opts.Gameplay.Submit(ctx, gameplay.SubmitInput{
		ChallengeID: in.ID,
		Actor:       s.actor(ctx),
		Provided:    in.Body.Flag,
	})
	s.opts.Metrics.ObserveSubmit(submitResult(res, err), time.Since(start))
	switch {
	case errors.Is(err, gameplay.ErrChallengeNotFound):
		return nil, huma.Error404NotFound("challenge not found")
	case errors.Is(err, gameplay.ErrChallengeLocked):
		return nil, huma.Error403Forbidden("solve the prerequisites first")
	case errors.Is(err, gameplay.ErrNoAttemptsRemaining):
		return nil, huma.Error403Forbidden("no attempts remaining for this challenge")
	case errors.Is(err, account.ErrTeamless):
		return nil, huma.Error403Forbidden("join a team to play")
	case err != nil:
		// A corrupt flag must surface as a 500, never as a laundered "incorrect".
		s.opts.Log.ErrorContext(ctx, "submit failed", "error", err)
		return nil, huma.Error500InternalServerError("could not submit flag")
	}
	out := &attemptOutput{}
	out.Body.Status = res.Status.String()
	out.Body.FirstBlood = res.FirstBlood
	out.Body.Value = res.Value
	out.Body.NextID = res.NextID
	return out, nil
}

// submitResult is the histogram's outcome label. A bounded set — the three statuses plus "error" —
// so it cannot explode metric cardinality.
func submitResult(res gameplay.Result, err error) string {
	if err != nil {
		return "error"
	}
	return res.Status.String()
}

func (s *Server) unlockHint(ctx context.Context, in *unlockInput) (*unlockOutput, error) {
	u, err := s.opts.Gameplay.UnlockHint(ctx, in.ID, in.HintID, s.actor(ctx))
	switch {
	case errors.Is(err, gameplay.ErrHintNotFound):
		return nil, huma.Error404NotFound("hint not found")
	case errors.Is(err, gameplay.ErrHintLocked):
		return nil, huma.Error403Forbidden("unlock the prerequisite hints first")
	case errors.Is(err, gameplay.ErrAlreadyUnlocked):
		return nil, huma.Error409Conflict("hint already unlocked")
	case errors.Is(err, gameplay.ErrInsufficientScore):
		return nil, huma.Error402PaymentRequired("not enough points to unlock this hint")
	case errors.Is(err, account.ErrTeamless):
		return nil, huma.Error403Forbidden("join a team to play")
	case err != nil:
		s.opts.Log.ErrorContext(ctx, "hint unlock failed", "error", err)
		return nil, huma.Error500InternalServerError("could not unlock hint")
	}
	out := &unlockOutput{}
	out.Body.HintID = u.HintID
	out.Body.Content = u.Content
	out.Body.Charged = u.Charged
	out.Body.Score = u.Score
	return out, nil
}
