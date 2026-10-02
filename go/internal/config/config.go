package config

import (
	"fmt"
	"os"
	"strconv"
)

type Config struct {
	PublicAddr         string
	DatabaseURL        string
	MigrateDatabaseURL string
	APIKey             string
	WorkspacesDir      string
	InternalAddr       string
	InternalToken      string
	LeaseSeconds       int
	HeartbeatSeconds   int
	ClaimMaxWaitMS     int
	RedisURL           string
}

// 缺数据库 URL、API Key 或内部钥匙直接启动失败。空着等到第一条请求才爆，分不清是配置还是库。
func Load() (Config, error) {
	lease, err := envInt("CD_LEASE_SECONDS", 30, false)
	if err != nil {
		return Config{}, err
	}
	heartbeat, err := envInt("CD_HEARTBEAT_SECONDS", 10, false)
	if err != nil {
		return Config{}, err
	}
	claimWait, err := envInt("CD_CLAIM_MAX_WAIT_MS", 20000, true)
	if err != nil {
		return Config{}, err
	}
	cfg := Config{
		PublicAddr:         envDefault("CD_PUBLIC_ADDR", ":7310"),
		DatabaseURL:        os.Getenv("CD_DATABASE_URL"),
		MigrateDatabaseURL: os.Getenv("CD_MIGRATE_DATABASE_URL"),
		APIKey:             os.Getenv("CD_API_KEY"),
		WorkspacesDir:      envDefault("CD_WORKSPACES_DIR", "./var/workspaces"),
		InternalAddr:       envDefault("CD_INTERNAL_ADDR", "127.0.0.1:7312"),
		InternalToken:      os.Getenv("CD_INTERNAL_TOKEN"),
		LeaseSeconds:       lease,
		HeartbeatSeconds:   heartbeat,
		ClaimMaxWaitMS:     claimWait,
		RedisURL:           envDefault("CD_REDIS_URL", "redis://127.0.0.1:7341/0"),
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
	if cfg.InternalToken == "" {
		return Config{}, fmt.Errorf("缺环境变量 CD_INTERNAL_TOKEN")
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

func envInt(key string, fallback int, allowZero bool) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 || (n == 0 && !allowZero) {
		return 0, fmt.Errorf("环境变量 %s 不合法", key)
	}
	return n, nil
}
