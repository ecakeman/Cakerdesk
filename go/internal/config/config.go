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

// 缺数据库 URL 或 API Key 直接启动失败。空着等到第一条请求才爆，分不清是配置还是库。
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

// 空字符串当没配。`.env` 里写成 `CD_PUBLIC_ADDR=` 时不能当成合法地址。
func envDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
