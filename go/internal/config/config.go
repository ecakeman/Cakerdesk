package config

import (
	"fmt"
	"os"
)

type Config struct {
	PublicAddr         string
	DatabaseURL        string
	MigrateDatabaseURL string
	APIKey             string
}

func Load() (Config, error) {
	cfg := Config{
		PublicAddr:         envDefault("CD_PUBLIC_ADDR", ":7310"),
		DatabaseURL:        os.Getenv("CD_DATABASE_URL"),
		MigrateDatabaseURL: os.Getenv("CD_MIGRATE_DATABASE_URL"),
		APIKey:             os.Getenv("CD_API_KEY"),
	}
	if cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("缺环境变量 CD_DATABASE_URL")
	}
	if cfg.MigrateDatabaseURL == "" {
		return Config{}, fmt.Errorf("缺环境变量 CD_MIGRATE_DATABASE_URL")
	}
	if cfg.APIKey == "" {
		return Config{}, fmt.Errorf("缺环境变量 CD_API_KEY")
	}
	return cfg, nil
}

func envDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
