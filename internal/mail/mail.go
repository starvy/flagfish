// Package mail sends account email over SMTP. It is transport only: message
// content is composed by the feature that owns it, and delivery retries are the
// job queue's business, not this package's.
package mail

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	stdmail "net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// Mailer is the seam tests fake. One method, because sending is the whole contract.
type Mailer interface {
	Send(ctx context.Context, to, subject, body string) error
}

// ErrNotConfigured means no SMTP server is set. It is a hard error on Send —
// an email that silently goes nowhere is a player who can never verify or reset.
var ErrNotConfigured = errors.New("mail: no SMTP server is configured")

// Unconfigured is the mailer an instance without SMTP settings gets. Every Send fails.
type Unconfigured struct{}

func (Unconfigured) Send(_ context.Context, to, _, _ string) error {
	return fmt.Errorf("mail: cannot send to %s: %w", to, ErrNotConfigured)
}

// Config is the SMTP configuration, already parsed and typed.
type Config struct {
	Host     string
	Port     int
	Username string // empty means no AUTH
	Password string
	StartTLS bool
	From     string // RFC 5322 address, e.g. "CTF <noreply@ctf.example>"
}

// SMTP sends through one configured server.
type SMTP struct {
	cfg  Config
	from *stdmail.Address
}

// New validates the config and returns a ready mailer. Every problem is reported
// at once: an operator fixing SMTP settings should not discover them one restart
// at a time.
func New(cfg Config) (*SMTP, error) {
	var problems []error
	if cfg.Host == "" {
		problems = append(problems, errors.New("mail_server is required"))
	}
	if cfg.Port < 1 || cfg.Port > 65535 {
		problems = append(problems, fmt.Errorf("mail_port %d is not a valid port", cfg.Port))
	}
	from, err := stdmail.ParseAddress(cfg.From)
	if err != nil {
		problems = append(problems, fmt.Errorf("mailfrom_addr %q is not a valid address: %w", cfg.From, err))
	}
	if cfg.Password != "" && cfg.Username == "" {
		problems = append(problems, errors.New("mail_password is set but mail_username is not"))
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("mail: invalid configuration: %w", errors.Join(problems...))
	}
	return &SMTP{cfg: cfg, from: from}, nil
}

// sendTimeout bounds a Send with no caller deadline. SMTP servers hang; workers must not.
const sendTimeout = 30 * time.Second

func (m *SMTP) Send(ctx context.Context, to, subject, body string) error {
	rcpt, err := stdmail.ParseAddress(to)
	if err != nil {
		return fmt.Errorf("mail: recipient %q: %w", to, err)
	}
	msg, err := message(m.from, rcpt, subject, body)
	if err != nil {
		return err
	}

	addr := net.JoinHostPort(m.cfg.Host, strconv.Itoa(m.cfg.Port))
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("mail: dial %s: %w", addr, err)
	}
	deadline := time.Now().Add(sendTimeout)
	if d, ok := ctx.Deadline(); ok {
		deadline = d
	}
	if err = conn.SetDeadline(deadline); err != nil {
		conn.Close()
		return fmt.Errorf("mail: set deadline: %w", err)
	}

	c, err := smtp.NewClient(conn, m.cfg.Host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("mail: smtp handshake with %s: %w", addr, err)
	}
	defer c.Close()

	if m.cfg.StartTLS {
		if err = c.StartTLS(&tls.Config{ServerName: m.cfg.Host, MinVersion: tls.VersionTLS12}); err != nil {
			return fmt.Errorf("mail: starttls with %s: %w", addr, err)
		}
	}
	if m.cfg.Username != "" {
		// PlainAuth itself refuses to send credentials over an unencrypted connection
		// to a remote host, which is the failure we want: loud, not downgraded.
		if err = c.Auth(smtp.PlainAuth("", m.cfg.Username, m.cfg.Password, m.cfg.Host)); err != nil {
			return fmt.Errorf("mail: auth with %s: %w", addr, err)
		}
	}

	if err = c.Mail(m.from.Address); err != nil {
		return fmt.Errorf("mail: MAIL FROM: %w", err)
	}
	if err = c.Rcpt(rcpt.Address); err != nil {
		return fmt.Errorf("mail: RCPT TO: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("mail: DATA: %w", err)
	}
	if _, err = w.Write(msg); err != nil {
		return fmt.Errorf("mail: write message: %w", err)
	}
	if err = w.Close(); err != nil {
		return fmt.Errorf("mail: finish message: %w", err)
	}
	if err = c.Quit(); err != nil {
		return fmt.Errorf("mail: quit: %w", err)
	}
	return nil
}

// message renders the wire format. The subject is refused rather than escaped if it
// carries CR/LF — a newline in a header is an injection, and nothing legitimate sends one.
func message(from, to *stdmail.Address, subject, body string) ([]byte, error) {
	if strings.ContainsAny(subject, "\r\n") {
		return nil, errors.New("mail: subject contains a line break")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", from.String())
	fmt.Fprintf(&b, "To: %s\r\n", to.String())
	fmt.Fprintf(&b, "Subject: %s\r\n", subject)
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().UTC().Format(time.RFC1123Z))
	b.WriteString("MIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n")
	b.WriteString(strings.ReplaceAll(body, "\n", "\r\n"))
	return []byte(b.String()), nil
}
