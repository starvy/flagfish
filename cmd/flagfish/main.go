// Command flagfish is the whole product: one static binary, four subcommands.
//
//	flagfish serve                 # API. River insert-only client — enqueues, never works.
//	flagfish worker                # River client with the workers registered.
//	flagfish serve --with-worker   # both, in-process. THE DEFAULT. compose runs this.
//	flagfish migrate               # goose, behind pg_advisory_lock.
//	flagfish import <archive.zip>  # CTFd archive -> our schema (one-way).
//
// Self-hosters get ONE container. Large events split the roles and get: imports
// cannot touch API p99, deploying the API does not kill a running import, and the
// process that drops and restores the database is not the one serving traffic.
// Don't force the topology — make it a flag.
//
// Why stdlib `flag` and not cobra: four subcommands, no nesting, no shell
// completion, no config-file merging. Cobra earns its keep on a CLI with dozens of
// commands and a plugin surface; here it would be a dependency, an init-order
// puzzle, and a layer of indirection in front of `os.Args`. The one place a real CLI
// framework would pay for itself is flagfishctl — and even there the command surface
// is five verbs.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/starvy/flagfish/internal/app"
	"github.com/starvy/flagfish/internal/config"
	"github.com/starvy/flagfish/internal/migrate"
	"github.com/starvy/flagfish/internal/platform/exporter"
	"github.com/starvy/flagfish/internal/platform/importer"
	"github.com/starvy/flagfish/internal/storage"
)

// version is stamped by the build (-X main.version=…); "dev" for a plain `go build`. The importer
// records it as instance.version so a restored instance names the binary that restored it.
var version = "dev"

func main() { os.Exit(main2()) }

// The graph stamps backups and imports with the same version string this binary reports, so the
// async workers and the CLI subcommands are indistinguishable in a manifest.
func init() { app.Version = version }

// main2 exists so that os.Exit is called in exactly one place, after every defer in
// here has run. os.Exit skips defers, so a signal handler released by `defer stop()`
// would otherwise leak on the error path.
func main2() int {
	// Bootstrap logger, replaced once the environment says how to log. Without it, a
	// bad FLAGFISH_LOG_LEVEL would have nowhere to report itself.
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// Commands that neither open the database nor serve do not need a validated environment, so they
	// run before LoadEnv can reject a box that has none — `flagfish openapi` on a CI runner, `--help`
	// anywhere.
	if args := os.Args[1:]; len(args) > 0 && envFreeCommand(args[0]) {
		if err := run(context.Background(), args, &config.Env{}, log); err != nil {
			log.Error("fatal", "error", err)
			return 1
		}
		return 0
	}

	env, err := config.LoadEnv()
	if err != nil {
		log.Error("fatal", "error", err)
		return 1
	}
	log = env.Logger()

	// Signal-aware from the first line: a CTF platform gets deployed mid-event, and
	// a submit transaction that is cut off halfway is a support ticket.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args[1:], &env, log); err != nil {
		log.Error("fatal", "error", err)
		return 1
	}
	return 0
}

func envFreeCommand(name string) bool {
	switch name {
	case "openapi", "healthcheck", "help", "-h", "--help":
		return true
	}
	return false
}

func run(ctx context.Context, args []string, env *config.Env, log *slog.Logger) error {
	if len(args) == 0 {
		usage()
		return errors.New("no subcommand given")
	}

	switch args[0] {
	case "serve":
		return serve(ctx, args[1:], env, log)
	case "worker":
		return worker(ctx, args[1:], env, log)
	case "migrate":
		return migrateCmd(ctx, args[1:], env, log)
	case "import":
		return importCmd(ctx, args[1:], env, log)
	case "admin":
		return adminCmd(ctx, args[1:], env, log)
	case "export":
		return exportCmd(ctx, args[1:], env, log)
	case "restore":
		return restoreCmd(ctx, args[1:], env, log)
	case "env":
		return envCmd(env)
	case "openapi":
		return openapiCmd(ctx, args[1:], log)
	case "healthcheck":
		return healthcheckCmd(ctx, args[1:])
	case "-h", "--help", "help":
		usage()
		return nil
	default:
		usage()
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

// envCmd prints the resolved environment. LoadEnv has already validated it, so
// reaching this at all means the config is good — which is the point: it turns "why
// won't it boot" into one command, before the deploy.
func envCmd(env *config.Env) error {
	proxies := "(none — trusting the socket peer address)"
	if len(env.TrustedProxies) > 0 {
		parts := make([]string, len(env.TrustedProxies))
		for i, p := range env.TrustedProxies {
			parts[i] = p.String()
		}
		proxies = strings.Join(parts, ", ")
	}
	fmt.Printf("database         %s\n", env.RedactedDatabaseURL())
	fmt.Printf("addr             %s\n", env.Addr)
	fmt.Printf("log              %s (%s)\n", env.LogLevel, env.LogFormat)
	fmt.Printf("trusted proxies  %s\n", proxies)
	return nil
}

// openapiCmd prints an OpenAPI document to stdout: the public surface, or the admin one with
// --admin. It builds the router without a database — the handlers are registered but never called —
// so the emitted contract is exactly what the binary serves, which is what makes the checked-in
// openapi.yaml a drift check rather than an artifact.
func openapiCmd(ctx context.Context, args []string, log *slog.Logger) error {
	fs := flag.NewFlagSet("openapi", flag.ExitOnError)
	admin := fs.Bool("admin", false, "emit the admin surface instead of the public one")
	if err := fs.Parse(args); err != nil {
		return err
	}

	doc, err := app.OpenAPIYAML(ctx, log, *admin)
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(doc)
	return err
}

// healthcheckCmd probes the local /healthz endpoint and exits non-zero on anything but 200. It is
// how the distroless image — which ships no shell and no curl — answers a container healthcheck:
// the binary probes itself. It never opens the database, so it stays an env-free command.
func healthcheckCmd(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("healthcheck", flag.ExitOnError)
	addr := fs.String("addr", envOrDefault("FLAGFISH_ADDR", ":8000"), "listen address to probe")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	url := "http://" + probeHostPort(*addr) + "/healthz"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return fmt.Errorf("healthcheck: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("healthcheck: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthcheck: %s returned %d", url, resp.StatusCode)
	}
	return nil
}

// probeHostPort turns a listen address into one a client can dial: a wildcard host (empty, 0.0.0.0,
// or ::) becomes loopback, since the probe runs inside the same container as the listener.
func probeHostPort(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		// No colon: treat the whole thing as a port.
		return net.JoinHostPort("127.0.0.1", strings.TrimPrefix(addr, ":"))
	}
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

func envOrDefault(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func usage() {
	fmt.Fprint(os.Stderr, `flagfish — a CTF platform

  serve [--with-worker]   run the API (and, by default, the worker in-process)
  worker                  run the job worker only
  migrate                 apply schema migrations, behind an advisory lock
  import <archive.zip>    import a CTFd archive
  admin create            create (or --promote) the first admin AND complete setup — this is what
                          makes a freshly migrated database a live instance. Until it runs, every
                          route is denied, login included. --mode users|teams fixes the model.
  export [--safe|--backup] <out.zip>
                          export the instance in our own format (default: --safe,
                          field-masked and shareable; --backup is full fidelity)
  restore <in.zip>        REPLACE this instance's data with a --backup archive: every table
                          it owns is truncated and reloaded in one transaction. Not a merge,
                          and it does not require an empty instance — it makes one.
  env                     print the resolved environment and exit
  openapi [--admin]       print the OpenAPI document (public surface; --admin for the admin one)
  healthcheck             GET /healthz on the local listener; exit non-zero unless 200.
                          The container health probe: the image has no shell and no curl.

Environment (see .env.example):
  FLAGFISH_DATABASE_URL     postgres://user:pass@host:5432/flagfish   (required;
                          DATABASE_URL is accepted as an alias)
  FLAGFISH_ADDR             listen address                          (default :8000)
  FLAGFISH_LOG_LEVEL        debug|info|warn|error                   (default info)
  FLAGFISH_LOG_FORMAT       text|json                               (default json)
  FLAGFISH_TRUSTED_PROXIES  comma-separated CIDRs whose X-Forwarded-For we believe.
                          Empty = trust nobody, use the socket peer address.
`)
}

// ---------------------------------------------------------------------------
// serve
// ---------------------------------------------------------------------------

func serve(ctx context.Context, args []string, env *config.Env, log *slog.Logger) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	// Flags override the environment; both are validated the same way.
	addr := fs.String("addr", env.Addr, "listen address")
	withWorker := fs.Bool("with-worker", true, "also run the job worker in this process (the default: one container)")
	autoMigrate := fs.Bool("migrate", false, "apply migrations before serving (safe: takes the advisory lock)")
	// Defaults to FLAGFISH_TRUSTED_PROXIES, already parsed and validated by LoadEnv. The
	// flag overrides it; an empty default here would silently discard the env var.
	trusted := fs.String("trusted-proxies", env.TrustedProxiesCSV(),
		"comma-separated CIDRs whose X-Forwarded-For we believe. EMPTY = trust nobody.")
	if err := fs.Parse(args); err != nil {
		return err
	}

	log.Info("starting", "addr", *addr, "database", env.RedactedDatabaseURL())

	if *autoMigrate {
		// Safe with N replicas ONLY because migrate.Run takes pg_advisory_lock.
		// Without that lock this flag is a schema-corrupting race.
		if err := migrate.Run(ctx, env.DatabaseURL, log); err != nil {
			return err
		}
	}

	proxies, err := parseCIDRs(*trusted)
	if err != nil {
		return err
	}
	if len(proxies) == 0 {
		log.Warn("no trusted proxies configured: X-Forwarded-For is ignored and the socket peer is recorded. " +
			"Behind a load balancer this is WRONG, and submissions.ip is anti-cheat evidence.")
	}

	return app.Serve(ctx, env, log, app.ServeConfig{
		Addr:           *addr,
		TrustedProxies: proxies,
		WithWorker:     *withWorker,
	})
}

func parseCIDRs(s string) ([]*net.IPNet, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var out []*net.IPNet
	for _, part := range strings.Split(s, ",") {
		_, n, err := net.ParseCIDR(strings.TrimSpace(part))
		if err != nil {
			return nil, fmt.Errorf("trusted-proxies: %w", err)
		}
		out = append(out, n)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// worker
// ---------------------------------------------------------------------------

func worker(ctx context.Context, args []string, env *config.Env, log *slog.Logger) error {
	fs := flag.NewFlagSet("worker", flag.ExitOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	return app.Worker(ctx, env, log)
}

// ---------------------------------------------------------------------------
// migrate
// ---------------------------------------------------------------------------

func migrateCmd(ctx context.Context, args []string, env *config.Env, log *slog.Logger) error {
	fs := flag.NewFlagSet("migrate", flag.ExitOnError)
	status := fs.Bool("status", false, "print the current schema version and the embedded migrations, then exit")
	if err := fs.Parse(args); err != nil {
		return err
	}

	url := env.DatabaseURL

	if *status {
		version, err := migrate.Version(ctx, url)
		if err != nil {
			return err
		}
		embedded, err := migrate.Embedded()
		if err != nil {
			return err
		}
		fmt.Printf("schema version: %d\nembedded migrations: %d\n", version, len(embedded))
		for _, m := range embedded {
			fmt.Printf("  %s\n", m)
		}
		return nil
	}

	return migrate.Run(ctx, url, log)
}

// ---------------------------------------------------------------------------
// import
// ---------------------------------------------------------------------------

func importCmd(ctx context.Context, args []string, env *config.Env, log *slog.Logger) error {
	fs := flag.NewFlagSet("import", flag.ExitOnError)
	assume := fs.String("assume-revision", "",
		"translate an unknown archive revision as this known one (unsupported, loud)")
	forceType := fs.String("force-unknown-challenge-type", "",
		"import an unknown challenge type as this type instead of failing (e.g. standard)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: flagfish import [flags] <archive.zip>")
	}
	path := fs.Arg(0)

	// One-way by decision: an archive comes in, and nothing goes back out in that format.
	pool, err := pgxpool.New(ctx, env.DatabaseURL)
	if err != nil {
		return fmt.Errorf("import: connect: %w", err)
	}
	defer pool.Close()

	log.Info("importing archive", "path", path)
	report, err := importer.Run(ctx, pool, path, importer.Options{
		Version:                   version,
		AssumeRevision:            *assume,
		ForceUnknownChallengeType: *forceType,
	})
	if report != nil {
		printImportReport(report)
	}
	if err != nil {
		return err
	}
	log.Info("import complete", "revision", report.SourceRevision, "ordinal", report.SourceOrdinal)
	return nil
}

// printImportReport writes a human summary to stdout: what came in, what was written, what was
// dropped, and every finding. The import is loud by construction — the report is where it says so.
func printImportReport(r *importer.Report) {
	fmt.Printf("\nimport report — source revision %s (ordinal %d)\n", r.SourceRevision, r.SourceOrdinal)
	if r.SourceHint != "" {
		fmt.Printf("source version hint: %s\n", r.SourceHint)
	}
	fmt.Printf("\n  %-14s %8s %8s %8s\n", "table", "read", "written", "dropped")
	tables := make([]string, 0, len(r.Rows))
	for t := range r.Rows {
		tables = append(tables, t)
	}
	sort.Strings(tables)
	for _, t := range tables {
		s := r.Rows[t]
		fmt.Printf("  %-14s %8d %8d %8d\n", t, s.Read, s.Written, s.Dropped)
	}
	if len(r.Findings) > 0 {
		fmt.Printf("\nfindings:\n")
		for _, f := range r.Findings {
			fmt.Printf("  [%s] %s %s: %s\n", f.Severity, f.Code, f.Table, f.Detail)
		}
	}
	fmt.Println()
}

// ---------------------------------------------------------------------------
// export / restore — our own archive format, never a foreign-compatible one
// ---------------------------------------------------------------------------

// exportCmd writes the instance to our own archive. The default is the field-masked, shareable
// profile; --backup is the full-fidelity artifact for migration and disaster recovery. Never the
// foreign import shape — this format is ours, and it is not re-importable by the tool we import from.
func exportCmd(ctx context.Context, args []string, env *config.Env, log *slog.Logger) error {
	fs := flag.NewFlagSet("export", flag.ExitOnError)
	_ = fs.Bool("safe", true, "field-masked, shareable, NOT restorable (the default)")
	backup := fs.Bool("backup", false, "full fidelity for migration/DR: includes password hashes and flags")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: flagfish export [--safe|--backup] <out.zip>")
	}
	if *backup && isFlagSet(fs, "safe") {
		return errors.New("export: pass either --safe or --backup, not both")
	}
	profile := exporter.ProfileSafe
	if *backup {
		profile = exporter.ProfileBackup
	}
	out := fs.Arg(0)

	pool, err := pgxpool.New(ctx, env.DatabaseURL)
	if err != nil {
		return fmt.Errorf("export: connect: %w", err)
	}
	defer pool.Close()
	store, err := provideExportStore(log)
	if err != nil {
		return err
	}

	f, err := os.Create(out)
	if err != nil {
		return fmt.Errorf("export: create %s: %w", out, err)
	}
	defer f.Close()

	log.Info("exporting instance", "profile", profile, "out", out)
	rep, err := exporter.Export(ctx, pool, store, profile, version, f)
	if err != nil {
		return err
	}
	if cerr := f.Close(); cerr != nil {
		return fmt.Errorf("export: finalise %s: %w", out, cerr)
	}
	printExportReport(rep, out)
	return nil
}

// restoreCmd replaces this instance's data with a --backup archive, in one transaction: it
// truncates every table it owns rather than refusing a populated database. A --safe archive is
// rejected loudly: it was field-masked and carries no secrets to restore.
func restoreCmd(ctx context.Context, args []string, env *config.Env, log *slog.Logger) error {
	fs := flag.NewFlagSet("restore", flag.ExitOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: flagfish restore <in.zip>")
	}
	in := fs.Arg(0)

	pool, err := pgxpool.New(ctx, env.DatabaseURL)
	if err != nil {
		return fmt.Errorf("restore: connect: %w", err)
	}
	defer pool.Close()
	store, err := provideExportStore(log)
	if err != nil {
		return err
	}

	f, err := os.Open(in)
	if err != nil {
		return fmt.Errorf("restore: open %s: %w", in, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("restore: stat %s: %w", in, err)
	}

	log.Info("restoring archive", "in", in)
	rep, err := exporter.Restore(ctx, pool, store, f, info.Size())
	if err != nil {
		return err
	}
	log.Info("restore complete", "user_mode", rep.UserMode, "files", rep.Files)
	fmt.Printf("\nrestore complete — user_mode %s, %d file(s), %d byte(s) of blobs\n", rep.UserMode, rep.Files, rep.UploadBytes)
	return nil
}

// provideExportStore builds the object store the same way the server does. Export and restore stream
// file blobs through it; an instance with no object storage configured can still round-trip an
// instance that has no files, and fails loudly the moment a blob is actually needed.
func provideExportStore(log *slog.Logger) (storage.Store, error) {
	cfg, err := storage.ConfigFromEnv()
	if err != nil {
		return nil, err
	}
	return storage.NewStore(cfg, log)
}

func printExportReport(r *exporter.Report, out string) {
	fmt.Printf("\nexport complete — profile %s, user_mode %s\n", r.Profile, r.UserMode)
	fmt.Printf("  %-22s %8s\n", "table", "rows")
	tables := make([]string, 0, len(r.Rows))
	for t := range r.Rows {
		tables = append(tables, t)
	}
	sort.Strings(tables)
	for _, t := range tables {
		fmt.Printf("  %-22s %8d\n", t, r.Rows[t])
	}
	fmt.Printf("\n  files: %d (%d bytes)\n", r.Files, r.UploadBytes)
	if len(r.Omitted) > 0 {
		fmt.Printf("  withheld: %s\n", strings.Join(r.Omitted, ", "))
	}
	fmt.Printf("\n  wrote %s\n\n", out)
}

// isFlagSet reports whether a flag was explicitly passed, as opposed to left at its default.
func isFlagSet(fs *flag.FlagSet, name string) bool {
	set := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	return set
}
