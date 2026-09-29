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

	"github.com/ecakeman/cakerdesk/internal/api"
	"github.com/ecakeman/cakerdesk/internal/config"
	"github.com/ecakeman/cakerdesk/internal/httpx"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
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
	if err := runAPI(); err != nil {
		slog.Error("exit", "err", err.Error())
		os.Exit(1)
	}
}

func runAPI() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	srv := &http.Server{
		Addr:    cfg.PublicAddr,
		Handler: httpx.LogRequests(api.NewMux()),
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
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
	case <-ctx.Done():
	}
	return srv.Shutdown(context.Background())
}
