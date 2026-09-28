package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"cakerdesk/internal/app"
	"cakerdesk/internal/platform/config"
	"cakerdesk/internal/platform/migrate"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if len(os.Args) > 1 && os.Args[1][0] != '-' {
		switch os.Args[1] {
		case "migrate":
			if len(os.Args) != 3 || os.Args[2] != "up" {
				fmt.Fprintln(os.Stderr, "usage: cakerdesk migrate up")
				os.Exit(2)
			}
			cfg, err := config.Load()
			if err != nil {
				fatal(err)
			}
			if err := migrate.Up(cfg.DatabaseURL); err != nil {
				fatal(err)
			}
			return
		case "admin":
			if len(os.Args) != 5 || os.Args[2] != "bootstrap" {
				fmt.Fprintln(os.Stderr, "usage: cakerdesk admin bootstrap <email> <tenant>")
				os.Exit(2)
			}
			if err := bootstrap(os.Args[3], os.Args[4]); err != nil {
				fatal(err)
			}
			return
		default:
			fmt.Fprintln(os.Stderr, "unknown command")
			os.Exit(2)
		}
	}
	role := "api"
	for _, arg := range os.Args[1:] {
		if len(arg) > 6 && arg[:6] == "-role=" {
			role = arg[6:]
		}
	}
	if role != "api" {
		fmt.Fprintf(os.Stderr, "unknown role %s\n", role)
		os.Exit(2)
	}
	if err := serve(); err != nil {
		fatal(err)
	}
}

func bootstrap(email, tenant string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	pool, err := pgxpool.New(context.Background(), cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	srv := app.New(cfg, pool)
	return srv.Bootstrap(context.Background(), email, tenant, os.Getenv("CAKERDESK_BOOTSTRAP_PASSWORD"))
}

func serve() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := cfg.ValidateAPI(); err != nil {
		return err
	}
	pool, err := pgxpool.New(context.Background(), cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	srv := app.New(cfg, pool)
	public := &http.Server{Addr: cfg.HTTPAddr, Handler: srv.Public}
	internal := &http.Server{Addr: cfg.InternalAddr, Handler: srv.Internal}
	errCh := make(chan error, 2)
	go func() { errCh <- public.ListenAndServe() }()
	go func() { errCh <- internal.ListenAndServe() }()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-sig:
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = public.Shutdown(ctx)
	_ = internal.Shutdown(ctx)
	return nil
}

func fatal(err error) {
	slog.Error("exit", "err", err.Error())
	os.Exit(1)
}
