package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/term"

	"github.com/starvy/flagfish/internal/accounts"
	"github.com/starvy/flagfish/internal/config"
	"github.com/starvy/flagfish/internal/domain/account"
)

// adminPasswordEnv is where the password is read from when --password is not given. Preferring the
// environment keeps the secret off the process argument list, where `ps` and shell history expose it.
const adminPasswordEnv = "FLAGFISH_ADMIN_PASSWORD" //nolint:gosec // the NAME of an env var, not a credential

// adminCmd owns the `admin` subcommand tree. Today it is only `create`, but a flat switch here keeps
// room for `admin promote`/`admin reset` without another dispatch layer in main.
func adminCmd(ctx context.Context, args []string, env config.Env, log *slog.Logger) error {
	if len(args) == 0 {
		adminUsage()
		return errors.New("admin: no subcommand given (try: create)")
	}
	switch args[0] {
	case "create":
		return adminCreate(ctx, args[1:], env, log)
	case "-h", "--help", "help":
		adminUsage()
		return nil
	default:
		adminUsage()
		return fmt.Errorf("unknown admin subcommand %q", args[0])
	}
}

func adminUsage() {
	fmt.Fprint(os.Stderr, `flagfish admin — administrative bootstrap

  create --email <e> [--name <n>] [--promote]

Create the first admin, or promote an existing account to admin. role='admin' is otherwise only
grantable through the admin API, which already requires an admin — this command breaks that deadlock.

The password (create only) is read from, in order: --password, $`+adminPasswordEnv+`, or an
interactive no-echo prompt. Prefer the environment variable: a password in --password is visible in
`+"`ps`"+` and shell history.

  --email    <address>   the admin's email (required)
  --name     <name>      display name (defaults to the email)
  --password <pw>        NOT recommended; prefer `+adminPasswordEnv+`
  --promote              elevate an EXISTING account to admin instead of creating one
`)
}

func adminCreate(ctx context.Context, args []string, env config.Env, log *slog.Logger) error {
	fs := flag.NewFlagSet("admin create", flag.ContinueOnError)
	email := fs.String("email", "", "the admin's email address (required)")
	name := fs.String("name", "", "display name (defaults to the email)")
	password := fs.String("password", "", "NOT recommended; prefer $"+adminPasswordEnv)
	promote := fs.Bool("promote", false, "elevate an existing account to admin instead of creating one")
	if err := fs.Parse(args); err != nil {
		return err
	}

	*email = strings.TrimSpace(*email)
	if *email == "" || !strings.Contains(*email, "@") {
		return errors.New("admin create: --email is required and must be an address")
	}

	pool, err := pgxpool.New(ctx, env.DatabaseURL)
	if err != nil {
		return fmt.Errorf("admin create: connect: %w", err)
	}
	defer pool.Close()

	// The account model is irrelevant to bootstrapping an admin — neither CreateAdmin nor
	// PromoteToAdmin resolves an account — so the mode passed here is never consulted.
	svc := accounts.NewService(pool, account.ModeUsers, log)

	if *promote {
		id, already, perr := svc.PromoteToAdmin(ctx, *email)
		if perr != nil {
			return fmt.Errorf("admin create: %w", perr)
		}
		if already {
			fmt.Printf("%s is already an admin (id %d); nothing to do\n", *email, id)
			return nil
		}
		fmt.Printf("promoted %s to admin (id %d)\n", *email, id)
		return nil
	}

	pw, err := resolvePassword(*password)
	if err != nil {
		return fmt.Errorf("admin create: %w", err)
	}
	if err = validateAdminPassword(pw); err != nil {
		return fmt.Errorf("admin create: %w", err)
	}

	displayName := strings.TrimSpace(*name)
	if displayName == "" {
		displayName = *email
	}

	id, err := svc.CreateAdmin(ctx, displayName, *email, pw)
	if errors.Is(err, accounts.ErrEmailTaken) {
		return fmt.Errorf("admin create: an account already exists for %s "+
			"(use --promote to make that account an admin)", *email)
	} else if err != nil {
		return fmt.Errorf("admin create: %w", err)
	}

	fmt.Printf("created admin %s (id %d)\n", *email, id)
	return nil
}

// resolvePassword sources the password without ever putting it in a log or a return-value error.
// Flag first (explicit), then the environment (recommended), then an interactive no-echo prompt.
func resolvePassword(flagValue string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	if env := os.Getenv(adminPasswordEnv); env != "" {
		return env, nil
	}
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", fmt.Errorf("no password given: set --password or $%s, or run interactively", adminPasswordEnv)
	}
	fmt.Fprint(os.Stderr, "admin password: ")
	b, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}
	return string(b), nil
}

// validateAdminPassword mirrors the registration policy (8–128 characters). A console bootstrap
// must not hold admin credentials to a weaker bar than the front door does.
func validateAdminPassword(pw string) error {
	switch n := utf8.RuneCountInString(pw); {
	case pw == "":
		return errors.New("password is empty")
	case n < 8:
		return fmt.Errorf("password is too short (%d characters; minimum 8)", n)
	case n > 128:
		return fmt.Errorf("password is too long (%d characters; maximum 128)", n)
	}
	return nil
}
