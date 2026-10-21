// Package config reads DeedFlow's settings from the environment. The variable
// names are the ones the Django project used (see .env.example), so an
// existing .env keeps working.
package config

import (
	"bufio"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // the portal's India time zone must work in a bare container
)

// Config holds every setting the application reads.
type Config struct {
	SecretKey          string
	Debug              bool
	AllowedHosts       []string
	FieldEncryptionKey string

	DatabaseURL string
	Addr        string

	MediaRoot string
	MediaURL  string

	SeleniumRemoteURL string
	PortalURL         string

	OpenAIAPIKey     string
	OpenAIModel      string
	AnthropicAPIKey  string
	AnthropicModel   string
	AIClaudeFallback bool

	TesseractCmd string
	PMD2JSONBin  string

	AWSRegion        string
	AWSSESSender     string
	EmailHost        string
	EmailPort        int
	EmailHostUser    string
	EmailHostPass    string
	EmailUseTLS      bool
	SiteURL          string
	DefaultFromEmail string

	// TimeZone is the office's (and the portal's) clock: Asia/Kolkata.
	TimeZone *time.Location

	// SubmissionWorkers / ProcessingWorkers bound how many jobs of each kind a
	// worker runs at once (the Celery worker ran --concurrency=4).
	ProcessingWorkers int
}

// LoadDotEnv reads KEY=VALUE lines from path into the environment without
// overriding variables that are already set. A missing file is not an error.
func LoadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if len(value) >= 2 && (value[0] == '"' && value[len(value)-1] == '"' || value[0] == '\'' && value[len(value)-1] == '\'') {
			value = value[1 : len(value)-1]
		}
		if _, set := os.LookupEnv(key); !set {
			os.Setenv(key, value)
		}
	}
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v, ok := os.LookupEnv(key); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return n
		}
	}
	return def
}

// Load builds the Config from the environment (after reading ./.env).
func Load() (*Config, error) {
	LoadDotEnv(".env")
	c := &Config{
		SecretKey:          env("SECRET_KEY", "insecure-dev-key-change-me"),
		Debug:              env("DEBUG", "0") == "1",
		FieldEncryptionKey: env("FIELD_ENCRYPTION_KEY", ""),
		Addr:               env("HTTP_ADDR", ":8000"),
		MediaRoot:          env("MEDIA_ROOT", "media"),
		MediaURL:           "/media/",
		SeleniumRemoteURL:  env("SELENIUM_REMOTE_URL", "http://selenium:4444/wd/hub"),
		PortalURL:          env("PORTAL_URL", ""),
		OpenAIAPIKey:       env("OPENAI_API_KEY", ""),
		OpenAIModel:        env("OPENAI_MODEL", ""),
		AnthropicAPIKey:    env("ANTHROPIC_API_KEY", ""),
		AnthropicModel:     env("ANTHROPIC_MODEL", "claude-sonnet-5"),
		AIClaudeFallback:   env("AI_CLAUDE_FALLBACK", "0") == "1",
		TesseractCmd:       env("TESSERACT_CMD", "tesseract"),
		PMD2JSONBin:        env("PMD2JSON_BIN", "pmd2json"),
		AWSRegion:          env("AWS_REGION", "ap-south-1"),
		AWSSESSender:       strings.Trim(strings.TrimSpace(env("AWS_SES_SENDER", "")), `"'`),
		EmailHost:          env("EMAIL_HOST", "localhost"),
		EmailPort:          envInt("EMAIL_PORT", 587),
		EmailHostUser:      env("EMAIL_HOST_USER", ""),
		EmailHostPass:      env("EMAIL_HOST_PASSWORD", ""),
		EmailUseTLS:        env("EMAIL_USE_TLS", "1") == "1",
		ProcessingWorkers:  envInt("PROCESSING_WORKERS", 4),
	}
	for _, h := range strings.Split(env("ALLOWED_HOSTS", "localhost,127.0.0.1"), ",") {
		if h = strings.TrimSpace(h); h != "" {
			c.AllowedHosts = append(c.AllowedHosts, h)
		}
	}
	loc, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		return nil, err
	}
	c.TimeZone = loc

	c.SiteURL = strings.TrimRight(env("SITE_URL", ""), "/")
	if c.SiteURL == "" {
		for _, h := range c.AllowedHosts {
			if h != "localhost" && h != "127.0.0.1" && h != "web" && h != "*" {
				c.SiteURL = "https://" + h
				break
			}
		}
	}
	c.DefaultFromEmail = c.AWSSESSender
	if c.DefaultFromEmail == "" {
		c.DefaultFromEmail = env("DEFAULT_FROM_EMAIL", env("EMAIL_HOST_USER", ""))
	}
	if c.DefaultFromEmail == "" {
		c.DefaultFromEmail = "deedflow@localhost"
	}

	c.DatabaseURL = env("DATABASE_URL", "")
	if c.DatabaseURL == "" {
		u := url.URL{
			Scheme: "postgres",
			User:   url.UserPassword(env("POSTGRES_USER", "deedflow"), env("POSTGRES_PASSWORD", "deedflow")),
			Host:   env("POSTGRES_HOST", "db") + ":" + env("POSTGRES_PORT", "5432"),
			Path:   env("POSTGRES_DB", "deedflow"),
		}
		q := url.Values{}
		q.Set("sslmode", env("POSTGRES_SSLMODE", "disable"))
		u.RawQuery = q.Encode()
		c.DatabaseURL = u.String()
	}
	if abs, err := filepath.Abs(c.MediaRoot); err == nil {
		c.MediaRoot = abs
	}
	return c, nil
}

// HostAllowed reports whether a Host header is acceptable (Django's
// ALLOWED_HOSTS semantics, including a leading-dot subdomain wildcard).
func (c *Config) HostAllowed(host string) bool {
	if i := strings.LastIndex(host, ":"); i >= 0 && !strings.HasSuffix(host, "]") {
		host = host[:i]
	}
	host = strings.ToLower(host)
	if c.Debug && len(c.AllowedHosts) == 0 {
		return true
	}
	for _, a := range c.AllowedHosts {
		a = strings.ToLower(a)
		switch {
		case a == "*", a == host:
			return true
		case strings.HasPrefix(a, ".") && (host == a[1:] || strings.HasSuffix(host, a)):
			return true
		}
	}
	return false
}

// String is safe to log: it never includes secrets.
func (c *Config) String() string {
	return fmt.Sprintf("Config{addr=%s debug=%v media=%s}", c.Addr, c.Debug, c.MediaRoot)
}
