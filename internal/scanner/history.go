package scanner

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/user/bitcreds/internal/bitbucket"
	"github.com/user/bitcreds/internal/db"
	"github.com/user/bitcreds/internal/models"
	"github.com/user/bitcreds/internal/patterns"
)

type HistoryConfig struct {
	Concurrency int
	DepthDays   int
	DepthCommits int
	CloneDir    string
}

type HistoryScanner struct {
	client   *bitbucket.Client
	db       *db.DB
	patterns []patterns.CompiledPattern
	config   HistoryConfig
}

func NewHistoryScanner(client *bitbucket.Client, database *db.DB, pats []patterns.CompiledPattern, cfg HistoryConfig) *HistoryScanner {
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 3
	}
	if cfg.CloneDir == "" {
		cfg.CloneDir = filepath.Join(os.TempDir(), "bitcreds-clones")
	}
	return &HistoryScanner{
		client:   client,
		db:       database,
		patterns: pats,
		config:   cfg,
	}
}

// Run clones repos and scans git history for credentials.
func (hs *HistoryScanner) Run(ctx context.Context, scanID string) Result {
	start := time.Now()

	scan := &models.Scan{
		ID:        scanID,
		StartedAt: start,
		Status:    "running",
	}
	if err := hs.db.InsertScan(scan); err != nil {
		return Result{Error: fmt.Errorf("insert scan: %w", err), Status: "error"}
	}

	repos, err := hs.client.ListAllRepos(ctx)
	if err != nil {
		log.Printf("[history] Error listing repos: %v", err)
		return Result{Error: err, Status: "error"}
	}

	os.MkdirAll(hs.config.CloneDir, 0755)

	var findingsCount int64
	var reposScanned int64

	type repoJob struct {
		repo bitbucket.Repository
	}

	jobs := make(chan repoJob, len(repos))
	for _, r := range repos {
		jobs <- repoJob{repo: r}
	}
	close(jobs)

	var wg sync.WaitGroup
	for i := 0; i < hs.config.Concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobs {
				if ctx.Err() != nil {
					return
				}
				count := hs.scanRepo(ctx, scanID, job.repo)
				atomic.AddInt64(&findingsCount, int64(count))
				atomic.AddInt64(&reposScanned, 1)
			}
		}()
	}

	wg.Wait()

	duration := time.Since(start)
	status := "completed"
	if ctx.Err() != nil {
		status = "timeout"
	}

	finishedAt := time.Now()
	scan.FinishedAt = &finishedAt
	scan.Status = status
	scan.FindingsCount = int(findingsCount)
	scan.ReposScanned = int(reposScanned)
	scan.DurationSeconds = int(duration.Seconds())
	hs.db.UpdateScan(scan)

	return Result{
		FindingsCount: int(findingsCount),
		ReposScanned:  int(reposScanned),
		Duration:      duration,
		Status:        status,
	}
}

func (hs *HistoryScanner) scanRepo(ctx context.Context, scanID string, repo bitbucket.Repository) int {
	repoDir := filepath.Join(hs.config.CloneDir, repo.Project.Key, repo.Slug)
	cloneURL := hs.client.GetRepoCloneURL(repo.Project.Key, repo.Slug)

	if err := hs.cloneOrFetch(ctx, repoDir, cloneURL); err != nil {
		log.Printf("[history] Error cloning %s/%s: %v", repo.Project.Key, repo.Slug, err)
		return 0
	}

	findings := 0
	logArgs := []string{"log", "--all", "-p", "--no-merges"}

	if hs.config.DepthDays > 0 {
		since := time.Now().AddDate(0, 0, -hs.config.DepthDays).Format("2006-01-02")
		logArgs = append(logArgs, "--since="+since)
	} else if hs.config.DepthCommits > 0 {
		logArgs = append(logArgs, "-n", strconv.Itoa(hs.config.DepthCommits))
	}

	cmd := exec.CommandContext(ctx, "git", logArgs...)
	cmd.Dir = repoDir

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		log.Printf("[history] Error setting up pipe for %s/%s: %v", repo.Project.Key, repo.Slug, err)
		return 0
	}

	if err := cmd.Start(); err != nil {
		log.Printf("[history] Error starting git log for %s/%s: %v", repo.Project.Key, repo.Slug, err)
		return 0
	}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	var currentCommit string
	var currentAuthor string
	var currentFile string
	var lineNum int

	for scanner.Scan() {
		if ctx.Err() != nil {
			break
		}
		line := scanner.Text()

		if strings.HasPrefix(line, "commit ") {
			currentCommit = strings.TrimPrefix(line, "commit ")
			if len(currentCommit) > 40 {
				currentCommit = currentCommit[:40]
			}
		} else if strings.HasPrefix(line, "Author: ") {
			currentAuthor = strings.TrimPrefix(line, "Author: ")
		} else if strings.HasPrefix(line, "+++ b/") {
			currentFile = strings.TrimPrefix(line, "+++ b/")
			lineNum = 0
		} else if strings.HasPrefix(line, "@@ ") {
			parts := strings.Split(line, "+")
			if len(parts) > 1 {
				numStr := strings.Split(parts[1], ",")[0]
				if n, err := strconv.Atoi(numStr); err == nil {
					lineNum = n
				}
			}
		} else if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			addedLine := line[1:]
			lineNum++

			for _, pat := range hs.patterns {
				if pat.Re.MatchString(addedLine) {
					matchText := pat.Re.FindString(addedLine)
					if len(matchText) > 500 {
						matchText = matchText[:500]
					}
					matchText = maskCredential(matchText)

					finding := &models.Finding{
						ScanID:      scanID,
						ProjectKey:  repo.Project.Key,
						RepoSlug:    repo.Slug,
						FilePath:    currentFile,
						LineNumber:  lineNum,
						MatchText:   matchText,
						PatternName: pat.Name,
						PatternType: pat.Type,
						CommitHash:  currentCommit,
						Author:      currentAuthor,
						Status:      "open",
						FoundAt:     time.Now(),
						UpdatedAt:   time.Now(),
					}

					if _, err := hs.db.InsertFinding(finding); err == nil {
						findings++
					}
					break
				}
			}
		} else if !strings.HasPrefix(line, "-") {
			lineNum++
		}
	}

	cmd.Wait()
	return findings
}

func (hs *HistoryScanner) cloneOrFetch(ctx context.Context, dir, cloneURL string) error {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		cmd := exec.CommandContext(ctx, "git", "fetch", "--all")
		cmd.Dir = dir
		return cmd.Run()
	}

	os.MkdirAll(filepath.Dir(dir), 0755)
	cmd := exec.CommandContext(ctx, "git", "clone", "--bare", cloneURL, dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("clone: %s: %w", string(out), err)
	}
	return nil
}
