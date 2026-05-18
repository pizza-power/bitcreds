package main

import (
	"flag"
	"log"
	"net/http"
	"os"

	"github.com/user/bitcreds/internal/config"
	"github.com/user/bitcreds/internal/db"
	"github.com/user/bitcreds/internal/patterns"
	"github.com/user/bitcreds/internal/web/handlers"
	"github.com/user/bitcreds/internal/web/middleware"
)

func main() {
	config.LoadEnv(".env")

	dbPath := flag.String("db", "bitcreds.db", "Path to SQLite database file")
	listen := flag.String("listen", ":8080", "Listen address")
	username := flag.String("username", envOrDefault("BITCREDS_USERNAME", "admin"), "Basic auth username")
	password := flag.String("password", envOrDefault("BITCREDS_PASSWORD", "changeme"), "Basic auth password")
	patternsFile := flag.String("patterns", "", "Path to patterns YAML file (for display purposes)")

	flag.Parse()

	database, err := db.Open(*dbPath)
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}
	defer database.Close()

	pats, err := patterns.Load(*patternsFile)
	if err != nil {
		log.Fatalf("Failed to load patterns: %v", err)
	}

	h := handlers.New(database, pats)
	mux := http.NewServeMux()

	mux.HandleFunc("/", h.Dashboard)
	mux.HandleFunc("/findings", h.FindingsList)
	mux.HandleFunc("/findings/", h.FindingDetail)
	mux.HandleFunc("/findings/update-status", h.UpdateStatus)
	mux.HandleFunc("/findings/bulk-action", h.BulkAction)
	mux.HandleFunc("/findings/delete", h.DeleteFinding)
	mux.HandleFunc("/notes/add", h.AddNote)
	mux.HandleFunc("/notes/delete", h.DeleteNote)
	mux.HandleFunc("/scans", h.ScansList)
	mux.HandleFunc("/patterns", h.PatternsList)
	mux.HandleFunc("/static/", h.Static)

	auth := middleware.BasicAuth(*username, *password)
	handler := auth(mux)

	log.Printf("bitcreds-server listening on %s", *listen)
	if err := http.ListenAndServe(*listen, handler); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
