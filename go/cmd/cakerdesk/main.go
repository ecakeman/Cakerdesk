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

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	// migrate 不走 -role。flag 会把未知子命令当成参数，两个入口必须先分开。
	if len(os.Args) >= 2 && os.Args[1] == "migrate" {
		if err := runMigrate(os.Args[2:]); err != nil {
			slog.Error("migrate", "err", err.Error())
			os.Exit(1)
		}
		return
	}
	role := flag.String("role", "api", "api | reaper | sandboxd")
	flag.Parse()
	// reaper、sandboxd 名字合法，但这步还没实现。退出码 2，避免空进程看起来像在跑。
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

func runAPI() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx := context.Background()
	parsed, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return err
	}
	// 领取的等待在事务外面。这里把池子放到 20，避免多个内核同时长轮询时把默认的几条连接用完。
	parsed.MaxConns = 20
	pool, err := pgxpool.NewWithConfig(ctx, parsed)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return err
	}
	app := api.New(pool, api.Options{
		APIKey:           cfg.APIKey,
		WorkspacesDir:    cfg.WorkspacesDir,
		InternalToken:    cfg.InternalToken,
		LeaseSeconds:     cfg.LeaseSeconds,
		HeartbeatSeconds: cfg.HeartbeatSeconds,
		ClaimMaxWait:     time.Duration(cfg.ClaimMaxWaitMS) * time.Millisecond,
		RedisURL:         cfg.RedisURL,
	})
	pub := &http.Server{Addr: cfg.PublicAddr, Handler: app.Public}
	internal := &http.Server{Addr: cfg.InternalAddr, Handler: app.Internal}
	// Listen 放进 goroutine，主协程才能接到 SIGINT 再 Shutdown。
	// 5 秒到了还关不掉就返回错误，避免连接不放时进程一直挂着。
	sigCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	errCh := make(chan error, 2)
	go func() {
		errCh <- pub.ListenAndServe()
	}()
	go func() {
		errCh <- internal.ListenAndServe()
	}()
	var runErr error
	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			runErr = err
		}
	case <-sigCtx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	errPub := pub.Shutdown(shutdownCtx)
	errIn := internal.Shutdown(shutdownCtx)
	if runErr != nil {
		return runErr
	}
	return errors.Join(errPub, errIn)
}
