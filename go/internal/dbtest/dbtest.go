package dbtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"testing"

	"github.com/ecakeman/cakerdesk/internal/migrate"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
)

type Env struct {
	Name     string
	App      *pgxpool.Pool
	Migrate  *pgxpool.Pool
	AdminURL string
}

// 每个测试一个库，迁移和权限互不影响。
// 必须用超级用户建库，再用 cd_migrate 跑迁移。超级用户建表的话，默认授权不会给到 cd_app。
func New(t *testing.T) *Env {
	t.Helper()
	adminURL := os.Getenv("CD_TEST_DATABASE_URL")
	if adminURL == "" {
		t.Fatal("CD_TEST_DATABASE_URL 未设置")
	}
	ctx := context.Background()
	name := "cd_test_" + randHex(8)
	admin, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name+" OWNER cd_migrate"); err != nil {
		admin.Close(ctx)
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "GRANT CONNECT ON DATABASE "+name+" TO cd_app"); err != nil {
		admin.Close(ctx)
		t.Fatal(err)
	}
	admin.Close(ctx)

	migURL := rewriteURL(adminURL, "cd_migrate", "cd_migrate", name)
	appURL := rewriteURL(adminURL, "cd_app", "cd_app", name)
	migPool, err := pgxpool.New(ctx, migURL)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB := stdlib.OpenDBFromPool(migPool)
	if err := migrate.Up(sqlDB); err != nil {
		migPool.Close()
		t.Fatal(err)
	}
	if err := sqlDB.Close(); err != nil {
		migPool.Close()
		t.Fatal(err)
	}
	appPool, err := pgxpool.New(ctx, appURL)
	if err != nil {
		migPool.Close()
		t.Fatal(err)
	}
	env := &Env{Name: name, App: appPool, Migrate: migPool, AdminURL: adminURL}
	t.Cleanup(func() { env.Close(t) })
	return env
}

func (e *Env) Close(t *testing.T) {
	t.Helper()
	e.App.Close()
	e.Migrate.Close()
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, e.AdminURL)
	if err != nil {
		t.Log(err)
		return
	}
	defer admin.Close(ctx)
	// 有连接时 DROP DATABASE 会失败，先踢掉再删。
	_, _ = admin.Exec(ctx, "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()", e.Name)
	_, _ = admin.Exec(ctx, "DROP DATABASE IF EXISTS "+e.Name)
}

func rewriteURL(raw, user, pass, dbname string) string {
	u, err := url.Parse(raw)
	if err != nil {
		panic(err)
	}
	u.User = url.UserPassword(user, pass)
	u.Path = "/" + dbname
	return u.String()
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (e *Env) AppURL() string {
	return rewriteURL(e.AdminURL, "cd_app", "cd_app", e.Name)
}

func (e *Env) MigrateURL() string {
	return rewriteURL(e.AdminURL, "cd_migrate", "cd_migrate", e.Name)
}
