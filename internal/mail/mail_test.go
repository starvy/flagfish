package mail

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// An unconfigured mailer must FAIL, not silently drop: a verification email that goes
// nowhere is a player who can never verify, with nothing in the logs to say why.
func TestUnconfiguredSendIsAHardError(t *testing.T) {
	err := Unconfigured{}.Send(context.Background(), "player@ctf.test", "hi", "body")
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("Send on unconfigured mailer = %v, want ErrNotConfigured", err)
	}
}

func TestNewReportsEveryProblemAtOnce(t *testing.T) {
	_, err := New(Config{Host: "", Port: 0, From: "not-an-address", Password: "p", Username: ""})
	if err == nil {
		t.Fatal("New accepted an invalid config")
	}
	for _, want := range []string{"mail_server", "mail_port", "mailfrom_addr", "mail_password"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestNewAcceptsAValidConfig(t *testing.T) {
	if _, err := New(Config{Host: "smtp.ctf.test", Port: 587, From: "CTF <noreply@ctf.test>", StartTLS: true}); err != nil {
		t.Fatalf("New rejected a valid config: %v", err)
	}
}

// A CR/LF in a header is an injection; the message builder refuses it rather than escaping.
func TestMessageRefusesHeaderInjection(t *testing.T) {
	m, err := New(Config{Host: "h", Port: 25, From: "a@b.test"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := message(m.from, m.from, "subject\r\nBcc: evil@ctf.test", "body"); err == nil {
		t.Fatal("message accepted a subject with a line break")
	}
}
