package config

import (
	"fmt"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server    Server    `yaml:"server"`
	Database  Database  `yaml:"database"`
	Log       Log       `yaml:"log"`
	Scheduler Scheduler `yaml:"scheduler"`
	Retention Retention `yaml:"retention"`
	SSRF      SSRF      `yaml:"ssrf"`
	Admin     Admin     `yaml:"admin"`
}

type Server struct {
	Listen  string `yaml:"listen"`
	BaseURL string `yaml:"base_url"`
}

type Database struct {
	Path string `yaml:"path"`
}

type Log struct {
	Level string `yaml:"level"`
}

type Scheduler struct {
	Workers int `yaml:"workers"`
}

type Retention struct {
	RawDays     int `yaml:"raw_days"`
	SummaryDays int `yaml:"summary_days"`
}

type SSRF struct {
	BlockPrivate bool `yaml:"block_private"`
}

type Admin struct {
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

func Load(path string) (Config, error) {
	cfg := Config{}
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return Config{}, fmt.Errorf("read config: %w", err)
		}
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return Config{}, fmt.Errorf("parse config: %w", err)
		}
	}
	cfg.applyDefaults()
	cfg.applyEnv()
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c *Config) applyDefaults() {
	if c.Server.Listen == "" {
		c.Server.Listen = ":8080"
	}
	if c.Database.Path == "" {
		c.Database.Path = "tickwatch.db"
	}
	if c.Log.Level == "" {
		c.Log.Level = "info"
	}
	if c.Scheduler.Workers == 0 {
		c.Scheduler.Workers = 16
	}
	if c.Retention.RawDays == 0 {
		c.Retention.RawDays = 30
	}
	if c.Retention.SummaryDays == 0 {
		c.Retention.SummaryDays = 365
	}
	if c.Admin.Username == "" {
		c.Admin.Username = "admin"
	}
}

func (c *Config) applyEnv() {
	envStr := func(key string, dst *string) {
		if v := os.Getenv(key); v != "" {
			*dst = v
		}
	}
	envInt := func(key string, dst *int) {
		if v := os.Getenv(key); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				*dst = n
			}
		}
	}
	envBool := func(key string, dst *bool) {
		v := strings.ToLower(os.Getenv(key))
		switch v {
		case "1", "true", "yes", "on":
			*dst = true
		case "0", "false", "no", "off":
			*dst = false
		}
	}

	envStr("TICKWATCH_LISTEN", &c.Server.Listen)
	envStr("TICKWATCH_BASE_URL", &c.Server.BaseURL)
	envStr("TICKWATCH_DB_PATH", &c.Database.Path)
	envStr("TICKWATCH_LOG_LEVEL", &c.Log.Level)
	envInt("TICKWATCH_WORKERS", &c.Scheduler.Workers)
	envInt("TICKWATCH_RAW_DAYS", &c.Retention.RawDays)
	envInt("TICKWATCH_SUMMARY_DAYS", &c.Retention.SummaryDays)
	envBool("TICKWATCH_SSRF_BLOCK_PRIVATE", &c.SSRF.BlockPrivate)
	envStr("TICKWATCH_ADMIN_USERNAME", &c.Admin.Username)
	envStr("TICKWATCH_ADMIN_PASSWORD", &c.Admin.Password)
}

func (c *Config) Validate() error {
	if c.Server.BaseURL != "" {
		u, err := url.Parse(c.Server.BaseURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("server.base_url must be an absolute http or https url")
		}
	}
	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("log.level must be one of debug, info, warn, error")
	}
	if c.Scheduler.Workers < 1 || c.Scheduler.Workers > 512 {
		return fmt.Errorf("scheduler.workers must be between 1 and 512")
	}
	if c.Retention.RawDays < 1 || c.Retention.RawDays > 3650 {
		return fmt.Errorf("retention.raw_days must be between 1 and 3650")
	}
	if c.Retention.SummaryDays < c.Retention.RawDays || c.Retention.SummaryDays > 36500 {
		return fmt.Errorf("retention.summary_days must be between raw_days and 36500")
	}
	if strings.ContainsRune(c.Admin.Username, ' ') {
		return fmt.Errorf("admin.username must not contain spaces")
	}
	return nil
}

func DefaultWorkers() int {
	n := runtime.NumCPU() * 2
	if n < 4 {
		return 4
	}
	return n
}
