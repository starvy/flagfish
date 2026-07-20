package mail

import (
	"context"
	"log/slog"

	"go.uber.org/fx"

	"github.com/starvy/flagfish/internal/config"
)

// Module provides the Mailer the email worker sends through.
var Module = fx.Module(
	"mail",
	fx.Provide(newMailer),
)

func newMailer(cfg *config.Manager, log *slog.Logger) Mailer {
	if cfg.Current().MailServer == "" {
		log.Warn("mail is not configured (mail_server is unset); outgoing email will fail until it is")
	}
	return &liveMailer{cfg: cfg}
}

// liveMailer reads the CURRENT config snapshot on every Send, so a runtime mail
// edit takes effect on the next email instead of the next restart. Rebuilding the
// SMTP client is only validation; the dial was per-send all along.
type liveMailer struct {
	cfg *config.Manager
}

func (m *liveMailer) Send(ctx context.Context, to, subject, body string) error {
	snap := m.cfg.Current()
	if snap.MailServer == "" {
		// Still a hard error: queued mail retries loudly instead of vanishing.
		return Unconfigured{}.Send(ctx, to, subject, body)
	}
	s, err := New(Config{
		Host:     snap.MailServer,
		Port:     snap.MailPort,
		Username: snap.MailUsername,
		Password: snap.MailPassword,
		StartTLS: snap.MailTLS,
		From:     snap.MailFrom,
	})
	if err != nil {
		return err
	}
	return s.Send(ctx, to, subject, body)
}
