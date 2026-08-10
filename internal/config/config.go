// Package config loads and validates the server's runtime configuration.
//
// Every setting comes from the process environment. Nothing is read from a
// config file and nothing is mutated after [Load] returns, so a *Config is safe
// to share across goroutines.
//
// All Hark-specific variables use the HARK_ prefix; the sole exception is the
// conventional DATABASE_URL.
package config

import (
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Environment names the deployment mode. It only ever relaxes or tightens
// operator ergonomics (log format, error verbosity); it never changes API
// behaviour.
type Environment string

const (
	EnvDevelopment Environment = "development"
	EnvProduction  Environment = "production"
)

// Defaults applied when a variable is unset or empty.
const (
	DefaultListenAddr      = ":8080"
	DefaultPublicURL       = "http://localhost:8080"
	DefaultAdminUsername   = "admin"
	DefaultAPNsBundleID    = "dev.abdeen.hark"
	DefaultAPNsEnvironment = "sandbox"

	DefaultShutdownTimeout = 20 * time.Second
	DefaultMaxRequestBytes = 64 << 10 // 64 KiB

	DefaultDBMaxConns        = 10
	DefaultDBMinConns        = 0
	DefaultDBConnectTimeout  = 10 * time.Second
	DefaultDBMaxConnLifetime = time.Hour
	DefaultDBMaxConnIdleTime = 30 * time.Minute

	DefaultRequesterRatePerMinute = 300
	DefaultAccountRatePerMinute   = 1500

	// DefaultAPNsAttemptRetentionDays is how many days of APNs delivery-attempt
	// rows the daily maintenance pruner keeps. The rows are diagnostic-only —
	// nothing reads them at request time — so a month of "why did this Live
	// Activity never appear" is plenty.
	DefaultAPNsAttemptRetentionDays = 30
	// MinAPNsAttemptRetentionDays and MaxAPNsAttemptRetentionDays bound the
	// override: at least one day so the pruner never eats what was just written,
	// at most ten years so a typo'd value fails at boot instead of quietly
	// meaning "forever".
	MinAPNsAttemptRetentionDays = 1
	MaxAPNsAttemptRetentionDays = 3650

	// MinSecretKeyLen is the shortest accepted HARK_SECRET_KEY. The key is the
	// root of every derived encryption and MAC key, so it must carry real
	// entropy; 32 characters is the floor for a base64url-encoded 24-byte
	// random value.
	MinSecretKeyLen = 32
)

// Config is the fully validated configuration of one harkd process.
type Config struct {
	Environment     Environment
	ListenAddr      string
	PublicURL       *url.URL // absolute, no trailing slash, no query or fragment
	ShutdownTimeout time.Duration
	MaxRequestBytes int64
	LogLevel        slog.Level
	LogFormat       LogFormat

	// SecretKey is the root secret from which every at-rest encryption key and
	// MAC key is derived. Rotating it invalidates all stored ciphertexts.
	SecretKey []byte

	// TrustedClientIPHeader names the single header a trusted reverse proxy
	// overwrites with the real client address. Empty means "do not trust any
	// header"; per-client rate limiting is then disabled rather than keyed off
	// a spoofable value.
	TrustedClientIPHeader string

	// APNsAttemptRetentionDays is how many days of APNs delivery-attempt audit
	// rows the daily maintenance pruner keeps. It governs only that diagnostic
	// table, never notification or event history.
	APNsAttemptRetentionDays int

	Database  Database
	Admin     Admin
	APNs      APNs
	RateLimit RateLimit
}

// Database describes the PostgreSQL connection pool.
type Database struct {
	URL             string // postgres:// or postgresql:// DSN
	MaxConns        int32
	MinConns        int32
	ConnectTimeout  time.Duration
	MaxConnLifetime time.Duration
	MaxConnIdleTime time.Duration
	AutoMigrate     bool
}

// Admin seeds the single account of this deployment. Hark is a private,
// single-user service: there is no sign-up surface, so the one account is
// created at boot from these values when the user table is empty.
type Admin struct {
	Username string
	Password string // empty means "do not seed"; nobody can sign in
	Email    string
}

// Seedable reports whether enough is configured to create the account.
func (a Admin) Seedable() bool { return a.Password != "" }

// APNs holds the Apple Push Notification service provider credentials.
type APNs struct {
	KeyID       string
	TeamID      string
	PrivateKey  []byte // PKCS#8 PEM
	BundleID    string
	Environment string // "sandbox" or "production"
}

// Configured reports whether pushes can be sent at all. When false, the server
// still runs and records every send as a provider-not-configured failure.
func (a APNs) Configured() bool {
	return a.KeyID != "" && a.TeamID != "" && len(a.PrivateKey) > 0
}

// RateLimit holds the request ceilings applied per requester and per account.
type RateLimit struct {
	RequesterPerMinute int
	AccountPerMinute   int
}

// LogFormat selects the slog handler.
type LogFormat string

const (
	LogFormatText LogFormat = "text"
	LogFormatJSON LogFormat = "json"
)

// Getenv reads one environment variable. [os.Getenv] satisfies it; tests pass a
// map lookup so that no test mutates the real process environment.
type Getenv func(string) string

// Load reads, defaults, and validates the configuration.
//
// Every problem found is reported: the returned error joins all validation
// failures so an operator fixes the whole file in one pass instead of
// discovering issues one restart at a time.
func Load(getenv Getenv) (*Config, error) {
	l := &loader{getenv: getenv}

	cfg := &Config{
		Environment:           l.environment("HARK_ENV"),
		ListenAddr:            l.str("HARK_LISTEN_ADDR", DefaultListenAddr),
		PublicURL:             l.publicURL("HARK_PUBLIC_URL", DefaultPublicURL),
		ShutdownTimeout:       l.duration("HARK_SHUTDOWN_TIMEOUT", DefaultShutdownTimeout),
		MaxRequestBytes:       int64(l.positiveInt("HARK_MAX_REQUEST_BYTES", DefaultMaxRequestBytes)),
		LogLevel:              l.logLevel("HARK_LOG_LEVEL"),
		SecretKey:             l.secretKey("HARK_SECRET_KEY"),
		TrustedClientIPHeader: strings.TrimSpace(l.str("HARK_TRUSTED_CLIENT_IP_HEADER", "")),

		APNsAttemptRetentionDays: l.intInRange("HARK_APNS_ATTEMPT_RETENTION_DAYS",
			DefaultAPNsAttemptRetentionDays, MinAPNsAttemptRetentionDays, MaxAPNsAttemptRetentionDays),

		Database: Database{
			URL:             l.databaseURL("DATABASE_URL"),
			MaxConns:        int32(l.positiveInt("HARK_DB_MAX_CONNS", DefaultDBMaxConns)),
			MinConns:        int32(l.nonNegativeInt("HARK_DB_MIN_CONNS", DefaultDBMinConns)),
			ConnectTimeout:  l.duration("HARK_DB_CONNECT_TIMEOUT", DefaultDBConnectTimeout),
			MaxConnLifetime: l.duration("HARK_DB_MAX_CONN_LIFETIME", DefaultDBMaxConnLifetime),
			MaxConnIdleTime: l.duration("HARK_DB_MAX_CONN_IDLE_TIME", DefaultDBMaxConnIdleTime),
			AutoMigrate:     l.boolean("HARK_DB_AUTO_MIGRATE", true),
		},
		RateLimit: RateLimit{
			RequesterPerMinute: l.positiveInt("HARK_RATE_LIMIT_REQUESTER_PER_MINUTE", DefaultRequesterRatePerMinute),
			AccountPerMinute:   l.positiveInt("HARK_RATE_LIMIT_ACCOUNT_PER_MINUTE", DefaultAccountRatePerMinute),
		},
	}

	cfg.LogFormat = l.logFormat("HARK_LOG_FORMAT", cfg.Environment)
	cfg.Admin = l.admin()
	cfg.APNs = l.apns()

	if cfg.Database.MinConns > cfg.Database.MaxConns {
		l.errorf("HARK_DB_MIN_CONNS (%d) must not exceed HARK_DB_MAX_CONNS (%d)",
			cfg.Database.MinConns, cfg.Database.MaxConns)
	}

	if err := l.err(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// LoadFromEnv is the production entry point.
func LoadFromEnv() (*Config, error) { return Load(os.Getenv) }

// Warnings lists non-fatal configuration gaps worth telling the operator about
// at boot. They are returned rather than logged so the caller owns the logger.
func (c *Config) Warnings() []string {
	var w []string
	if !c.Admin.Seedable() {
		w = append(w, "HARK_ADMIN_PASSWORD is not set: no account will be created and nobody can sign in")
	}
	if !c.APNs.Configured() {
		w = append(w, "APNs is not configured (HARK_APNS_KEY_ID, HARK_APNS_TEAM_ID, HARK_APNS_PRIVATE_KEY): no push notifications can be delivered")
	}
	if c.TrustedClientIPHeader == "" {
		w = append(w, "HARK_TRUSTED_CLIENT_IP_HEADER is not set: per-client rate limiting is disabled and only global buckets apply")
	}
	return w
}

// LogValue renders the configuration for structured logging with every secret
// omitted. It satisfies [slog.LogValuer], so passing a *Config to a logger can
// never leak the secret key, the admin password, or the database password.
func (c *Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("environment", string(c.Environment)),
		slog.String("listen_addr", c.ListenAddr),
		slog.String("public_url", c.PublicURL.String()),
		slog.String("log_level", c.LogLevel.String()),
		slog.String("log_format", string(c.LogFormat)),
		slog.String("database_url", RedactURL(c.Database.URL)),
		slog.Int("database_max_conns", int(c.Database.MaxConns)),
		slog.Bool("database_auto_migrate", c.Database.AutoMigrate),
		slog.String("admin_username", c.Admin.Username),
		slog.Bool("admin_seedable", c.Admin.Seedable()),
		slog.Bool("apns_configured", c.APNs.Configured()),
		slog.String("apns_environment", c.APNs.Environment),
		slog.String("apns_bundle_id", c.APNs.BundleID),
		slog.Int("apns_attempt_retention_days", c.APNsAttemptRetentionDays),
		slog.Int("rate_limit_requester_per_minute", c.RateLimit.RequesterPerMinute),
		slog.Int("rate_limit_account_per_minute", c.RateLimit.AccountPerMinute),
	)
}

// RedactURL replaces the password in a connection URL with "xxxxx" so the DSN
// can appear in logs and error messages. Unparseable input is reported as
// "<invalid url>" rather than echoed, since it may itself be a credential.
func RedactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "<invalid url>"
	}
	if u.User != nil {
		if _, hasPassword := u.User.Password(); hasPassword {
			u.User = url.UserPassword(u.User.Username(), "xxxxx")
		}
	}
	return u.String()
}

// loader accumulates validation failures so Load can report them all at once.
type loader struct {
	getenv Getenv
	errs   []error
}

func (l *loader) errorf(format string, args ...any) {
	l.errs = append(l.errs, fmt.Errorf(format, args...))
}

func (l *loader) err() error {
	if len(l.errs) == 0 {
		return nil
	}
	return fmt.Errorf("invalid configuration:\n  %w", joinLines(l.errs))
}

func joinLines(errs []error) error {
	msgs := make([]string, len(errs))
	for i, e := range errs {
		msgs[i] = e.Error()
	}
	return errors.New(strings.Join(msgs, "\n  "))
}

func (l *loader) raw(key string) string { return strings.TrimSpace(l.getenv(key)) }

func (l *loader) str(key, fallback string) string {
	if v := l.raw(key); v != "" {
		return v
	}
	return fallback
}

func (l *loader) environment(key string) Environment {
	switch v := l.str(key, string(EnvDevelopment)); Environment(v) {
	case EnvDevelopment:
		return EnvDevelopment
	case EnvProduction:
		return EnvProduction
	default:
		l.errorf("%s: must be %q or %q, got %q", key, EnvDevelopment, EnvProduction, v)
		return EnvDevelopment
	}
}

func (l *loader) logLevel(key string) slog.Level {
	v := strings.ToLower(l.str(key, "info"))
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(v)); err != nil {
		l.errorf("%s: must be one of debug, info, warn, error, got %q", key, v)
		return slog.LevelInfo
	}
	return lvl
}

func (l *loader) logFormat(key string, env Environment) LogFormat {
	fallback := LogFormatText
	if env == EnvProduction {
		fallback = LogFormatJSON
	}
	switch v := LogFormat(strings.ToLower(l.str(key, string(fallback)))); v {
	case LogFormatText, LogFormatJSON:
		return v
	default:
		l.errorf("%s: must be %q or %q, got %q", key, LogFormatText, LogFormatJSON, v)
		return fallback
	}
}

func (l *loader) positiveInt(key string, fallback int) int {
	v := l.raw(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		l.errorf("%s: must be a positive integer, got %q", key, v)
		return fallback
	}
	return n
}

// intInRange accepts an integer in the closed range [lo, hi]. Everything else —
// zero, negative, fractional, non-numeric, out of range — is one error naming
// the variable and the range, so a mistake fails at boot rather than becoming a
// surprising duration later.
func (l *loader) intInRange(key string, fallback, lo, hi int) int {
	v := l.raw(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < lo || n > hi {
		l.errorf("%s: must be an integer between %d and %d, got %q", key, lo, hi, v)
		return fallback
	}
	return n
}

func (l *loader) nonNegativeInt(key string, fallback int) int {
	v := l.raw(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		l.errorf("%s: must be a non-negative integer, got %q", key, v)
		return fallback
	}
	return n
}

func (l *loader) boolean(key string, fallback bool) bool {
	v := l.raw(key)
	if v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		l.errorf("%s: must be a boolean (true/false), got %q", key, v)
		return fallback
	}
	return b
}

func (l *loader) duration(key string, fallback time.Duration) time.Duration {
	v := l.raw(key)
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		l.errorf("%s: must be a positive Go duration such as 30s or 5m, got %q", key, v)
		return fallback
	}
	return d
}

func (l *loader) publicURL(key, fallback string) *url.URL {
	raw := l.str(key, fallback)
	u, err := url.Parse(raw)
	if err != nil {
		l.errorf("%s: must be an absolute URL, got %q", key, raw)
		return &url.URL{Scheme: "http", Host: "localhost:8080"}
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		l.errorf("%s: scheme must be http or https, got %q", key, raw)
	}
	if u.Host == "" {
		l.errorf("%s: must include a host, got %q", key, raw)
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawQuery = ""
	u.Fragment = ""
	return u
}

func (l *loader) databaseURL(key string) string {
	raw := l.raw(key)
	if raw == "" {
		return l.missing(key, "a PostgreSQL connection URL, e.g. postgres://hark:secret@localhost:5432/hark?sslmode=disable")
	}
	u, err := url.Parse(raw)
	if err != nil {
		l.errorf("%s: must be a valid URL", key) // never echo: it carries a password
		return raw
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		l.errorf("%s: scheme must be postgres:// or postgresql://, got %q", key, u.Scheme)
	}
	if u.Host == "" {
		l.errorf("%s: must include a host", key)
	}
	return raw
}

func (l *loader) secretKey(key string) []byte {
	v := l.raw(key)
	switch {
	case v == "":
		l.missing(key, fmt.Sprintf("a random string of at least %d characters, e.g. `openssl rand -base64 48`", MinSecretKeyLen))
		return nil
	case len(v) < MinSecretKeyLen:
		l.errorf("%s: must be at least %d characters, got %d", key, MinSecretKeyLen, len(v))
		return nil
	}
	return []byte(v)
}

func (l *loader) missing(key, hint string) string {
	l.errorf("%s: required, set it to %s", key, hint)
	return ""
}

// usernamePattern mirrors the sign-in handle rules: 3-30 characters of ASCII
// letters, digits, underscore, and dot.
var usernamePattern = regexp.MustCompile(`^[a-zA-Z0-9_.]{3,30}$`)

// Password bounds, checked here only so a bad HARK_ADMIN_PASSWORD fails at boot
// instead of at the first seeding attempt. They must match the policy in
// internal/auth, which is where it is actually enforced; config deliberately
// does not import that package, since pulling the credential layer (and the
// database driver behind it) into configuration parsing would invert the
// dependency the whole package exists to avoid.
const (
	minPasswordLen = 12
	maxPasswordLen = 256
)

func (l *loader) admin() Admin {
	a := Admin{
		Username: l.str("HARK_ADMIN_USERNAME", DefaultAdminUsername),
		Password: l.getenv("HARK_ADMIN_PASSWORD"), // never trimmed: spaces are legal
		Email:    strings.ToLower(l.raw("HARK_ADMIN_EMAIL")),
	}
	if !usernamePattern.MatchString(a.Username) {
		l.errorf("HARK_ADMIN_USERNAME: must be 3-30 characters of letters, digits, underscore, or dot, got %q", a.Username)
	}
	if a.Password != "" {
		if n := len([]rune(a.Password)); n < minPasswordLen || n > maxPasswordLen {
			l.errorf("HARK_ADMIN_PASSWORD: must be %d-%d characters, got %d", minPasswordLen, maxPasswordLen, n)
		}
	}
	if a.Email == "" {
		a.Email = a.Username + "@hark.local"
	} else if !strings.Contains(a.Email, "@") {
		l.errorf("HARK_ADMIN_EMAIL: must be an email address, got %q", a.Email)
	}
	return a
}

func (l *loader) apns() APNs {
	a := APNs{
		KeyID:       l.raw("HARK_APNS_KEY_ID"),
		TeamID:      l.raw("HARK_APNS_TEAM_ID"),
		BundleID:    l.str("HARK_APNS_BUNDLE_ID", DefaultAPNsBundleID),
		Environment: strings.ToLower(l.str("HARK_APNS_ENVIRONMENT", DefaultAPNsEnvironment)),
	}
	if a.Environment != "sandbox" && a.Environment != "production" {
		l.errorf("HARK_APNS_ENVIRONMENT: must be \"sandbox\" or \"production\", got %q", a.Environment)
	}

	rawKey := l.getenv("HARK_APNS_PRIVATE_KEY")
	keyFile := l.raw("HARK_APNS_PRIVATE_KEY_FILE")
	switch {
	case rawKey != "" && keyFile != "":
		l.errorf("HARK_APNS_PRIVATE_KEY and HARK_APNS_PRIVATE_KEY_FILE: set one, not both")
	case keyFile != "":
		b, err := os.ReadFile(keyFile)
		if err != nil {
			l.errorf("HARK_APNS_PRIVATE_KEY_FILE: %v", err)
		} else {
			rawKey = string(b)
		}
	}
	if rawKey != "" {
		pemBytes, err := normalizePrivateKey(rawKey)
		if err != nil {
			l.errorf("HARK_APNS_PRIVATE_KEY: %v", err)
		} else {
			a.PrivateKey = pemBytes
		}
	}

	// Partial credentials are a misconfiguration, not a "push disabled" state:
	// the operator clearly meant to enable pushes and got it wrong.
	set := 0
	for _, present := range []bool{a.KeyID != "", a.TeamID != "", len(a.PrivateKey) > 0} {
		if present {
			set++
		}
	}
	if set > 0 && set < 3 {
		l.errorf("HARK_APNS_KEY_ID, HARK_APNS_TEAM_ID, and HARK_APNS_PRIVATE_KEY must be set together or left entirely unset")
	}
	return a
}

// normalizePrivateKey accepts an APNs .p8 key as PEM text, as PEM text whose
// newlines were escaped as the two characters `\n` (the usual shape when a key
// is pasted into a single-line environment variable), or as base64 of either.
func normalizePrivateKey(raw string) ([]byte, error) {
	candidate := strings.TrimSpace(strings.ReplaceAll(raw, `\n`, "\n"))
	if !strings.Contains(candidate, "PRIVATE KEY") {
		decoded, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(candidate), ""))
		if err != nil {
			return nil, errors.New("must be a PEM private key or its base64 encoding")
		}
		candidate = strings.TrimSpace(string(decoded))
	}
	block, _ := pem.Decode([]byte(candidate))
	if block == nil || !strings.Contains(block.Type, "PRIVATE KEY") {
		return nil, errors.New("must be a PEM private key or its base64 encoding")
	}
	return []byte(candidate), nil
}
