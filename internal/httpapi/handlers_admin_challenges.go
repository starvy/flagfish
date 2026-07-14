package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/starvy/flagfish/internal/adminops"
	"github.com/starvy/flagfish/internal/audit"
	"github.com/starvy/flagfish/internal/db"
	"github.com/starvy/flagfish/internal/domain/policy"
)

// adminActor identifies the acting admin for the audit trail. The IP is the trusted-proxy-aware
// client address, same as the one stamped on submissions.
func (s *Server) adminActor(ctx context.Context) audit.Actor {
	return audit.Actor{ID: AuthOf(ctx).Principal.UserID, IP: clientIPOf(ctx)}
}

// adminOpsError maps service errors onto the wire. Database error text never leaves this function:
// anything unrecognised is logged in full and returned as a bare 500.
func (s *Server) adminOpsError(ctx context.Context, err error, action string) error {
	var ve *adminops.ValidationError
	switch {
	case errors.As(err, &ve):
		return huma.Error422UnprocessableEntity(ve.Reason)
	case errors.Is(err, adminops.ErrChallengeNotFound):
		return huma.Error404NotFound("challenge not found")
	case errors.Is(err, adminops.ErrFlagNotFound):
		return huma.Error404NotFound("flag not found")
	case errors.Is(err, adminops.ErrHintNotFound):
		return huma.Error404NotFound("hint not found")
	case errors.Is(err, adminops.ErrUserNotFound):
		return huma.Error404NotFound("user not found")
	case errors.Is(err, adminops.ErrBracketNotFound):
		return huma.Error404NotFound("bracket not found")
	case errors.Is(err, adminops.ErrAccountNotFound):
		return huma.Error404NotFound("account not found")
	case errors.Is(err, adminops.ErrChallengeHasSolves):
		return huma.Error409Conflict("challenge has solves: solves are scoreboard history and are never deleted with a challenge — hide it instead")
	case errors.Is(err, adminops.ErrChallengeInUse):
		return huma.Error409Conflict("challenge has issued unique flags and cannot be deleted — hide it instead")
	case errors.Is(err, adminops.ErrLastAdmin):
		return huma.Error409Conflict("cannot demote the last admin")
	case errors.Is(err, adminops.ErrSelfBan):
		return huma.Error409Conflict("you cannot ban yourself")
	case errors.Is(err, adminops.ErrTagNotFound):
		return huma.Error404NotFound("tag not found")
	case errors.Is(err, adminops.ErrTagInUse):
		return huma.Error409Conflict("tag is attached to challenges: pass force=true to remove it from all of them")
	default:
		s.opts.Log.ErrorContext(ctx, action+" failed", "error", err)
		return huma.Error500InternalServerError("could not " + action)
	}
}

type adminChallengeBody struct {
	ID             int64     `json:"id"`
	Name           string    `json:"name"`
	Category       string    `json:"category"`
	Description    string    `json:"description"`
	Attribution    *string   `json:"attribution,omitempty"`
	ConnectionInfo *string   `json:"connection_info,omitempty"`
	Type           string    `json:"type"`
	State          string    `json:"state"`
	Value          int32     `json:"value"`
	Function       string    `json:"function"`
	Initial        *int32    `json:"initial,omitempty"`
	Minimum        *int32    `json:"minimum,omitempty"`
	Decay          *int32    `json:"decay,omitempty"`
	MaxAttempts    int32     `json:"max_attempts"`
	Logic          string    `json:"logic"`
	Position       int32     `json:"position"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type adminChallengeOutput struct {
	Body adminChallengeBody
}

func (s *Server) adminChallenge(c *db.Challenge) *adminChallengeOutput {
	return &adminChallengeOutput{Body: adminChallengeBody{
		ID: c.ID, Name: c.Name, Category: c.Category, Description: c.Description,
		Attribution: c.Attribution, ConnectionInfo: c.ConnectionInfo,
		Type: c.Type, State: c.State, Value: c.Value, Function: c.Function,
		Initial: c.Initial, Minimum: c.Minimum, Decay: c.Decay,
		MaxAttempts: c.MaxAttempts, Logic: c.Logic, Position: c.Position,
		CreatedAt: c.CreatedAt.Time, UpdatedAt: c.UpdatedAt.Time,
	}}
}

func adminFlag(f db.Flag) *adminFlagOutput {
	return &adminFlagOutput{Body: adminFlagBody{
		ID: f.ID, ChallengeID: f.ChallengeID, Type: f.Type,
		Content: f.Content, CaseInsensitive: f.CaseInsensitive,
	}}
}

func adminHint(h db.Hint) *adminHintOutput {
	return &adminHintOutput{Body: adminHintBody{
		ID: h.ID, ChallengeID: h.ChallengeID, Title: h.Title,
		Content: h.Content, Cost: h.Cost, Position: h.Position,
	}}
}

type adminCreateChallengeInput struct {
	Body struct {
		Name           string  `json:"name" minLength:"1" maxLength:"256"`
		Category       string  `json:"category" minLength:"1" maxLength:"128"`
		Description    string  `json:"description,omitempty"`
		Attribution    *string `json:"attribution,omitempty"`
		ConnectionInfo *string `json:"connection_info,omitempty"`
		State          string  `json:"state,omitempty" enum:"visible,hidden" default:"visible"`
		Value          int32   `json:"value" minimum:"0"`
		Function       string  `json:"function,omitempty" enum:"static,linear,logarithmic" default:"static"`
		Initial        *int32  `json:"initial,omitempty" minimum:"0"`
		Minimum        *int32  `json:"minimum,omitempty" minimum:"0"`
		Decay          *int32  `json:"decay,omitempty" minimum:"1"`
		MaxAttempts    int32   `json:"max_attempts,omitempty" minimum:"0"`
		Logic          string  `json:"logic,omitempty" enum:"any,all" default:"any"`
		Position       int32   `json:"position,omitempty"`
	}
}

type adminUpdateChallengeInput struct {
	ID   int64 `path:"id"`
	Body struct {
		Name        *string `json:"name,omitempty" minLength:"1" maxLength:"256"`
		Category    *string `json:"category,omitempty" minLength:"1" maxLength:"128"`
		Description *string `json:"description,omitempty"`
		// The nullable columns are three-state: omit to keep, null to clear, a value to set. The
		// dynamic-params CHECK arbitrates the result, so clearing initial/minimum/decay on a decayed
		// challenge is refused with a 422 rather than silently stored.
		Attribution    Optional[string] `json:"attribution,omitempty"`
		ConnectionInfo Optional[string] `json:"connection_info,omitempty"`
		Value          *int32           `json:"value,omitempty" minimum:"0"`
		Function       *string          `json:"function,omitempty" enum:"static,linear,logarithmic"`
		Initial        Optional[int32]  `json:"initial,omitempty" minimum:"0"`
		Minimum        Optional[int32]  `json:"minimum,omitempty" minimum:"0"`
		Decay          Optional[int32]  `json:"decay,omitempty" minimum:"1"`
		MaxAttempts    *int32           `json:"max_attempts,omitempty" minimum:"0"`
		Logic          *string          `json:"logic,omitempty" enum:"any,all"`
		Position       *int32           `json:"position,omitempty"`
	}
}

type adminChallengeStateInput struct {
	ID   int64 `path:"id"`
	Body struct {
		State string `json:"state" enum:"visible,hidden"`
	}
}

type adminReorderInput struct {
	Body struct {
		Items []struct {
			ID       int64 `json:"id"`
			Position int32 `json:"position"`
		} `json:"items" minItems:"1"`
	}
}

type adminReorderOutput struct {
	Body struct {
		Reordered int `json:"reordered"`
	}
}

type adminDeleteOutput struct{}

type adminFlagBody struct {
	ID              int64  `json:"id"`
	ChallengeID     int64  `json:"challenge_id"`
	Type            string `json:"type"`
	Content         string `json:"content"`
	CaseInsensitive bool   `json:"case_insensitive"`
}

type adminFlagOutput struct {
	Body adminFlagBody
}

type adminAddFlagInput struct {
	ID   int64 `path:"id"`
	Body struct {
		Type            string `json:"type" enum:"static,regex"`
		Content         string `json:"content" minLength:"1"`
		CaseInsensitive bool   `json:"case_insensitive,omitempty"`
	}
}

type adminUpdateFlagInput struct {
	ID     int64 `path:"id"`
	FlagID int64 `path:"flagID"`
	Body   struct {
		Type            *string `json:"type,omitempty" enum:"static,regex"`
		Content         *string `json:"content,omitempty" minLength:"1"`
		CaseInsensitive *bool   `json:"case_insensitive,omitempty"`
	}
}

type adminFlagPathInput struct {
	ID     int64 `path:"id"`
	FlagID int64 `path:"flagID"`
}

type adminHintBody struct {
	ID          int64   `json:"id"`
	ChallengeID int64   `json:"challenge_id"`
	Title       *string `json:"title,omitempty"`
	Content     string  `json:"content"`
	Cost        int32   `json:"cost"`
	Position    int32   `json:"position"`
}

type adminHintOutput struct {
	Body adminHintBody
}

type adminAddHintInput struct {
	ID   int64 `path:"id"`
	Body struct {
		Title    *string `json:"title,omitempty" maxLength:"256"`
		Content  string  `json:"content" minLength:"1"`
		Cost     int32   `json:"cost,omitempty" minimum:"0"`
		Position int32   `json:"position,omitempty"`
	}
}

type adminUpdateHintInput struct {
	ID     int64 `path:"id"`
	HintID int64 `path:"hintID"`
	Body   struct {
		Title    *string `json:"title,omitempty" maxLength:"256"`
		Content  *string `json:"content,omitempty" minLength:"1"`
		Cost     *int32  `json:"cost,omitempty" minimum:"0"`
		Position *int32  `json:"position,omitempty"`
	}
}

type adminHintPathInput struct {
	ID     int64 `path:"id"`
	HintID int64 `path:"hintID"`
}

func (s *Server) registerAdminChallenges() {
	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-create-challenge", Method: http.MethodPost, Path: "/challenges",
		DefaultStatus: http.StatusCreated,
		Summary:       "Create a challenge", Tags: []string{"admin/challenges"},
	}, s.adminCreateChallenge)

	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-update-challenge", Method: http.MethodPatch, Path: "/challenges/{id}",
		Summary: "Update a challenge (partial)", Tags: []string{"admin/challenges"},
	}, s.adminUpdateChallenge)

	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-set-challenge-state", Method: http.MethodPut, Path: "/challenges/{id}/state",
		Summary: "Show or hide a challenge", Tags: []string{"admin/challenges"},
	}, s.adminSetChallengeState)

	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-reorder-challenges", Method: http.MethodPut, Path: "/challenges/order",
		Summary: "Set the board position of many challenges at once", Tags: []string{"admin/challenges"},
	}, s.adminReorderChallenges)

	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-delete-challenge", Method: http.MethodDelete, Path: "/challenges/{id}",
		DefaultStatus: http.StatusNoContent,
		Summary:       "Delete a challenge (refused while it has solves)", Tags: []string{"admin/challenges"},
	}, s.adminDeleteChallenge)

	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-add-flag", Method: http.MethodPost, Path: "/challenges/{id}/flags",
		DefaultStatus: http.StatusCreated,
		Summary:       "Add a flag", Tags: []string{"admin/flags"},
	}, s.adminAddFlag)

	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-update-flag", Method: http.MethodPatch, Path: "/challenges/{id}/flags/{flagID}",
		Summary: "Update a flag (partial)", Tags: []string{"admin/flags"},
	}, s.adminUpdateFlag)

	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-delete-flag", Method: http.MethodDelete, Path: "/challenges/{id}/flags/{flagID}",
		DefaultStatus: http.StatusNoContent,
		Summary:       "Delete a flag", Tags: []string{"admin/flags"},
	}, s.adminDeleteFlag)

	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-add-hint", Method: http.MethodPost, Path: "/challenges/{id}/hints",
		DefaultStatus: http.StatusCreated,
		Summary:       "Add a hint", Tags: []string{"admin/hints"},
	}, s.adminAddHint)

	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-update-hint", Method: http.MethodPatch, Path: "/challenges/{id}/hints/{hintID}",
		Summary: "Update a hint (partial)", Tags: []string{"admin/hints"},
	}, s.adminUpdateHint)

	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-delete-hint", Method: http.MethodDelete, Path: "/challenges/{id}/hints/{hintID}",
		DefaultStatus: http.StatusNoContent,
		Summary:       "Delete a hint", Tags: []string{"admin/hints"},
	}, s.adminDeleteHint)
}

func (s *Server) adminCreateChallenge(ctx context.Context, in *adminCreateChallengeInput) (*adminChallengeOutput, error) {
	b := in.Body
	c, err := s.opts.AdminOps.CreateChallenge(ctx, s.adminActor(ctx), adminops.NewChallenge{
		Name: b.Name, Category: b.Category, Description: b.Description,
		Attribution: b.Attribution, ConnectionInfo: b.ConnectionInfo,
		State: b.State, Value: b.Value, Function: b.Function,
		Initial: b.Initial, Minimum: b.Minimum, Decay: b.Decay,
		MaxAttempts: b.MaxAttempts, Logic: b.Logic, Position: b.Position,
	})
	if err != nil {
		return nil, s.adminOpsError(ctx, err, "create challenge")
	}
	return s.adminChallenge(&c), nil
}

func (s *Server) adminUpdateChallenge(ctx context.Context, in *adminUpdateChallengeInput) (*adminChallengeOutput, error) {
	b := in.Body
	attribution, clearAttribution := b.Attribution.split()
	connectionInfo, clearConnectionInfo := b.ConnectionInfo.split()
	initial, clearInitial := b.Initial.split()
	minimum, clearMinimum := b.Minimum.split()
	decay, clearDecay := b.Decay.split()

	c, err := s.opts.AdminOps.UpdateChallenge(ctx, s.adminActor(ctx), in.ID, adminops.ChallengePatch{
		Name: b.Name, Category: b.Category, Description: b.Description,
		Attribution: attribution, ConnectionInfo: connectionInfo,
		Value: b.Value, Function: b.Function,
		Initial: initial, Minimum: minimum, Decay: decay,
		MaxAttempts: b.MaxAttempts, Logic: b.Logic, Position: b.Position,
		ClearAttribution:    clearAttribution,
		ClearConnectionInfo: clearConnectionInfo,
		ClearInitial:        clearInitial,
		ClearMinimum:        clearMinimum,
		ClearDecay:          clearDecay,
	})
	if err != nil {
		return nil, s.adminOpsError(ctx, err, "update challenge")
	}
	return s.adminChallenge(&c), nil
}

func (s *Server) adminSetChallengeState(ctx context.Context, in *adminChallengeStateInput) (*adminChallengeOutput, error) {
	c, err := s.opts.AdminOps.SetChallengeState(ctx, s.adminActor(ctx), in.ID, in.Body.State)
	if err != nil {
		return nil, s.adminOpsError(ctx, err, "set challenge state")
	}
	return s.adminChallenge(&c), nil
}

func (s *Server) adminReorderChallenges(ctx context.Context, in *adminReorderInput) (*adminReorderOutput, error) {
	orders := make([]adminops.ChallengeOrder, len(in.Body.Items))
	for i, it := range in.Body.Items {
		orders[i] = adminops.ChallengeOrder{ID: it.ID, Position: it.Position}
	}
	if err := s.opts.AdminOps.ReorderChallenges(ctx, s.adminActor(ctx), orders); err != nil {
		return nil, s.adminOpsError(ctx, err, "reorder challenges")
	}
	out := &adminReorderOutput{}
	out.Body.Reordered = len(orders)
	return out, nil
}

func (s *Server) adminDeleteChallenge(ctx context.Context, in *challengeIDInput) (*adminDeleteOutput, error) {
	if err := s.opts.AdminOps.DeleteChallenge(ctx, s.adminActor(ctx), in.ID); err != nil {
		return nil, s.adminOpsError(ctx, err, "delete challenge")
	}
	return &adminDeleteOutput{}, nil
}

func (s *Server) adminAddFlag(ctx context.Context, in *adminAddFlagInput) (*adminFlagOutput, error) {
	f, err := s.opts.AdminOps.AddFlag(ctx, s.adminActor(ctx), in.ID, adminops.NewFlag{
		Type: in.Body.Type, Content: in.Body.Content, CaseInsensitive: in.Body.CaseInsensitive,
	})
	if err != nil {
		return nil, s.adminOpsError(ctx, err, "add flag")
	}
	return adminFlag(f), nil
}

func (s *Server) adminUpdateFlag(ctx context.Context, in *adminUpdateFlagInput) (*adminFlagOutput, error) {
	f, err := s.opts.AdminOps.UpdateFlag(ctx, s.adminActor(ctx), in.ID, in.FlagID, adminops.FlagPatch{
		Type: in.Body.Type, Content: in.Body.Content, CaseInsensitive: in.Body.CaseInsensitive,
	})
	if err != nil {
		return nil, s.adminOpsError(ctx, err, "update flag")
	}
	return adminFlag(f), nil
}

func (s *Server) adminDeleteFlag(ctx context.Context, in *adminFlagPathInput) (*adminDeleteOutput, error) {
	if err := s.opts.AdminOps.DeleteFlag(ctx, s.adminActor(ctx), in.ID, in.FlagID); err != nil {
		return nil, s.adminOpsError(ctx, err, "delete flag")
	}
	return &adminDeleteOutput{}, nil
}

func (s *Server) adminAddHint(ctx context.Context, in *adminAddHintInput) (*adminHintOutput, error) {
	h, err := s.opts.AdminOps.AddHint(ctx, s.adminActor(ctx), in.ID, adminops.NewHint{
		Title: in.Body.Title, Content: in.Body.Content, Cost: in.Body.Cost, Position: in.Body.Position,
	})
	if err != nil {
		return nil, s.adminOpsError(ctx, err, "add hint")
	}
	return adminHint(h), nil
}

func (s *Server) adminUpdateHint(ctx context.Context, in *adminUpdateHintInput) (*adminHintOutput, error) {
	h, err := s.opts.AdminOps.UpdateHint(ctx, s.adminActor(ctx), in.ID, in.HintID, adminops.HintPatch{
		Title: in.Body.Title, Content: in.Body.Content, Cost: in.Body.Cost, Position: in.Body.Position,
	})
	if err != nil {
		return nil, s.adminOpsError(ctx, err, "update hint")
	}
	return adminHint(h), nil
}

func (s *Server) adminDeleteHint(ctx context.Context, in *adminHintPathInput) (*adminDeleteOutput, error) {
	if err := s.opts.AdminOps.DeleteHint(ctx, s.adminActor(ctx), in.ID, in.HintID); err != nil {
		return nil, s.adminOpsError(ctx, err, "delete hint")
	}
	return &adminDeleteOutput{}, nil
}
