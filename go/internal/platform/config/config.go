package config

import (
	"fmt"
	"os"
	"strings"
)

type Config struct {
	DatabaseURL   string
	HTTPAddr      string
	InternalAddr  string
	InternalToken string
	MasterKey     string
	Env           string
	LLMAliases    []string
	SecureCookie  bool
}

func Load() (Config, error) {
	cfg := Config{
		DatabaseURL:   os.Getenv("CAKERDESK_DATABASE_URL"),
		HTTPAddr:      envDefault("CAKERDESK_HTTP_ADDR", ":7310"),
		InternalAddr:  envDefault("CAKERDESK_INTERNAL_ADDR", ":7312"),
		InternalToken: os.Getenv("CAKERDESK_INTERNAL__TOKEN"),
		MasterKey:     os.Getenv("CAKERDESK_MASTER_KEY"),
		Env:           envDefault("CAKERDESK_ENV", "development"),
		SecureCookie:  envDefault("CAKERDESK_HTTP__SECURE_COOKIE", "true") != "false",
	}
	if raw := strings.TrimSpace(os.Getenv("CAKERDESK_LLM__ALIASES")); raw != "" {
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			if part != "" {
				cfg.LLMAliases = append(cfg.LLMAliases, part)
			}
		}
	}
	if cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("CAKERDESK_DATABASE_URL is required")
	}
	return cfg, nil
}

func (c Config) ValidateAPI() error {
	if c.InternalToken == "" {
		return fmt.Errorf("CAKERDESK_INTERNAL__TOKEN is required")
	}
	if c.MasterKey == "" {
		return fmt.Errorf("CAKERDESK_MASTER_KEY is required")
	}
	return nil
}

func envDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
