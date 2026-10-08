package main

import (
	"context"
	"database/sql"
	"log"
	"os"
	"path/filepath"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"cakerdesk/internal/cli"
	"cakerdesk/internal/db"
	"cakerdesk/internal/pythonclient"
	"cakerdesk/internal/server"
)

func main() {
	base := env("CAKERDESK_URL", "http://127.0.0.1:8080")
	opt := cli.Options{BaseURL: base, Out: os.Stdout, In: os.Stdin}
	if len(os.Args) == 1 {
		if err := cli.Shell(opt); err != nil {
			log.Fatal(err)
		}
		return
	}
	if os.Args[1] == "serve" {
		if err := serve(); err != nil {
			log.Fatal(err)
		}
		return
	}
	if err := cli.Execute(os.Args[1:], opt); err != nil {
		log.Fatal(err)
	}
}

func serve() error {
	url := os.Getenv("CAKERDESK_DATABASE_URL")
	if url == "" {
		return errString("需要 CAKERDESK_DATABASE_URL")
	}
	sqlDB, err := sql.Open("pgx", url)
	if err != nil {
		return err
	}
	defer sqlDB.Close()
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	if err := goose.Up(sqlDB, migrationsDir()); err != nil {
		return err
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		return err
	}
	defer pool.Close()
	srv := &server.Server{
		Pool:      pool,
		Q:         db.New(pool),
		Python:    pythonclient.Client{BaseURL: env("CAKERDESK_PYTHON_URL", "http://127.0.0.1:8090")},
		Workspace: env("CAKERDESK_WORKSPACE", filepath.Join("..", "workspace")),
	}
	addr := env("CAKERDESK_LISTEN", ":8080")
	log.Printf("cakerdesk go listening on %s", addr)
	return srv.Router().Run(addr)
}

func migrationsDir() string {
	if dir := os.Getenv("CAKERDESK_MIGRATIONS"); dir != "" {
		return dir
	}
	for _, dir := range []string{"migrations", filepath.Join("go", "migrations")} {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir
		}
	}
	return "migrations"
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

type errString string

func (e errString) Error() string { return string(e) }
