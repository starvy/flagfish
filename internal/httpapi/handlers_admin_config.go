package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/starvy/flagfish/internal/audit"
	"github.com/starvy/flagfish/internal/config"
	"github.com/starvy/flagfish/internal/domain/policy"
)

// adminConfigInput is the subset of config knobs this endpoint edits. Every field is a pointer:
// absent means "leave it", so a PATCH touches only the keys the operator sent. The mode is not
// here — it is fixed at setup and immutable, and letting it be PATCHed would silently change what
// every account-scoped query means.
type adminConfigInput struct {
	Body struct {
		Name        *string `json:"name,omitempty"`
		Description *string `json:"description,omitempty"`

		// Theme is the instance default theme name; ThemeTokens is a JSON object of
		// semantic-token overrides (validated on write). Both are public branding the
		// anonymous /instance endpoint serves.
		Theme       *string `json:"theme,omitempty"`
		ThemeTokens *string `json:"theme_tokens,omitempty"`

		// Three-state: an omitted key keeps the current time, an explicit null clears it (the event
		// loses its start/end/freeze), and a value sets it.
		Start  Optional[time.Time] `json:"start,omitempty"`
		End    Optional[time.Time] `json:"end,omitempty"`
		Freeze Optional[time.Time] `json:"freeze,omitempty"`

		ChallengeVisibility    *string `json:"challenge_visibility,omitempty" enum:"public,private"`
		ScoreVisibility        *string `json:"score_visibility,omitempty" enum:"public,private,hidden"`
		AccountVisibility      *string `json:"account_visibility,omitempty" enum:"public,private"`
		RegistrationVisibility *string `json:"registration_visibility,omitempty" enum:"public,private,mlc"`
	}
}

type adminConfigOutput struct {
	Body struct {
		Name        string `json:"name"`
		Description string `json:"description"`

		Theme       string `json:"theme"`
		ThemeTokens string `json:"theme_tokens,omitempty"`

		Start  *time.Time `json:"start,omitempty"`
		End    *time.Time `json:"end,omitempty"`
		Freeze *time.Time `json:"freeze,omitempty"`

		ChallengeVisibility    string `json:"challenge_visibility"`
		ScoreVisibility        string `json:"score_visibility"`
		AccountVisibility      string `json:"account_visibility"`
		RegistrationVisibility string `json:"registration_visibility"`
	}
}

func (s *Server) registerAdminConfig() {
	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-get-config", Method: http.MethodGet, Path: "/config",
		Summary: "Read the editable config knobs", Tags: []string{"admin/config"},
	}, s.adminGetConfig)

	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-update-config", Method: http.MethodPatch, Path: "/config",
		Summary: "Update config knobs (partial)", Tags: []string{"admin/config"},
	}, s.adminUpdateConfig)
}

func (s *Server) adminGetConfig(_ context.Context, _ *struct{}) (*adminConfigOutput, error) {
	return configOutput(s.opts.Config.Current()), nil
}

func (s *Server) adminUpdateConfig(ctx context.Context, in *adminConfigInput) (*adminConfigOutput, error) {
	kv := map[string]string{}
	putString := func(key string, v *string) {
		if v != nil {
			kv[key] = *v
		}
	}
	// Clearing writes the empty string: an empty config row reads back as unset, which is exactly a
	// nil start/end/freeze. Set still validates the merged result, so an incoherent combination is
	// refused whole.
	putTime := func(key string, o Optional[time.Time]) {
		t, cleared := o.split()
		switch {
		case cleared:
			kv[key] = ""
		case t != nil:
			kv[key] = strconv.FormatInt(t.Unix(), 10)
		}
	}

	putString("ctf_name", in.Body.Name)
	putString("ctf_description", in.Body.Description)
	putString("ctf_theme", in.Body.Theme)
	putString("theme_tokens", in.Body.ThemeTokens)
	putTime("start", in.Body.Start)
	putTime("end", in.Body.End)
	putTime("freeze", in.Body.Freeze)
	putString("challenge_visibility", in.Body.ChallengeVisibility)
	putString("score_visibility", in.Body.ScoreVisibility)
	putString("account_visibility", in.Body.AccountVisibility)
	putString("registration_visibility", in.Body.RegistrationVisibility)

	// Set validates the merged result before it writes, so an incoherent combination (freeze after
	// end, say) is refused whole rather than stored and then found broken on the next boot. The
	// actor rides on the context because the config store's upsert predates the audit parameter.
	if err := s.opts.Config.Set(audit.WithActor(ctx, s.adminActor(ctx)), kv); err != nil {
		if errors.Is(err, config.ErrRejected) {
			// The rejection text names the offending key and value and is written for the operator;
			// it never carries database internals, so it is safe to return verbatim.
			return nil, huma.Error422UnprocessableEntity(err.Error())
		}
		s.opts.Log.ErrorContext(ctx, "update config failed", "error", err)
		return nil, huma.Error500InternalServerError("could not update config")
	}
	return configOutput(s.opts.Config.Current()), nil
}

func configOutput(snap *config.Snapshot) *adminConfigOutput {
	out := &adminConfigOutput{}
	out.Body.Name = snap.CTFName
	out.Body.Description = snap.CTFDescription
	out.Body.Theme = snap.Theme
	out.Body.ThemeTokens = snap.ThemeTokens
	out.Body.Start = snap.Start
	out.Body.End = snap.End
	out.Body.Freeze = snap.Freeze
	out.Body.ChallengeVisibility = snap.ChallengeVis.String()
	out.Body.ScoreVisibility = snap.ScoreVis.String()
	out.Body.AccountVisibility = snap.AccountVis.String()
	out.Body.RegistrationVisibility = snap.RegistrationVis.String()
	return out
}
