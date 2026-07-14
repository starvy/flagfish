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
	} else {
		s.opts.Log.Warn("no accounts service configured: auth and token routes are not registered")
	}
	if s.opts.Catalog != nil {
		s.registerChallenges()
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
		s.registerAdminUsers()
		s.registerAdminBrackets()
		s.registerAdminTags()
		s.registerAdminAudit()
	}
	if s.opts.Anticheat != nil {
		s.registerAdminAnticheat()
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

// In teams mode the playing account is the team; in users mode TeamID stays nil.
func (s *Server) actor(ctx context.Context) gameplay.Actor {
	pr := AuthOf(ctx).Principal
	var teamID *int64
	if s.opts.Config.Current().Mode == account.ModeTeams {
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
