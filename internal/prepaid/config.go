package prepaid

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	RemnawaveAPIURL    string
	RemnawaveAPIToken  string
	RemnawaveCookies   string
	RemnawaveHeaders   string
	RedisURL           string
	LimitedSquadUUID   string
	UnlimitedSquadUUID string
	WebhookAddr        string
	WebhookSecret      string
	ReconcileInterval  time.Duration
	LogLevel           string
	LogFormat          string
}

func LoadConfig(envPath string) (*Config, error) {
	if envPath == "" {
		envPath = ".env"
	}
	_ = godotenv.Load(envPath)

	intervalSec, err := envInt("TRAFFIC_RECONCILE_INTERVAL", 60)
	if err != nil {
		return nil, err
	}

	cfg := &Config{
		RemnawaveAPIURL:    strings.TrimRight(strings.TrimSpace(os.Getenv("REMNAWAVE_API_URL")), "/"),
		RemnawaveAPIToken:  strings.TrimSpace(os.Getenv("REMNAWAVE_API_TOKEN")),
		RemnawaveCookies:   strings.TrimSpace(os.Getenv("REMNAWAVE_COOKIES")),
		RemnawaveHeaders:   strings.TrimSpace(os.Getenv("REMNAWAVE_HEADERS")),
		RedisURL:           envDefault("REDIS_URL", "redis://redis:6379"),
		LimitedSquadUUID:   strings.TrimSpace(os.Getenv("TRAFFIC_LIMITED_SQUAD_UUID")),
		UnlimitedSquadUUID: strings.TrimSpace(os.Getenv("TRAFFIC_UNLIMITED_SQUAD_UUID")),
		WebhookAddr:        envDefault("TRAFFIC_WEBHOOK_ADDR", ":8081"),
		WebhookSecret:      strings.TrimSpace(os.Getenv("REMNAWAVE_WEBHOOK_SECRET")),
		ReconcileInterval:  time.Duration(intervalSec) * time.Second,
		LogLevel:           strings.ToLower(envDefault("LOG_LEVEL", "info")),
		LogFormat:          strings.ToLower(envDefault("LOG_FORMAT", "text")),
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) Validate() error {
	if c.RemnawaveAPIURL == "" {
		return fmt.Errorf("REMNAWAVE_API_URL is required")
	}
	if err := validateHTTPURL(c.RemnawaveAPIURL); err != nil {
		return fmt.Errorf("REMNAWAVE_API_URL: %w", err)
	}
	if c.RemnawaveAPIToken == "" {
		return fmt.Errorf("REMNAWAVE_API_TOKEN is required")
	}
	if c.RedisURL == "" {
		return fmt.Errorf("REDIS_URL is required")
	}
	if c.LimitedSquadUUID == "" {
		return fmt.Errorf("TRAFFIC_LIMITED_SQUAD_UUID is required")
	}
	if c.UnlimitedSquadUUID == "" {
		return fmt.Errorf("TRAFFIC_UNLIMITED_SQUAD_UUID is required")
	}
	if c.LimitedSquadUUID == c.UnlimitedSquadUUID {
		return fmt.Errorf("TRAFFIC_LIMITED_SQUAD_UUID and TRAFFIC_UNLIMITED_SQUAD_UUID must differ")
	}
	if c.WebhookSecret == "" {
		return fmt.Errorf("REMNAWAVE_WEBHOOK_SECRET is required")
	}
	if len(c.WebhookSecret) < 32 {
		return fmt.Errorf("REMNAWAVE_WEBHOOK_SECRET must be at least 32 characters")
	}
	if _, _, err := net.SplitHostPort(c.WebhookAddr); err != nil {
		return fmt.Errorf("TRAFFIC_WEBHOOK_ADDR must be host:port (for example :8081): %w", err)
	}
	if c.ReconcileInterval <= 0 {
		return fmt.Errorf("TRAFFIC_RECONCILE_INTERVAL must be > 0")
	}
	switch c.LogLevel {
	case "trace", "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("LOG_LEVEL must be trace, debug, info, warn or error")
	}
	switch c.LogFormat {
	case "text", "json":
	default:
		return fmt.Errorf("LOG_FORMAT must be text or json")
	}
	return nil
}

func validateHTTPURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("expected http:// or https://")
	}
	if u.Host == "" {
		return fmt.Errorf("host is missing")
	}
	return nil
}

func envDefault(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) (int, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer: %w", key, err)
	}
	return n, nil
}
