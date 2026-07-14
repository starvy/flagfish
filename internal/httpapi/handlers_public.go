package httpapi

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/starvy/flagfish/internal/domain/policy"
)

// instanceOutput is the public branding the SPA needs before anyone logs in: the CTF
// name for the page, the instance's chosen theme, and any admin token overrides.
type instanceOutput struct {
	Body struct {
		CTFName     string            `json:"ctf_name"`
		Theme       string            `json:"theme"`
		ThemeTokens map[string]string `json:"theme_tokens,omitempty"`
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
