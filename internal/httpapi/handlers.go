package httpapi

import (
	"context"
	"net/netip"
	"time"

	"github.com/starvy/flagfish/internal/domain/account"
	"github.com/starvy/flagfish/internal/domain/policy"
	"github.com/starvy/flagfish/internal/gameplay"
)

// freezeCutoff is the horizon a read must clamp to, or nil when this caller sees live data. Every
// reader derives its cutoff from this one freeze decision rather than re-deriving it from config.
func freezeCutoff(p policy.Policy) *time.Time {
	if policy.Frozen(p) {
		return p.E.FreezeAt
	}
	return nil
}

func (s *Server) registerRoutes() {
	// Registered unconditionally so the public branding contract is in the OpenAPI
	// document even when the doc is generated without a config manager wired.
	s.registerPublic()

	if s.opts.Accounts != nil {
		s.registerAuth()
		s.registerTokens()
		s.registerTeams()
		s.registerProfiles()
	} else {
		s.opts.Log.Warn("no accounts service configured: auth and token routes are not registered")
	}
	if s.opts.Catalog != nil {
		s.registerChallenges()
		s.registerPages()
	} else {
		s.opts.Log.Warn("no catalog service configured: challenge routes are not registered")
	}
	if s.opts.Gameplay != nil {
		s.registerSubmit()
	} else {
		s.opts.Log.Warn("no gameplay service configured: submit routes are not registered")
	}
	if s.opts.Board != nil {
		s.registerScoreboard()
		s.registerScoreboardDetail()
	} else {
		s.opts.Log.Warn("no board service configured: scoreboard routes are not registered")
	}
	if s.opts.Files != nil {
		s.registerFiles()
		if s.opts.AdminOps != nil {
			s.registerAdminFiles()
		}
	} else {
		s.opts.Log.Warn("no files service configured: file routes are not registered")
	}
	if s.opts.AdminOps != nil {
		s.registerAdminChallenges()
		s.registerAdminInstances()
		s.registerAdminUsers()
		s.registerAdminTeams()
		s.registerAdminTeamRoster()
		s.registerAdminBrackets()
		s.registerAdminTags()
		s.registerAdminAnnotations()
		s.registerAdminAwards()
		s.registerAdminFields()
		s.registerAdminPages()
		s.registerAdminAudit()
		s.registerAdminExport()
	}
	// Async backup/restore/import is its own service (tasks queue + object store), wired only in the
	// serve graph. Registered on its own so a doc-only build without it still emits every other route.
	if s.opts.Ops != nil {
		s.registerAdminOps()
	}
	if s.opts.Anticheat != nil {
		s.registerAdminAnticheat()
		s.registerAdminSubmissions()
	}
	if s.opts.Stats != nil {
		s.registerAdminStats()
	}
	if s.opts.Config != nil {
		s.registerAdminConfig()
	}
	// The list and admin publish need only the service; the live stream also needs the fan-out.
	// Register what the wired dependencies can actually serve.
	if s.opts.Notify != nil {
		s.registerAdminNotifications()
		s.registerNotifications()
		if s.opts.Broadcaster != nil {
			s.registerNotificationStream()
		} else {
			s.opts.Log.Warn("no broadcaster configured: notification stream is not registered")
		}
	} else {
		s.opts.Log.Warn("no notify service configured: notification routes are not registered")
	}
}

// In teams mode the playing account is the team; in users mode TeamID stays nil. A teamless player
// has no team either, and gets nil rather than a fabricated id 0: resolving an account still
// refuses that, but a query handed team_id = 0 would quietly read nobody's rows instead.
func (s *Server) actor(ctx context.Context) gameplay.Actor {
	pr := AuthOf(ctx).Principal
	var teamID *int64
	if s.opts.Config.Current().Mode == account.ModeTeams && pr.AccountID != 0 {
		id := int64(pr.AccountID)
		teamID = &id
	}
	return gameplay.Actor{UserID: pr.UserID, TeamID: teamID, IP: clientIPOf(ctx)}
}

func clientIPOf(ctx context.Context) *netip.Addr {
	if a, ok := ctx.Value(ctxClientIP).(netip.Addr); ok {
		return &a
	}
	return nil
}

func secureOf(ctx context.Context) bool {
	s, ok := ctx.Value(ctxSecure).(bool)
	return ok && s
}

// servedOverTLS reports whether the request provably arrived over TLS — directly, or forwarded
// https by a trusted proxy. Unlike secureOf it does not fold in the operator's secure-cookie
// default: HSTS is a promise the browser holds us to, so it is made only on evidence.
func servedOverTLS(ctx context.Context) bool {
	s, ok := ctx.Value(ctxServedTLS).(bool)
	return ok && s
}
