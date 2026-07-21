package mail

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"maps"
	"net"
	"strconv"
	"strings"
	"testing"

	"github.com/starvy/flagfish/internal/config"
	"github.com/starvy/flagfish/internal/domain/account"
)

type cfgStore struct {
	rows map[string]string
}

func (s *cfgStore) All(context.Context) (map[string]string, error) {
	return maps.Clone(s.rows), nil
}

func (s *cfgStore) Mode(context.Context) (account.Mode, bool, error) {
	return account.ModeUsers, true, nil
}

func (s *cfgStore) Replace(_ context.Context, kv map[string]string) error {
	maps.Copy(s.rows, kv)
	return nil
}

// The mailer must follow the config, not the boot: SMTP settings written while
// the process runs take effect on the very next Send. Before this, the mailer was
// built once at boot and a runtime edit silently changed nothing until a restart.
func TestSendReadsTheCurrentSnapshot(t *testing.T) {
	ctx := context.Background()
	cfg, err := config.New(ctx, &cfgStore{rows: map[string]string{"setup": "true"}},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	m := newMailer(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))

	if err = m.Send(ctx, "p@ctf.test", "s", "b"); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("Send before configuration: got %v, want ErrNotConfigured", err)
	}

	// A loopback port with nothing listening: the dial must be attempted and fail,
	// which is exactly the proof the new settings were picked up without a rebuild.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr, ok := l.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("listener addr is %T, want *net.TCPAddr", l.Addr())
	}
	port := addr.Port
	_ = l.Close()

	if err = cfg.Set(ctx, map[string]string{
		"mail_server":   "127.0.0.1",
		"mail_port":     strconv.Itoa(port),
		"mailfrom_addr": "noreply@ctf.test",
	}); err != nil {
		t.Fatalf("write mail config: %v", err)
	}

	err = m.Send(ctx, "p@ctf.test", "s", "b")
	if errors.Is(err, ErrNotConfigured) {
		t.Fatal("Send still says not-configured: the mailer is frozen on its boot-time snapshot")
	}
	if err == nil || !strings.Contains(err.Error(), "dial") {
		t.Fatalf("Send = %v, want a dial failure against the freshly-configured server", err)
	}
}
