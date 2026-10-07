package main

import (
	"log"
	"net/http"
	"os"
	"path/filepath"

	"cakerdesk/internal/server"
	"cakerdesk/internal/store"
)

func main() {
	dbPath := env("CAKERDESK_DB", "cakerdesk.db")
	st, err := store.Open(dbPath)
	if err != nil {
		log.Fatal(err)
	}
	web := env("CAKERDESK_WEB", filepath.Join("..", "web"))
	srv := &server.Server{
		Store:     st,
		PythonURL: env("CAKERDESK_PYTHON_URL", "http://127.0.0.1:8090"),
		Workspace: env("CAKERDESK_WORKSPACE", filepath.Join("..", "workspace")),
		WebDir:    web,
	}
	addr := env("CAKERDESK_HTTP", ":8080")
	log.Printf("cakerdesk go listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, srv.Handler()))
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
