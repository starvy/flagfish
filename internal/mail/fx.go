package mail

import (
	"log/slog"

	"go.uber.org/fx"

	"github.com/starvy/flagfish/internal/config"
)

// Module provides the Mailer the email worker sends through.
var Module = fx.Module(
	"mail",
	fx.Provide(newMailer),
)

// newMailer builds the mailer from the boot-time config snapshot. Invalid settings
// refuse to boot; absent settings yield a mailer whose every Send fails loudly —
// queued mail then errors and retries instead of vanishing.
func newMailer(cfg *config.Manager, log *slog.Logger) (Mailer, error) {
	snap := cfg.Current()
	if snap.MailServer == "" {
		log.Warn("mail is not configured (mail_server is unset); outgoing email will fail until it is")
		return Unconfigured{}, nil
	}
	m, err := New(Config{
		Host:     snap.MailServer,
		Port:     snap.MailPort,
		Username: snap.MailUsername,
		Password: snap.MailPassword,
		StartTLS: snap.MailTLS,
		From:     snap.MailFrom,
	})
	if err != nil {
		return nil, err
	}
	return m, nil
}
