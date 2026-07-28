package httpapi

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
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

		// PortalView selects the player board. The enum is asserted against config's own list in
		// this package's tests, so the documented values and the accepted ones cannot drift.
		PortalView *string `json:"portal_view,omitempty" enum:"standard,globe"`

		// Three-state: an omitted key keeps the current time, an explicit null clears it (the event
		// loses its start/end/freeze), and a value sets it.
		Start  Optional[time.Time] `json:"start,omitempty"`
		End    Optional[time.Time] `json:"end,omitempty"`
		Freeze Optional[time.Time] `json:"freeze,omitempty"`

		ChallengeVisibility *string `json:"challenge_visibility,omitempty" enum:"public,private"`
		ScoreVisibility     *string `json:"score_visibility,omitempty" enum:"public,private,hidden"`
		AccountVisibility   *string `json:"account_visibility,omitempty" enum:"public,private"`
		// No "mlc": there is no MajorLeagueCyber sign-in in this binary, so offering it
		// would let an operator 404 their own registration form with nothing able to
		// create accounts behind it. config.Set refuses it too.
		RegistrationVisibility *string `json:"registration_visibility,omitempty" enum:"public,private"`

		// Pausing stops flag submissions for everyone, admins included; browsing and
		// hint unlocks keep working.
		Paused       *bool `json:"paused,omitempty"`
		VerifyEmails *bool `json:"verify_emails,omitempty"`
		ViewAfterCTF *bool `json:"view_after_ctf,omitempty"`
		TeamCreation *bool `json:"team_creation,omitempty"`

		// Caps. Zero means unlimited.
		NumUsers *int `json:"num_users,omitempty" minimum:"0"`
		NumTeams *int `json:"num_teams,omitempty" minimum:"0"`
		TeamSize *int `json:"team_size,omitempty" minimum:"0"`

		// SMTP. Server, username and password are secrets and set-only: a value
		// writes, "" clears, absent keeps. They are never echoed back — the GET
		// carries presence booleans instead, and rotation is overwrite.
		MailServer   *string `json:"mail_server,omitempty"`
		MailUsername *string `json:"mail_username,omitempty"`
		MailPassword *string `json:"mail_password,omitempty"`
		MailPort     *int    `json:"mail_port,omitempty" minimum:"0" maximum:"65535"`
		MailTLS      *bool   `json:"mail_tls,omitempty"`
		MailFrom     *string `json:"mailfrom_addr,omitempty"`

		// The webhook URL embeds its token, so it is a credential and set-only too.
		WebhookURL     *string   `json:"webhook_url,omitempty"`
		WebhookEnabled *bool     `json:"webhook_enabled,omitempty"`
		WebhookEvents  *[]string `json:"webhook_events,omitempty" enum:"first_blood,solve" minItems:"1"`
	}
}

type adminConfigOutput struct {
	Body struct {
		Name        string `json:"name"`
		Description string `json:"description"`

		Theme       string `json:"theme"`
		ThemeTokens string `json:"theme_tokens,omitempty"`
		PortalView  string `json:"portal_view"`

		Start  *time.Time `json:"start,omitempty"`
		End    *time.Time `json:"end,omitempty"`
		Freeze *time.Time `json:"freeze,omitempty"`

		ChallengeVisibility    string `json:"challenge_visibility"`
		ScoreVisibility        string `json:"score_visibility"`
		AccountVisibility      string `json:"account_visibility"`
		RegistrationVisibility string `json:"registration_visibility"`

		Paused       bool `json:"paused"`
		VerifyEmails bool `json:"verify_emails"`
		ViewAfterCTF bool `json:"view_after_ctf"`
		TeamCreation bool `json:"team_creation"`

		NumUsers int `json:"num_users"`
		NumTeams int `json:"num_teams"`
		TeamSize int `json:"team_size"`

		MailPort int    `json:"mail_port"`
		MailTLS  bool   `json:"mail_tls"`
		MailFrom string `json:"mailfrom_addr"`

		// Presence booleans, never values: the secrets are set-only.
		MailServerSet   bool `json:"mail_server_set"`
		MailUsernameSet bool `json:"mail_username_set"`
		MailPasswordSet bool `json:"mail_password_set"`

		WebhookEnabled bool     `json:"webhook_enabled"`
		WebhookEvents  []string `json:"webhook_events"`
		WebhookURLSet  bool     `json:"webhook_url_set"`

		// Problems are coherence violations the instance is currently serving with.
		// They can only arrive out of band — the write path refuses to create them —
		// and the operator reading this form is the one who can repair them.
		Problems []string `json:"problems,omitempty"`

		// Repairs are stored values this build cannot honour and has substituted for.
		// Unlike a problem, the instance is running normally on the substitute; what is
		// owed is a deliberate choice, and this form is where it gets made.
		Repairs []string `json:"repairs,omitempty"`
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
	return configOutput(s.opts.Config), nil
}

func (s *Server) adminUpdateConfig(ctx context.Context, in *adminConfigInput) (*adminConfigOutput, error) {
	kv := map[string]string{}
	putString := func(key string, v *string) {
		if v != nil {
			kv[key] = *v
		}
	}
	putBool := func(key string, v *bool) {
		if v != nil {
			kv[key] = strconv.FormatBool(*v)
		}
	}
	putInt := func(key string, v *int) {
		if v != nil {
			kv[key] = strconv.Itoa(*v)
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
	putString("ctf_portal_view", in.Body.PortalView)
	putTime("start", in.Body.Start)
	putTime("end", in.Body.End)
	putTime("freeze", in.Body.Freeze)
	putString("challenge_visibility", in.Body.ChallengeVisibility)
	putString("score_visibility", in.Body.ScoreVisibility)
	putString("account_visibility", in.Body.AccountVisibility)
	putString("registration_visibility", in.Body.RegistrationVisibility)
	putBool("paused", in.Body.Paused)
	putBool("verify_emails", in.Body.VerifyEmails)
	putBool("view_after_ctf", in.Body.ViewAfterCTF)
	putBool("team_creation", in.Body.TeamCreation)
	putInt("num_users", in.Body.NumUsers)
	putInt("num_teams", in.Body.NumTeams)
	putInt("team_size", in.Body.TeamSize)
	putString("mail_server", in.Body.MailServer)
	putString("mail_username", in.Body.MailUsername)
	putString("mail_password", in.Body.MailPassword)
	putInt("mail_port", in.Body.MailPort)
	putBool("mail_tls", in.Body.MailTLS)
	putString("mailfrom_addr", in.Body.MailFrom)
	putString("webhook_url", in.Body.WebhookURL)
	putBool("webhook_enabled", in.Body.WebhookEnabled)
	if in.Body.WebhookEvents != nil {
		kv["webhook_events"] = strings.Join(*in.Body.WebhookEvents, ",")
	}

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
	return configOutput(s.opts.Config), nil
}

func configOutput(cfg *config.Manager) *adminConfigOutput {
	snap := cfg.Current()
	out := &adminConfigOutput{}
	out.Body.Name = snap.CTFName
	out.Body.Description = snap.CTFDescription
	out.Body.Theme = snap.Theme
	out.Body.ThemeTokens = snap.ThemeTokens
	out.Body.PortalView = snap.PortalView.String()
	out.Body.Start = snap.Start
	out.Body.End = snap.End
	out.Body.Freeze = snap.Freeze
	out.Body.ChallengeVisibility = snap.ChallengeVis.String()
	out.Body.ScoreVisibility = snap.ScoreVis.String()
	out.Body.AccountVisibility = snap.AccountVis.String()
	out.Body.RegistrationVisibility = snap.RegistrationVis.String()
	out.Body.Paused = snap.Paused
	out.Body.VerifyEmails = snap.VerifyEmails
	out.Body.ViewAfterCTF = snap.ViewAfterCTF
	out.Body.TeamCreation = snap.TeamCreation
	out.Body.NumUsers = snap.NumUsers
	out.Body.NumTeams = snap.NumTeams
	out.Body.TeamSize = snap.TeamSize
	out.Body.MailPort = snap.MailPort
	out.Body.MailTLS = snap.MailTLS
	out.Body.MailFrom = snap.MailFrom
	out.Body.MailServerSet = snap.MailServer != ""
	out.Body.MailUsernameSet = snap.MailUsername != ""
	out.Body.MailPasswordSet = snap.MailPassword != ""
	out.Body.WebhookEnabled = snap.WebhookEnabled
	out.Body.WebhookEvents = webhookEventNames(snap.WebhookEvents)
	out.Body.WebhookURLSet = snap.WebhookURL != ""
	out.Body.Problems = cfg.Problems()
	out.Body.Repairs = cfg.Repairs()
	return out
}

// webhookEventNames flattens the set into a sorted list; only enabled events count,
// because the defaults keep disabled entries in the map.
func webhookEventNames(set config.WebhookEventSet) []string {
	out := make([]string, 0, len(set))
	for e, on := range set {
		if on {
			out = append(out, string(e))
		}
	}
	slices.Sort(out)
	return out
}
