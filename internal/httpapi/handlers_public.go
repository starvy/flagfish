package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/starvy/flagfish/internal/domain/policy"
)

// instanceOutput is what the SPA needs before anyone logs in: the branding, and the
// shape of the world it is rendering. The account mode decides whether the team UI
// exists at all, and the clock decides what a page may show — the client must be told
// both, not left to infer them from the 403s it collects.
//
// Nothing here is a secret: every field is visible to an anonymous visitor anyway, in
// the countdown, the register form, or the absence of a team tab.
type instanceOutput struct {
	Body struct {
		CTFName     string            `json:"ctf_name"`
		Theme       string            `json:"theme"`
		ThemeTokens map[string]string `json:"theme_tokens,omitempty"`

		Mode string `json:"mode" enum:"users,teams"`

		Start  *time.Time `json:"start,omitempty"`
		End    *time.Time `json:"end,omitempty"`
		Freeze *time.Time `json:"freeze,omitempty"`

		Paused       bool `json:"paused"`
		TeamCreation bool `json:"team_creation"`
		VerifyEmails bool `json:"verify_emails"`

		RegistrationVisibility string `json:"registration_visibility" enum:"public,private,mlc"`
	}
}

// registerPublic wires the anonymous branding endpoint. It carries ClassThemeAsset:
// like the static assets, it must answer before login, before setup, and for a
// banned account — the page that tells you any of those still has to render themed.
func (s *Server) registerPublic() {
	Register(s.Public, policy.ClassThemeAsset, huma.Operation{
		OperationID: "instance", Method: http.MethodGet, Path: "/instance",
		Summary: "Public instance branding and theme", Tags: []string{"instance"},
	}, s.instanceInfo)
}

func (s *Server) instanceInfo(ctx context.Context, _ *struct{}) (*instanceOutput, error) {
	snap := s.opts.Config.Current()

	out := &instanceOutput{}
	out.Body.CTFName = snap.CTFName
	out.Body.Theme = snap.Theme
	out.Body.Mode = snap.Mode.String()
	out.Body.Start = snap.Start
	out.Body.End = snap.End
	out.Body.Freeze = snap.Freeze
	out.Body.Paused = snap.Paused
	out.Body.TeamCreation = snap.TeamCreation
	out.Body.VerifyEmails = snap.VerifyEmails
	out.Body.RegistrationVisibility = snap.RegistrationVis.String()

	if snap.ThemeTokens != "" {
		var tokens map[string]string
		if err := json.Unmarshal([]byte(snap.ThemeTokens), &tokens); err != nil {
			// The blob is validated at the write, so this cannot happen from operator
			// input; if it ever does, serve the branding without the override rather
			// than fail the page that depends on it.
			s.opts.Log.ErrorContext(ctx, "stored theme_tokens did not parse", "error", err)
		} else {
			out.Body.ThemeTokens = tokens
		}
	}
	return out, nil
}
