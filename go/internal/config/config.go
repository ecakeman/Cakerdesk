package config

import "os"

// Config 只包含当前步骤已经引入的配置。后续步骤再往这里加字段。
type Config struct {
	PublicAddr string
}

func Load() (Config, error) {
	return Config{
		PublicAddr: envDefault("CD_PUBLIC_ADDR", ":7310"),
	}, nil
}

func envDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
