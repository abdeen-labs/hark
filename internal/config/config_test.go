package config

import (
	"encoding/base64"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	testSecret = "0123456789abcdef0123456789abcdef" // exactly MinSecretKeyLen
	testDSN    = "postgres://hark:secret@localhost:5432/hark?sslmode=disable"
)

// env builds a Getenv over a map, starting from the minimal valid set.
func env(overrides map[string]string) Getenv {
	vars := map[string]string{
		"DATABASE_URL":    testDSN,
		"HARK_SECRET_KEY": testSecret,
	}
	for k, v := range overrides {
		if v == "" {
			delete(vars, k)
			continue
		}
		vars[k] = v
	}
	return func(key string) string { return vars[key] }
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(env(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Environment != EnvDevelopment {
		t.Errorf("Environment = %q, want %q", cfg.Environment, EnvDevelopment)
	}
	if cfg.ListenAddr != DefaultListenAddr {
		t.Errorf("ListenAddr = %q, want %q", cfg.ListenAddr, DefaultListenAddr)
	}
	if got := cfg.PublicURL.String(); got != DefaultPublicURL {
		t.Errorf("PublicURL = %q, want %q", got, DefaultPublicURL)
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Errorf("LogLevel = %v, want info", cfg.LogLevel)
	}
	if cfg.LogFormat != LogFormatText {
		t.Errorf("LogFormat = %q, want text in development", cfg.LogFormat)
	}
	if cfg.MaxRequestBytes != DefaultMaxRequestBytes {
		t.Errorf("MaxRequestBytes = %d, want %d", cfg.MaxRequestBytes, DefaultMaxRequestBytes)
	}
	if !cfg.Database.AutoMigrate {
		t.Error("Database.AutoMigrate = false, want true by default")
	}
	if cfg.Database.MaxConns != DefaultDBMaxConns {
		t.Errorf("Database.MaxConns = %d, want %d", cfg.Database.MaxConns, DefaultDBMaxConns)
	}
	if cfg.Admin.Username != DefaultAdminUsername {
		t.Errorf("Admin.Username = %q, want %q", cfg.Admin.Username, DefaultAdminUsername)
	}
	if want := DefaultAdminUsername + "@hark.local"; cfg.Admin.Email != want {
		t.Errorf("Admin.Email = %q, want %q", cfg.Admin.Email, want)
	}
	if cfg.Admin.Seedable() {
		t.Error("Admin.Seedable() = true without a password")
	}
	if cfg.APNs.Configured() {
		t.Error("APNs.Configured() = true without credentials")
	}
	if cfg.APNs.BundleID != DefaultAPNsBundleID || cfg.APNs.Environment != DefaultAPNsEnvironment {
		t.Errorf("APNs defaults = %q/%q", cfg.APNs.BundleID, cfg.APNs.Environment)
	}
	if cfg.RateLimit.RequesterPerMinute != DefaultRequesterRatePerMinute ||
		cfg.RateLimit.AccountPerMinute != DefaultAccountRatePerMinute {
		t.Errorf("RateLimit = %+v", cfg.RateLimit)
	}
	if cfg.APNsAttemptRetentionDays != DefaultAPNsAttemptRetentionDays {
		t.Errorf("APNsAttemptRetentionDays = %d, want %d",
			cfg.APNsAttemptRetentionDays, DefaultAPNsAttemptRetentionDays)
	}
}

func TestLoadOverrides(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		"HARK_ENV":                             "production",
		"HARK_LISTEN_ADDR":                     "127.0.0.1:9999",
		"HARK_PUBLIC_URL":                      "https://hark.example.com/",
		"HARK_SHUTDOWN_TIMEOUT":                "45s",
		"HARK_MAX_REQUEST_BYTES":               "1024",
		"HARK_LOG_LEVEL":                       "debug",
		"HARK_TRUSTED_CLIENT_IP_HEADER":        " X-Real-IP ",
		"HARK_DB_MAX_CONNS":                    "25",
		"HARK_DB_MIN_CONNS":                    "5",
		"HARK_DB_AUTO_MIGRATE":                 "false",
		"HARK_RATE_LIMIT_REQUESTER_PER_MINUTE": "50",
		"HARK_RATE_LIMIT_ACCOUNT_PER_MINUTE":   "500",
		"HARK_APNS_ATTEMPT_RETENTION_DAYS":     "90",
		"HARK_ADMIN_USERNAME":                  "operator",
		"HARK_ADMIN_PASSWORD":                  "hunter2hunter2",
		"HARK_ADMIN_EMAIL":                     "Ops@Example.COM",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Environment != EnvProduction {
		t.Errorf("Environment = %q", cfg.Environment)
	}
	if cfg.LogFormat != LogFormatJSON {
		t.Errorf("LogFormat = %q, want json in production", cfg.LogFormat)
	}
	if got := cfg.PublicURL.String(); got != "https://hark.example.com" {
		t.Errorf("PublicURL = %q, want trailing slash stripped", got)
	}
	if cfg.ShutdownTimeout != 45*time.Second {
		t.Errorf("ShutdownTimeout = %v", cfg.ShutdownTimeout)
	}
	if cfg.TrustedClientIPHeader != "X-Real-IP" {
		t.Errorf("TrustedClientIPHeader = %q, want trimmed", cfg.TrustedClientIPHeader)
	}
	if cfg.Database.AutoMigrate {
		t.Error("Database.AutoMigrate = true, want false")
	}
	if cfg.Admin.Email != "ops@example.com" {
		t.Errorf("Admin.Email = %q, want lowercased", cfg.Admin.Email)
	}
	if !cfg.Admin.Seedable() {
		t.Error("Admin.Seedable() = false with a password set")
	}
	if cfg.APNsAttemptRetentionDays != 90 {
		t.Errorf("APNsAttemptRetentionDays = %d, want 90", cfg.APNsAttemptRetentionDays)
	}
}

func TestAPNsAttemptRetentionBounds(t *testing.T) {
	for _, bad := range []string{"0", "-7", "3651", "many", "1.5"} {
		t.Run(bad, func(t *testing.T) {
			_, err := Load(env(map[string]string{"HARK_APNS_ATTEMPT_RETENTION_DAYS": bad}))
			if err == nil {
				t.Fatalf("Load accepted %q, want an error", bad)
			}
			for _, want := range []string{"HARK_APNS_ATTEMPT_RETENTION_DAYS", "between 1 and 3650"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error does not mention %q:\n%v", want, err)
				}
			}
		})
	}
}

func TestLoadReportsEveryProblemAtOnce(t *testing.T) {
	_, err := Load(env(map[string]string{
		"DATABASE_URL":                     "",
		"HARK_SECRET_KEY":                  "too-short",
		"HARK_ENV":                         "staging",
		"HARK_ADMIN_USERNAME":              "no",
		"HARK_LOG_LEVEL":                   "loud",
		"HARK_APNS_ATTEMPT_RETENTION_DAYS": "0",
	}))
	if err == nil {
		t.Fatal("Load succeeded, want error")
	}
	for _, want := range []string{"DATABASE_URL", "HARK_SECRET_KEY", "HARK_ENV",
		"HARK_ADMIN_USERNAME", "HARK_LOG_LEVEL", "HARK_APNS_ATTEMPT_RETENTION_DAYS"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %s:\n%v", want, err)
		}
	}
}

func TestLoadRejectsNonPostgresDSN(t *testing.T) {
	_, err := Load(env(map[string]string{"DATABASE_URL": "mysql://root@localhost/hark"}))
	if err == nil || !strings.Contains(err.Error(), "postgres") {
		t.Fatalf("err = %v, want a postgres scheme complaint", err)
	}
}

func TestLoadNeverEchoesTheDSN(t *testing.T) {
	_, err := Load(env(map[string]string{"DATABASE_URL": "postgres://u:hunter2@%zz/db"}))
	if err == nil {
		t.Fatal("Load succeeded, want a parse error")
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("error leaks the database password:\n%v", err)
	}
}

func TestDatabaseConnBoundsAreChecked(t *testing.T) {
	_, err := Load(env(map[string]string{"HARK_DB_MAX_CONNS": "2", "HARK_DB_MIN_CONNS": "9"}))
	if err == nil || !strings.Contains(err.Error(), "HARK_DB_MIN_CONNS") {
		t.Fatalf("err = %v, want a min/max complaint", err)
	}
}

func TestAPNsCredentialsMustBeCompleteOrAbsent(t *testing.T) {
	_, err := Load(env(map[string]string{"HARK_APNS_KEY_ID": "ABC1234567"}))
	if err == nil || !strings.Contains(err.Error(), "HARK_APNS_TEAM_ID") {
		t.Fatalf("err = %v, want an all-or-nothing complaint", err)
	}
}

// pkcs8Key is a syntactically valid PKCS#8 PEM block. Its contents are never
// parsed by config; only the envelope is checked.
const pkcs8Key = `-----BEGIN PRIVATE KEY-----
MIGHAgEAMBMGByqGSM49AgEGCCqGSM49AwEHBG0wawIBAQQgevZzL1gdAFr88hb2
OF/2NxApJCzGCEDdfSp6VQO30hyhRANCAAQRWz+jn65BtOMvdyHKcvjBeBSDZH2r
1RTwjmYSi9R/zpBnuQ4EiMnCqfMPWiZqB4QdbAd0E7oH50VpuZ1P087G
-----END PRIVATE KEY-----`

func TestAPNsPrivateKeyForms(t *testing.T) {
	escaped := strings.ReplaceAll(pkcs8Key, "\n", `\n`)
	for name, value := range map[string]string{
		"pem":           pkcs8Key,
		"escaped-pem":   escaped,
		"base64-of-pem": base64Of(pkcs8Key),
		"padded-pem":    "\n  " + pkcs8Key + "  \n",
	} {
		t.Run(name, func(t *testing.T) {
			cfg, err := Load(env(map[string]string{
				"HARK_APNS_KEY_ID":      "ABC1234567",
				"HARK_APNS_TEAM_ID":     "TEAM123456",
				"HARK_APNS_PRIVATE_KEY": value,
			}))
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if !cfg.APNs.Configured() {
				t.Fatal("APNs.Configured() = false")
			}
			if !strings.HasPrefix(string(cfg.APNs.PrivateKey), "-----BEGIN PRIVATE KEY-----") {
				t.Errorf("PrivateKey was not normalized to PEM: %q", cfg.APNs.PrivateKey)
			}
		})
	}
}

func TestAPNsPrivateKeyFromFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "AuthKey.p8")
	if err := os.WriteFile(path, []byte(pkcs8Key), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(env(map[string]string{
		"HARK_APNS_KEY_ID":           "ABC1234567",
		"HARK_APNS_TEAM_ID":          "TEAM123456",
		"HARK_APNS_PRIVATE_KEY_FILE": path,
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.APNs.Configured() {
		t.Fatal("APNs.Configured() = false")
	}
}

func TestAPNsPrivateKeyRejectsGarbage(t *testing.T) {
	_, err := Load(env(map[string]string{
		"HARK_APNS_KEY_ID":      "ABC1234567",
		"HARK_APNS_TEAM_ID":     "TEAM123456",
		"HARK_APNS_PRIVATE_KEY": "not a key at all",
	}))
	if err == nil || !strings.Contains(err.Error(), "HARK_APNS_PRIVATE_KEY") {
		t.Fatalf("err = %v, want a private key complaint", err)
	}
}

func TestWarnings(t *testing.T) {
	cfg, err := Load(env(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := strings.Join(cfg.Warnings(), "\n")
	for _, want := range []string{"HARK_ADMIN_PASSWORD", "APNs", "HARK_TRUSTED_CLIENT_IP_HEADER"} {
		if !strings.Contains(got, want) {
			t.Errorf("warnings do not mention %s:\n%s", want, got)
		}
	}
}

func TestLogValueRedactsSecrets(t *testing.T) {
	cfg, err := Load(env(map[string]string{"HARK_ADMIN_PASSWORD": "hunter2hunter2"}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	rendered := cfg.LogValue().String()
	for _, secret := range []string{testSecret, "hunter2hunter2", "secret@localhost"} {
		if strings.Contains(rendered, secret) {
			t.Errorf("LogValue leaks %q:\n%s", secret, rendered)
		}
	}
	if !strings.Contains(rendered, "xxxxx") {
		t.Errorf("LogValue did not redact the DSN password:\n%s", rendered)
	}
}

// TestLogValueIncludesAttemptRetention pins the retention window into the
// startup configuration line: it is not a secret, and an operator reading the
// boot log should see the window that is actually in force.
func TestLogValueIncludesAttemptRetention(t *testing.T) {
	cfg, err := Load(env(map[string]string{"HARK_APNS_ATTEMPT_RETENTION_DAYS": "45"}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if rendered := cfg.LogValue().String(); !strings.Contains(rendered, "apns_attempt_retention_days=45") {
		t.Errorf("LogValue does not carry the retention window:\n%s", rendered)
	}
}

func TestRedactURL(t *testing.T) {
	for in, want := range map[string]string{
		testDSN:                               "postgres://hark:xxxxx@localhost:5432/hark?sslmode=disable",
		"postgres://hark@localhost:5432/hark": "postgres://hark@localhost:5432/hark",
		"postgres://localhost/hark":           "postgres://localhost/hark",
		"://nonsense":                         "<invalid url>",
	} {
		if got := RedactURL(in); got != want {
			t.Errorf("RedactURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func base64Of(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
