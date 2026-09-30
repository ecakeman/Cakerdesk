package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ecakeman/cakerdesk/internal/api"
	"github.com/ecakeman/cakerdesk/internal/config"
	"github.com/ecakeman/cakerdesk/internal/migrate"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
)

func main() { // 入口：migrate 子命令或 -role=api。
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	// 子命令 migrate 与 -role 进程分开。
	if len(os.Args) >= 2 && os.Args[1] == "migrate" {
		if err := runMigrate(os.Args[2:]); err != nil {
			slog.Error("migrate", "err", err.Error())
			os.Exit(1)
		}
		return
	}
	role := flag.String("role", "api", "api | reaper | sandboxd")
	flag.Parse()
	switch *role {
	case "api":
	case "reaper", "sandboxd":
		fmt.Fprintf(os.Stderr, "unknown role %s\n", *role)
		os.Exit(2)
	default:
		fmt.Fprintf(os.Stderr, "unknown role %s\n", *role)
		os.Exit(2)
	}
	if err := runAPI(); err != nil {
		slog.Error("exit", "err", err.Error())
		os.Exit(1)
	}
}

// runMigrate 用 cd_migrate 连接串执行 goose。
func runMigrate(args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, cfg.MigrateDatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	return migrate.Cmd(stdlib.OpenDBFromPool(pool), args)
}

// runAPI 连 cd_app 库，对外提供 Public 引擎，收到信号后 Shutdown。
func runAPI() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return err
	}
	srv := &http.Server{
		Addr:    cfg.PublicAddr,
		Handler: api.New(pool, cfg.APIKey).Public,
	}
	// 阻塞直到进程被杀或 Listen 自己退出，再给 5 秒收尾。
	sigCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.ListenAndServe()
	}()
	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-sigCtx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
