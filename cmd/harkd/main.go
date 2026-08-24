// Command harkd runs the Hark server — the API and the embedded dashboard —
// and provisions its single account.
//
//	harkd serve                 run the server (the default)
//	harkd create-user           create the one account this deployment serves
//	harkd set-password          replace that account's password
//
// All configuration comes from the environment; see the README for the full
// list. The process exits non-zero with a single diagnostic line when the
// configuration is invalid or the database cannot be reached at boot.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/abdeen-labs/hark/internal/apns"
	"github.com/abdeen-labs/hark/internal/auth"
	"github.com/abdeen-labs/hark/internal/callbacks"
	"github.com/abdeen-labs/hark/internal/config"
	"github.com/abdeen-labs/hark/internal/dashboard"
	"github.com/abdeen-labs/hark/internal/db"
	"github.com/abdeen-labs/hark/internal/httpapi"
	"github.com/abdeen-labs/hark/internal/maintenance"
	"github.com/abdeen-labs/hark/internal/push"
	"github.com/abdeen-labs/hark/internal/secret"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Server timeouts. The write timeout has to comfortably exceed the longest
// long-poll the API offers, so raising a poll ceiling means raising this too.
const (
	readHeaderTimeout = 10 * time.Second
	readTimeout       = 30 * time.Second
	writeTimeout      = 90 * time.Second
	idleTimeout       = 120 * time.Second
	maxHeaderBytes    = 1 << 16 // 64 KiB
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "harkd: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	// A bare `harkd` runs the server, so the first argument is a command name
	// only when it does not look like a flag.
	command := "serve"
	if len(args) > 0 {
		switch args[0] {
		case "-h", "-help", "--help":
			usage(os.Stdout)
			return nil
		}
		if !strings.HasPrefix(args[0], "-") {
			command, args = args[0], args[1:]
		}
	}

	switch command {
	case "help":
		usage(os.Stdout)
		return nil
	case "serve":
		return serveCommand(ctx, args)
	case "create-user":
		return createUserCommand(ctx, args)
	case "set-password":
		return setPasswordCommand(ctx, args)
	default:
		usage(os.Stderr)
		return fmt.Errorf("unknown command %q", command)
	}
}

func usage(w io.Writer) {
	_, _ = io.WriteString(w, `harkd — the Hark server

Usage:
  harkd [serve]                          run the API and the dashboard
  harkd create-user [flags]              create the single account
  harkd set-password [flags]             replace that account's password
  harkd help                             show this message

Configuration comes from the environment; see the README.
The password for create-user and set-password is read from standard input when
something is piped in, and otherwise from HARK_ADMIN_PASSWORD:

  printf '%s' 'correct horse battery staple' | harkd create-user -username admin
`)
}

func serveCommand(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.LoadFromEnv()
	if err != nil {
		return err
	}

	log := newLogger(cfg)
	slog.SetDefault(log)

	version := buildVersion()
	log.Info("starting harkd", "version", version, "config", cfg)
	for _, w := range cfg.Warnings() {
		log.Warn(w)
	}

	pool, err := openDatabase(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer pool.Close()

	store := db.New(pool)
	authService := auth.New(store, nil)
	seedAccount(ctx, log, authService, cfg)

	sender, err := newPushSender(cfg, log)
	if err != nil {
		return err
	}

	secrets := secret.NewKeeper(cfg.SecretKey)

	// A webhook may ask its question with a callback URL. Answering arms the
	// row; this worker is what posts the answer back.
	callbackWorker := callbacks.New(callbacks.Options{
		Store:   store,
		Secrets: secrets,
		Logger:  log,
	})

	// Every push appends a diagnostic attempt row and nothing reads one back at
	// request time, so this worker is the table's retention policy: prune what
	// is older than the configured window at boot, then daily.
	attemptPruner := maintenance.NewAttemptPruner(maintenance.Options{
		Attempts:      store.Attempts,
		RetentionDays: cfg.APNsAttemptRetentionDays,
		Logger:        log,
	})

	// The admin UI is compiled into this binary and mounted on the site root.
	// It shares this process's auth service, store and push transport rather
	// than calling the API over HTTP, and serves inside the API's middleware
	// chain — so it authenticates through the same session cookie.
	admin := dashboard.New(dashboard.Options{
		Auth:                  authService,
		Store:                 store,
		Secrets:               secrets,
		Push:                  sender,
		PublicURL:             cfg.PublicURL,
		TrustedClientIPHeader: cfg.TrustedClientIPHeader,
		Version:               version,
	})

	handler := httpapi.New(httpapi.Options{
		Logger:                 log,
		DB:                     pool,
		Auth:                   authService,
		Store:                  store,
		Secrets:                secrets,
		Push:                   sender,
		PublicURL:              cfg.PublicURL,
		TrustedClientIPHeader:  cfg.TrustedClientIPHeader,
		MaxRequestBytes:        cfg.MaxRequestBytes,
		RequesterRatePerMinute: cfg.RateLimit.RequesterPerMinute,
		AccountRatePerMinute:   cfg.RateLimit.AccountPerMinute,
		Version:                version,
		Dashboard:              admin,
		Callbacks:              callbackWorker,
	})

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
		BaseContext:       func(net.Listener) context.Context { return context.WithoutCancel(ctx) },
	}

	ln, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.ListenAddr, err)
	}
	log.Info("listening", "addr", ln.Addr().String(), "public_url", cfg.PublicURL.String())

	// The workers stop on the same signal the server does. A callback still in
	// flight is abandoned — the attempt is recorded, and the row it belongs to
	// is retried on schedule after the next boot — and the process waits for
	// both to unwind so nothing touches a pool that is about to close.
	var background sync.WaitGroup
	background.Add(2)
	go func() {
		defer background.Done()
		callbackWorker.Run(ctx)
	}()
	go func() {
		defer background.Done()
		attemptPruner.Run(ctx)
	}()

	err = serve(ctx, log, srv, ln, cfg.ShutdownTimeout)
	background.Wait()
	return err
}

// newPushSender builds the APNs transport, or the one that reaches nothing.
//
// A deployment without credentials is a supported state — the server runs, and
// every send is recorded as a provider failure rather than as a delivery that
// did not happen — so the absence of credentials is a warning and not an error.
// Present but invalid credentials prevent startup so configuration errors are
// reported immediately.
func newPushSender(cfg *config.Config, log *slog.Logger) (push.Sender, error) {
	if !cfg.APNs.Configured() {
		// The boot warning naming the three variables has already been logged.
		return push.Noop{}, nil
	}

	client, err := apns.NewClient(apns.Config{
		KeyID:       cfg.APNs.KeyID,
		TeamID:      cfg.APNs.TeamID,
		PrivateKey:  cfg.APNs.PrivateKey,
		BundleID:    cfg.APNs.BundleID,
		Environment: cfg.APNs.Environment,
		Logger:      log,
	})
	if err != nil {
		return nil, err
	}

	log.Info("APNs is configured",
		"environment", cfg.APNs.Environment, "topic", cfg.APNs.BundleID)
	return apns.NewSender(client), nil
}

// createUserCommand provisions the deployment's one account.
//
// There is no sign-up endpoint, so this and the boot-time seed are the only two
// ways an account comes into being — and both go through the same guarded
// insert, which refuses once an account exists.
func createUserCommand(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("create-user", flag.ContinueOnError)
	username := fs.String("username", "", "sign-in handle (defaults to HARK_ADMIN_USERNAME)")
	email := fs.String("email", "", "contact address, display only (defaults to HARK_ADMIN_EMAIL)")
	displayName := fs.String("display-name", "", "human-facing name (defaults to the username)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, log, pool, err := openForCommand(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()

	password, err := readPassword(cfg)
	if err != nil {
		return err
	}
	if *username == "" {
		*username = cfg.Admin.Username
	}
	if *email == "" {
		*email = cfg.Admin.Email
	}

	service := auth.New(db.New(pool), nil)
	user, err := service.CreateAccount(ctx, auth.CreateAccountParams{
		Username:    *username,
		Password:    password,
		Email:       *email,
		DisplayName: *displayName,
	})
	switch {
	case errors.Is(err, auth.ErrAccountExists):
		return errors.New("an account already exists; Hark is single-user. Use `harkd set-password` to change its password")
	case err != nil:
		return err
	}

	log.Info("created the account", "username", user.Username, "id", user.ID)
	return nil
}

// setPasswordCommand lets an operator reset a password without knowing the
// current value. This recovery operation is available only on the server.
func setPasswordCommand(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("set-password", flag.ContinueOnError)
	username := fs.String("username", "", "sign-in handle (defaults to HARK_ADMIN_USERNAME)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, log, pool, err := openForCommand(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()

	password, err := readPassword(cfg)
	if err != nil {
		return err
	}
	if *username == "" {
		*username = cfg.Admin.Username
	}

	service := auth.New(db.New(pool), nil)
	switch err := service.SetPassword(ctx, *username, password); {
	case errors.Is(err, auth.ErrNotFound):
		return fmt.Errorf("no account named %q", *username)
	case err != nil:
		return err
	}

	log.Info("replaced the password and signed out every session", "username", *username)
	return nil
}

// openForCommand does the shared boot work of the non-serving commands.
func openForCommand(ctx context.Context) (*config.Config, *slog.Logger, *pgxpool.Pool, error) {
	cfg, err := config.LoadFromEnv()
	if err != nil {
		return nil, nil, nil, err
	}
	log := newLogger(cfg)

	pool, err := openDatabase(ctx, cfg, log)
	if err != nil {
		return nil, nil, nil, err
	}
	return cfg, log, pool, nil
}

func openDatabase(ctx context.Context, cfg *config.Config, log *slog.Logger) (*pgxpool.Pool, error) {
	pool, err := db.Open(ctx, db.Config{
		URL:             cfg.Database.URL,
		MaxConns:        cfg.Database.MaxConns,
		MinConns:        cfg.Database.MinConns,
		ConnectTimeout:  cfg.Database.ConnectTimeout,
		MaxConnLifetime: cfg.Database.MaxConnLifetime,
		MaxConnIdleTime: cfg.Database.MaxConnIdleTime,
	})
	if err != nil {
		return nil, err
	}
	log.Info("connected to postgres", "url", db.Redact(cfg.Database.URL))

	if cfg.Database.AutoMigrate {
		if err := db.Migrate(ctx, pool, db.Migrations(), log); err != nil {
			pool.Close()
			return nil, err
		}
	} else {
		log.Warn("automatic migrations are disabled (HARK_DB_AUTO_MIGRATE=false)")
	}
	return pool, nil
}

// readPassword takes the password from standard input when something is piped
// in, and otherwise from HARK_ADMIN_PASSWORD.
//
// Piped input wins because it is the more specific instruction: an operator
// resetting a password on a host whose environment still holds the original
// seed means the value they just typed, not the one in the compose file.
//
// Nothing here prompts interactively with echo disabled — that would mean a
// terminal library for the one path an operator runs twice in a deployment's
// lifetime. Piping is also what keeps the password out of a shell history and
// out of an argv any process on the box can read.
func readPassword(cfg *config.Config) (string, error) {
	if piped, err := readPipedPassword(); err != nil {
		return "", err
	} else if piped != "" {
		return piped, nil
	}

	if cfg.Admin.Password != "" {
		return cfg.Admin.Password, nil
	}
	return "", errors.New("no password given: pipe one into stdin, or set HARK_ADMIN_PASSWORD")
}

// readPipedPassword returns what was redirected into stdin, or "" when stdin is
// a terminal and there is nothing to read.
func readPipedPassword() (string, error) {
	info, err := os.Stdin.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice != 0 {
		// A character device is an interactive terminal: reading it would hang
		// waiting for input the caller never meant to give.
		return "", nil //nolint:nilerr // an unstattable stdin is not an input source
	}
	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", fmt.Errorf("read the password from stdin: %w", err)
	}
	return strings.TrimRight(string(raw), "\r\n"), nil
}

// seedAccount creates the account at boot when one is configured and none
// exists. It is idempotent and non-fatal; a failure leaves sign-in unavailable
// and is written to the log.
func seedAccount(ctx context.Context, log *slog.Logger, service *auth.Service, cfg *config.Config) {
	if !cfg.Admin.Seedable() {
		return
	}

	user, err := service.CreateAccount(ctx, auth.CreateAccountParams{
		Username: cfg.Admin.Username,
		Password: cfg.Admin.Password,
		Email:    cfg.Admin.Email,
	})
	switch {
	case errors.Is(err, auth.ErrAccountExists):
		// The normal case on every restart after the first.
	case err != nil:
		log.Error("could not create the configured account; sign-in may be unavailable", "error", err)
	default:
		log.Info("created the configured account", "username", user.Username, "id", user.ID)
	}
}

// serve runs srv on ln until ctx is canceled, then stops accepting connections
// and gives in-flight requests up to grace to finish. Requests still running
// when the grace period expires are dropped rather than hanging the process.
func serve(ctx context.Context, log *slog.Logger, srv *http.Server, ln net.Listener, grace time.Duration) error {
	serveErr := make(chan error, 1)
	go func() {
		err := srv.Serve(ln)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serveErr <- err
	}()

	select {
	case err := <-serveErr:
		// Serve returned on its own: the listener broke.
		if err != nil {
			return fmt.Errorf("serve: %w", err)
		}
		return nil
	case <-ctx.Done():
		log.Info("shutting down", "grace", grace)
	}

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), grace)
	defer cancel()

	shutdownErr := srv.Shutdown(shutdownCtx)
	if shutdownErr != nil {
		_ = srv.Close()
	}
	if err := <-serveErr; err != nil {
		return fmt.Errorf("serve: %w", err)
	}
	if shutdownErr != nil {
		return fmt.Errorf("graceful shutdown: %w", shutdownErr)
	}

	log.Info("stopped")
	return nil
}

func newLogger(cfg *config.Config) *slog.Logger {
	opts := &slog.HandlerOptions{Level: cfg.LogLevel}
	if cfg.LogFormat == config.LogFormatJSON {
		return slog.New(slog.NewJSONHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stdout, opts))
}

// buildVersion reports the VCS revision stamped into the binary by the Go
// toolchain, falling back to the module version and then to "dev".
func buildVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	var revision, modified string
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			modified = s.Value
		}
	}
	switch {
	case revision != "" && modified == "true":
		return shortRevision(revision) + "-dirty"
	case revision != "":
		return shortRevision(revision)
	case info.Main.Version != "" && info.Main.Version != "(devel)":
		return info.Main.Version
	default:
		return "dev"
	}
}

func shortRevision(rev string) string {
	if len(rev) > 12 {
		return rev[:12]
	}
	return rev
}
