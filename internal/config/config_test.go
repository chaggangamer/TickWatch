package config

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "tickwatch.yml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDefaults(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("load empty config: %v", err)
	}
	if cfg.Server.Listen != ":8080" {
		t.Errorf("listen = %q, want :8080", cfg.Server.Listen)
	}
	if cfg.Database.Path != "tickwatch.db" {
		t.Errorf("db path = %q, want tickwatch.db", cfg.Database.Path)
	}
	if cfg.Log.Level != "info" {
		t.Errorf("log level = %q, want info", cfg.Log.Level)
	}
	if cfg.Scheduler.Workers != 16 {
		t.Errorf("workers = %d, want 16", cfg.Scheduler.Workers)
	}
	if cfg.Retention.RawDays != 30 {
		t.Errorf("raw days = %d, want 30", cfg.Retention.RawDays)
	}
	if cfg.Retention.SummaryDays != 365 {
		t.Errorf("summary days = %d, want 365", cfg.Retention.SummaryDays)
	}
	if cfg.Admin.Username != "admin" {
		t.Errorf("admin username = %q, want admin", cfg.Admin.Username)
	}
}

func TestParseYAML(t *testing.T) {
	p := write(t, `
server:
  listen: ":9090"
  base_url: "https://tick.example.com"
database:
  path: "/data/tw.db"
log:
  level: "debug"
scheduler:
  workers: 8
retention:
  raw_days: 14
  summary_days: 180
ssrf:
  block_private: true
admin:
  username: "ops"
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Server.Listen != ":9090" {
		t.Errorf("listen = %q, want :9090", cfg.Server.Listen)
	}
	if cfg.Server.BaseURL != "https://tick.example.com" {
		t.Errorf("base url = %q", cfg.Server.BaseURL)
	}
	if cfg.Database.Path != "/data/tw.db" {
		t.Errorf("db path = %q", cfg.Database.Path)
	}
	if cfg.Log.Level != "debug" || cfg.Scheduler.Workers != 8 {
		t.Errorf("log level %q, workers %d", cfg.Log.Level, cfg.Scheduler.Workers)
	}
	if cfg.Retention.RawDays != 14 || cfg.Retention.SummaryDays != 180 {
		t.Errorf("retention %d/%d", cfg.Retention.RawDays, cfg.Retention.SummaryDays)
	}
	if !cfg.SSRF.BlockPrivate {
		t.Error("ssrf.block_private = false, want true")
	}
	if cfg.Admin.Username != "ops" {
		t.Errorf("admin username = %q, want ops", cfg.Admin.Username)
	}
}

func TestEnvOverrides(t *testing.T) {
	t.Setenv("TICKWATCH_LISTEN", ":9999")
	t.Setenv("TICKWATCH_DB_PATH", "/tmp/x.db")
	t.Setenv("TICKWATCH_LOG_LEVEL", "warn")
	t.Setenv("TICKWATCH_WORKERS", "4")
	t.Setenv("TICKWATCH_RAW_DAYS", "7")
	t.Setenv("TICKWATCH_SUMMARY_DAYS", "60")
	t.Setenv("TICKWATCH_SSRF_BLOCK_PRIVATE", "true")
	t.Setenv("TICKWATCH_ADMIN_USERNAME", "root")
	t.Setenv("TICKWATCH_ADMIN_PASSWORD", "secret")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Server.Listen != ":9999" {
		t.Errorf("listen = %q, want :9999", cfg.Server.Listen)
	}
	if cfg.Database.Path != "/tmp/x.db" {
		t.Errorf("db path = %q", cfg.Database.Path)
	}
	if cfg.Log.Level != "warn" {
		t.Errorf("log level = %q", cfg.Log.Level)
	}
	if cfg.Scheduler.Workers != 4 {
		t.Errorf("workers = %d", cfg.Scheduler.Workers)
	}
	if cfg.Retention.RawDays != 7 || cfg.Retention.SummaryDays != 60 {
		t.Errorf("retention = %d/%d", cfg.Retention.RawDays, cfg.Retention.SummaryDays)
	}
	if !cfg.SSRF.BlockPrivate {
		t.Error("ssrf.block_private = false, want true")
	}
	if cfg.Admin.Username != "root" || cfg.Admin.Password != "secret" {
		t.Errorf("admin = %q/%q", cfg.Admin.Username, cfg.Admin.Password)
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Config)
		wantErr bool
	}{
		{"valid", func(c *Config) {}, false},
		{"bad level", func(c *Config) { c.Log.Level = "verbose" }, true},
		{"zero workers", func(c *Config) { c.Scheduler.Workers = 0 }, true},
		{"huge workers", func(c *Config) { c.Scheduler.Workers = 1000 }, true},
		{"zero raw days", func(c *Config) { c.Retention.RawDays = 0 }, true},
		{"summary below raw", func(c *Config) { c.Retention.SummaryDays = 10 }, true},
		{"bad base url", func(c *Config) { c.Server.BaseURL = "ftp://x" }, true},
		{"relative base url", func(c *Config) { c.Server.BaseURL = "/only/path" }, true},
		{"good base url", func(c *Config) { c.Server.BaseURL = "http://localhost:8080" }, false},
		{"bad admin name", func(c *Config) { c.Admin.Username = "two words" }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Load("")
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			tc.mutate(&cfg)
			err = cfg.Validate()
			if tc.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "missing.yml"))
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestLoadBadYAML(t *testing.T) {
	p := write(t, "server: {listen: [unclosed")
	if _, err := Load(p); err == nil {
		t.Error("expected parse error")
	}
}
