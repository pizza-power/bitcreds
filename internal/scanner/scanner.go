package scanner

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/user/bitcreds/internal/bitbucket"
	"github.com/user/bitcreds/internal/db"
	"github.com/user/bitcreds/internal/models"
	"github.com/user/bitcreds/internal/patterns"
)

type Config struct {
	Concurrency int
	BaseURL     string
}

type Scanner struct {
	client      *bitbucket.Client
	db          *db.DB
	patterns    []patterns.CompiledPattern
	concurrency int
	baseURL     string
}

func New(client *bitbucket.Client, database *db.DB, pats []patterns.CompiledPattern, cfg Config) *Scanner {
	conc := cfg.Concurrency
	if conc <= 0 {
		conc = 5
	}
	return &Scanner{
		client:      client,
		db:          database,
		patterns:    pats,
		concurrency: conc,
		baseURL:     cfg.BaseURL,
	}
}

type Result struct {
	FindingsCount int
	ReposScanned  int
	Duration      time.Duration
	Status        string
	Error         error
}

// Run executes a full scan using the Bitbucket code search API.
func (s *Scanner) Run(ctx context.Context, scanID string) Result {
	start := time.Now()

	scan := &models.Scan{
		ID:        scanID,
		StartedAt: start,
		Status:    "running",
	}
	if err := s.db.InsertScan(scan); err != nil {
		return Result{Error: fmt.Errorf("insert scan: %w", err), Status: "error"}
	}

	queryMap := patterns.ToSearchQueries(s.patterns)

	var findingsCount int64
	var reposScanned sync.Map

	type searchJob struct {
		query    string
		patterns []patterns.CompiledPattern
	}

	jobs := make(chan searchJob, len(queryMap))
	for q, pats := range queryMap {
		jobs <- searchJob{query: q, patterns: pats}
	}
	close(jobs)

	var wg sync.WaitGroup
	for i := 0; i < s.concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobs {
				if ctx.Err() != nil {
					return
				}
				s.processSearchQuery(ctx, scanID, job.query, job.patterns, &findingsCount, &reposScanned)
			}
		}()
	}

	wg.Wait()

	duration := time.Since(start)
	status := "completed"
	if ctx.Err() != nil {
		status = "timeout"
	}

	repos := 0
	reposScanned.Range(func(_, _ interface{}) bool {
		repos++
		return true
	})

	finishedAt := time.Now()
	scan.FinishedAt = &finishedAt
	scan.Status = status
	scan.FindingsCount = int(atomic.LoadInt64(&findingsCount))
	scan.ReposScanned = repos
	scan.DurationSeconds = int(duration.Seconds())
	s.db.UpdateScan(scan)

	return Result{
		FindingsCount: int(findingsCount),
		ReposScanned:  repos,
		Duration:      duration,
		Status:        status,
	}
}

func (s *Scanner) processSearchQuery(ctx context.Context, scanID, query string, pats []patterns.CompiledPattern, findingsCount *int64, reposScanned *sync.Map) {
	log.Printf("[scan] Searching for: %s", query)

	results, err := s.client.SearchCode(ctx, query)
	if err != nil {
		log.Printf("[scan] Error searching %q: %v", query, err)
		return
	}

	for _, result := range results {
		if ctx.Err() != nil {
			return
		}

		repoKey := result.File.Project + "/" + result.File.Repo
		reposScanned.Store(repoKey, true)

		for _, hitCtx := range result.HitContexts {
			for _, line := range hitCtx.Lines {
				for _, pat := range pats {
					if pat.Re.MatchString(line.Text) {
						matchText := pat.Re.FindString(line.Text)
						if len(matchText) > 500 {
							matchText = matchText[:500]
						}
						// Mask the middle of credentials for safety
						matchText = maskCredential(matchText)

						finding := &models.Finding{
							ScanID:      scanID,
							ProjectKey:  result.File.Project,
							RepoSlug:    result.File.Repo,
							FilePath:    result.File.Path,
							LineNumber:  line.Line,
							MatchText:   matchText,
							PatternName: pat.Name,
							PatternType: pat.Type,
							Status:      "open",
							FoundAt:     time.Now(),
							UpdatedAt:   time.Now(),
						}

						if _, err := s.db.InsertFinding(finding); err != nil {
							log.Printf("[scan] Error inserting finding: %v", err)
						} else {
							atomic.AddInt64(findingsCount, 1)
						}
						break
					}
				}
			}
		}
	}
}

func maskCredential(s string) string {
	if len(s) <= 8 {
		return s
	}
	visible := 4
	if len(s) > 40 {
		visible = 8
	}
	masked := s[:visible] + strings.Repeat("*", len(s)-visible*2)
	if visible < len(s) {
		masked += s[len(s)-visible:]
	}
	return masked
}
