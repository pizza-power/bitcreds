package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/user/bitcreds/internal/bitbucket"
	"github.com/user/bitcreds/internal/config"
	"github.com/user/bitcreds/internal/db"
	"github.com/user/bitcreds/internal/patterns"
	"github.com/user/bitcreds/internal/scanner"
)

func main() {
	config.LoadEnv(".env")

	baseURL := flag.String("base-url", envOrDefault("BITBUCKET_URL", ""), "Bitbucket Server base URL")
	token := flag.String("token", envOrDefault("BITBUCKET_TOKEN", ""), "Bitbucket personal access token")
	dbPath := flag.String("db", "bitcreds.db", "Path to SQLite database file")
	timeout := flag.Duration("timeout", 0, "Maximum scan duration (e.g. 1h, 30m). 0 means no limit")
	rateLimit := flag.Float64("rate-limit", 10, "Maximum requests per second")
	patternsFile := flag.String("patterns", "", "Path to additional patterns YAML file")
	history := flag.Bool("history", false, "Enable history scanning (clone repos and scan git log)")
	historyDepth := flag.String("history-depth", "30d", "How far back to scan: Nd for days, Nc for commits")
	concurrency := flag.Int("concurrency", 5, "Number of parallel workers")
	cloneDir := flag.String("clone-dir", "", "Directory for cloned repos (history mode only)")

	flag.Parse()

	if *baseURL == "" {
		log.Fatal("--base-url or BITBUCKET_URL is required")
	}
	if *token == "" {
		log.Fatal("--token or BITBUCKET_TOKEN is required")
	}

	database, err := db.Open(*dbPath)
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}
	defer database.Close()

	pats, err := patterns.Load(*patternsFile)
	if err != nil {
		log.Fatalf("Failed to load patterns: %v", err)
	}
	log.Printf("Loaded %d patterns", len(pats))

	client := bitbucket.NewClient(*baseURL, *token, *rateLimit)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if *timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, *timeout)
		defer cancel()
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("Received shutdown signal, finishing in-flight work...")
		cancel()
	}()

	scanID := uuid.New().String()
	log.Printf("Starting scan %s", scanID)
	log.Printf("Target: %s", *baseURL)

	var result scanner.Result

	if *history {
		depthDays, depthCommits := parseHistoryDepth(*historyDepth)
		hs := scanner.NewHistoryScanner(client, database, pats, scanner.HistoryConfig{
			Concurrency:  *concurrency,
			DepthDays:    depthDays,
			DepthCommits: depthCommits,
			CloneDir:     *cloneDir,
		})
		result = hs.Run(ctx, scanID)
	} else {
		s := scanner.New(client, database, pats, scanner.Config{
			Concurrency: *concurrency,
			BaseURL:     *baseURL,
		})
		result = s.Run(ctx, scanID)
	}

	if result.Error != nil {
		log.Fatalf("Scan failed: %v", result.Error)
	}

	fmt.Println()
	fmt.Printf("Scan complete: %s\n", result.Status)
	fmt.Printf("  Duration:      %s\n", result.Duration.Round(time.Second))
	fmt.Printf("  Repos scanned: %d\n", result.ReposScanned)
	fmt.Printf("  Findings:      %d\n", result.FindingsCount)
	fmt.Printf("  Scan ID:       %s\n", scanID)
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func parseHistoryDepth(s string) (days, commits int) {
	s = strings.TrimSpace(s)
	if strings.HasSuffix(s, "d") {
		n, _ := strconv.Atoi(strings.TrimSuffix(s, "d"))
		return n, 0
	}
	if strings.HasSuffix(s, "c") {
		n, _ := strconv.Atoi(strings.TrimSuffix(s, "c"))
		return 0, n
	}
	n, _ := strconv.Atoi(s)
	return n, 0
}
