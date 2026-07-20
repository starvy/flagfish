package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/starvy/flagfish/internal/adminops"
	"github.com/starvy/flagfish/internal/audit"
	"github.com/starvy/flagfish/internal/db"
	"github.com/starvy/flagfish/internal/domain/policy"
	"github.com/starvy/flagfish/internal/domain/prereq"
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
	case errors.Is(err, adminops.ErrTeamNotFound):
		return huma.Error404NotFound("team not found")
	case errors.Is(err, adminops.ErrBracketNotFound):
		return huma.Error404NotFound("bracket not found")
	case errors.Is(err, adminops.ErrAccountNotFound):
		return huma.Error404NotFound("account not found")
	case errors.Is(err, adminops.ErrChallengeHasSolves):
		return huma.Error409Conflict("challenge has solves: solves are scoreboard history and are never deleted with a challenge — hide it instead")
	case errors.Is(err, adminops.ErrChallengeHasHistory):
		return huma.Error409Conflict("challenge has recorded attempts, awards or hint unlocks: gameplay history is never deleted with a challenge — hide it instead")
	case errors.Is(err, adminops.ErrChallengeInUse):
		return huma.Error409Conflict("challenge has issued unique flags and cannot be deleted — hide it instead")
	case errors.Is(err, adminops.ErrHintUnlocked):
		return huma.Error409Conflict("hint has been unlocked: players paid for it, and the unlock and its charge stay on the ledger")
	case errors.Is(err, adminops.ErrLastAdmin):
		return huma.Error409Conflict("cannot demote the last admin")
	case errors.Is(err, adminops.ErrSelfBan):
		return huma.Error409Conflict("you cannot ban yourself")
	case errors.Is(err, adminops.ErrSelfTeamBan):
		return huma.Error409Conflict("you cannot ban your own team")
	case errors.Is(err, adminops.ErrTagNotFound):
		return huma.Error404NotFound("tag not found")
	case errors.Is(err, adminops.ErrTagInUse):
		return huma.Error409Conflict("tag is attached to challenges: pass force=true to remove it from all of them")
	case errors.Is(err, adminops.ErrTagAlreadyAttached):
		return huma.Error409Conflict("the challenge already carries this tag")
	default:
		s.opts.Log.ErrorContext(ctx, action+" failed", "error", err)
		return huma.Error500InternalServerError("could not " + action)
	}
}

type adminChallengeBody struct {
	ID              int64     `json:"id"`
	Name            string    `json:"name"`
	Category        string    `json:"category"`
	Description     string    `json:"description"`
	Attribution     *string   `json:"attribution,omitempty"`
	ConnectionInfo  *string   `json:"connection_info,omitempty"`
	Type            string    `json:"type"`
	State           string    `json:"state"`
	Value           int32     `json:"value"`
	Function        string    `json:"function"`
	Initial         *int32    `json:"initial,omitempty"`
	Minimum         *int32    `json:"minimum,omitempty"`
	Decay           *int32    `json:"decay,omitempty"`
	MaxAttempts     int32     `json:"max_attempts"`
	Logic           string    `json:"logic"`
	Position        int32     `json:"position"`
	FirstBlood      string    `json:"first_blood"`
	FirstBloodBonus *int32    `json:"first_blood_bonus,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`

	Requirements adminRequirementsBody `json:"requirements"`
}

// adminRequirementsBody speaks the visibility vocabulary, not the stored anonymize spelling.
type adminRequirementsBody struct {
	Prerequisites []int64 `json:"prerequisites"`
	Visibility    string  `json:"visibility" enum:"hidden,masked,preview"`
}

type adminChallengeOutput struct {
	Body adminChallengeBody
}

func (s *Server) adminChallenge(c *db.Challenge) (*adminChallengeOutput, error) {
	reqs, err := prereq.Parse(c.Requirements)
	if err != nil {
		return nil, fmt.Errorf("challenge %d: %w", c.ID, err)
	}
	prereqs := reqs.Prerequisites
	if prereqs == nil {
		prereqs = []int64{}
	}
	return &adminChallengeOutput{Body: adminChallengeBody{
		ID: c.ID, Name: c.Name, Category: c.Category, Description: c.Description,
		Attribution: c.Attribution, ConnectionInfo: c.ConnectionInfo,
		Type: c.Type, State: c.State, Value: c.Value, Function: c.Function,
		Initial: c.Initial, Minimum: c.Minimum, Decay: c.Decay,
		MaxAttempts: c.MaxAttempts, Logic: c.Logic, Position: c.Position,
		FirstBlood: c.FirstBlood, FirstBloodBonus: c.FirstBloodBonus,
		CreatedAt: c.CreatedAt.Time, UpdatedAt: c.UpdatedAt.Time,
		Requirements: adminRequirementsBody{
			Prerequisites: prereqs,
			Visibility:    reqs.Visibility.Name(),
		},
	}}, nil
}

func adminFlag(f db.Flag) *adminFlagOutput {
	return &adminFlagOutput{Body: adminFlagBody{
		ID: f.ID, ChallengeID: f.ChallengeID, Type: f.Type,
		Content: f.Content, CaseInsensitive: f.CaseInsensitive,
	}}
}

func adminHint(h *db.Hint) (*adminHintOutput, error) {
	reqs, err := prereq.Parse(h.Requirements)
	if err != nil {
		return nil, fmt.Errorf("hint %d: %w", h.ID, err)
	}
	prereqs := reqs.Prerequisites
	if prereqs == nil {
		prereqs = []int64{}
	}
	return &adminHintOutput{Body: adminHintBody{
		ID: h.ID, ChallengeID: h.ChallengeID, Title: h.Title,
		Content: h.Content, Cost: h.Cost, Position: h.Position,
		Prerequisites: prereqs,
	}}, nil
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
		// FirstBloodBonus is required with first_blood "bonus" and refused with any other mode;
		// the pairing CHECK arbitrates, so the two cannot disagree in storage.
		FirstBlood      string `json:"first_blood,omitempty" enum:"none,announce,bonus" default:"none"`
		FirstBloodBonus *int32 `json:"first_blood_bonus,omitempty" minimum:"1"`
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
		// Switching bonus → announce/none must clear the bonus in the same PATCH (explicit null);
		// the pairing CHECK refuses the half-switched row.
		FirstBlood      *string         `json:"first_blood,omitempty" enum:"none,announce,bonus"`
		FirstBloodBonus Optional[int32] `json:"first_blood_bonus,omitempty" minimum:"1"`
	}
}

type adminChallengeStateInput struct {
	ID   int64 `path:"id"`
	Body struct {
		State string `json:"state" enum:"visible,hidden"`
	}
}

type adminSetRequirementsInput struct {
	ID   int64 `path:"id"`
	Body struct {
		Prerequisites []int64 `json:"prerequisites" maxItems:"64"`
		Visibility    string  `json:"visibility,omitempty" enum:"hidden,masked,preview" default:"hidden" doc:"How the challenge appears while its prerequisites are unmet: hidden (absent), masked (listed as ???), or preview (real name, no solvable content)."`
	}
}

type adminSetRequirementsOutput struct {
	Body struct {
		Challenge adminChallengeBody `json:"challenge"`
		// Warnings flag stored-but-suspect states, e.g. a prerequisite cycle. The write went in.
		Warnings []string `json:"warnings,omitempty"`
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
	ID            int64   `json:"id"`
	ChallengeID   int64   `json:"challenge_id"`
	Title         *string `json:"title,omitempty"`
	Content       string  `json:"content"`
	Cost          int32   `json:"cost"`
	Position      int32   `json:"position"`
	Prerequisites []int64 `json:"prerequisites"`
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
		// Prerequisites are hint ids on the same challenge that must be unlocked before this one.
		Prerequisites []int64 `json:"prerequisites,omitempty" maxItems:"64"`
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
		// The whole set replaces: omit to keep, [] or null to clear.
		Prerequisites Optional[[]int64] `json:"prerequisites,omitempty" maxItems:"64"`
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
		OperationID: "admin-set-challenge-requirements", Method: http.MethodPut, Path: "/challenges/{id}/requirements",
		Summary: "Set a challenge's prerequisites (whole-value replace)", Tags: []string{"admin/challenges"},
	}, s.adminSetChallengeRequirements)

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
		FirstBlood: b.FirstBlood, FirstBloodBonus: b.FirstBloodBonus,
	})
	if err != nil {
		return nil, s.adminOpsError(ctx, err, "create challenge")
	}
	out, err := s.adminChallenge(&c)
	if err != nil {
		return nil, s.adminOpsError(ctx, err, "create challenge")
	}
	return out, nil
}

func (s *Server) adminUpdateChallenge(ctx context.Context, in *adminUpdateChallengeInput) (*adminChallengeOutput, error) {
	b := in.Body
	attribution, clearAttribution := b.Attribution.split()
	connectionInfo, clearConnectionInfo := b.ConnectionInfo.split()
	initial, clearInitial := b.Initial.split()
	minimum, clearMinimum := b.Minimum.split()
	decay, clearDecay := b.Decay.split()
	firstBloodBonus, clearFirstBloodBonus := b.FirstBloodBonus.split()

	c, err := s.opts.AdminOps.UpdateChallenge(ctx, s.adminActor(ctx), in.ID, adminops.ChallengePatch{
		Name: b.Name, Category: b.Category, Description: b.Description,
		Attribution: attribution, ConnectionInfo: connectionInfo,
		Value: b.Value, Function: b.Function,
		Initial: initial, Minimum: minimum, Decay: decay,
		MaxAttempts: b.MaxAttempts, Logic: b.Logic, Position: b.Position,
		FirstBlood: b.FirstBlood, FirstBloodBonus: firstBloodBonus,
		ClearAttribution:     clearAttribution,
		ClearConnectionInfo:  clearConnectionInfo,
		ClearInitial:         clearInitial,
		ClearMinimum:         clearMinimum,
		ClearDecay:           clearDecay,
		ClearFirstBloodBonus: clearFirstBloodBonus,
	})
	if err != nil {
		return nil, s.adminOpsError(ctx, err, "update challenge")
	}
	out, err := s.adminChallenge(&c)
	if err != nil {
		return nil, s.adminOpsError(ctx, err, "update challenge")
	}
	return out, nil
}

func (s *Server) adminSetChallengeState(ctx context.Context, in *adminChallengeStateInput) (*adminChallengeOutput, error) {
	c, err := s.opts.AdminOps.SetChallengeState(ctx, s.adminActor(ctx), in.ID, in.Body.State)
	if err != nil {
		return nil, s.adminOpsError(ctx, err, "set challenge state")
	}
	out, err := s.adminChallenge(&c)
	if err != nil {
		return nil, s.adminOpsError(ctx, err, "set challenge state")
	}
	return out, nil
}

func (s *Server) adminSetChallengeRequirements(ctx context.Context, in *adminSetRequirementsInput) (*adminSetRequirementsOutput, error) {
	vis, err := prereq.VisibilityFromName(in.Body.Visibility)
	if err != nil {
		return nil, huma.Error422UnprocessableEntity(err.Error())
	}
	c, warnings, err := s.opts.AdminOps.SetChallengeRequirements(ctx, s.adminActor(ctx), in.ID, adminops.ChallengeRequirements{
		Prerequisites: in.Body.Prerequisites, Visibility: vis,
	})
	if err != nil {
		return nil, s.adminOpsError(ctx, err, "set challenge requirements")
	}
	ch, err := s.adminChallenge(&c)
	if err != nil {
		return nil, s.adminOpsError(ctx, err, "set challenge requirements")
	}
	out := &adminSetRequirementsOutput{}
	out.Body.Challenge = ch.Body
	out.Body.Warnings = warnings
	return out, nil
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
		Prerequisites: in.Body.Prerequisites,
	})
	if err != nil {
		return nil, s.adminOpsError(ctx, err, "add hint")
	}
	out, err := adminHint(&h)
	if err != nil {
		return nil, s.adminOpsError(ctx, err, "add hint")
	}
	return out, nil
}

func (s *Server) adminUpdateHint(ctx context.Context, in *adminUpdateHintInput) (*adminHintOutput, error) {
	prereqs, clearPrereqs := in.Body.Prerequisites.split()
	if clearPrereqs {
		prereqs = &[]int64{}
	}
	h, err := s.opts.AdminOps.UpdateHint(ctx, s.adminActor(ctx), in.ID, in.HintID, adminops.HintPatch{
		Title: in.Body.Title, Content: in.Body.Content, Cost: in.Body.Cost, Position: in.Body.Position,
		Prerequisites: prereqs,
	})
	if err != nil {
		return nil, s.adminOpsError(ctx, err, "update hint")
	}
	out, err := adminHint(&h)
	if err != nil {
		return nil, s.adminOpsError(ctx, err, "update hint")
	}
	return out, nil
}

func (s *Server) adminDeleteHint(ctx context.Context, in *adminHintPathInput) (*adminDeleteOutput, error) {
	if err := s.opts.AdminOps.DeleteHint(ctx, s.adminActor(ctx), in.ID, in.HintID); err != nil {
		return nil, s.adminOpsError(ctx, err, "delete hint")
	}
	return &adminDeleteOutput{}, nil
}
