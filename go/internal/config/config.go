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

// Load 读环境变量。A2 起数据库 URL 和 API Key 缺了就不能启动。
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

// envDefault 空字符串当没配。
func envDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
