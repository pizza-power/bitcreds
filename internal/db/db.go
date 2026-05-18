package db

import (
	"database/sql"
	"fmt"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/user/bitcreds/internal/models"
)

type DB struct {
	conn *sql.DB
}

func Open(path string) (*DB, error) {
	conn, err := sql.Open("sqlite3", path+"?_journal_mode=WAL&_foreign_keys=on")
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if err := conn.Ping(); err != nil {
		return nil, fmt.Errorf("ping database: %w", err)
	}
	db := &DB{conn: conn}
	if err := db.migrate(); err != nil {
		return nil, fmt.Errorf("migrate database: %w", err)
	}
	return db, nil
}

func (db *DB) Close() error {
	return db.conn.Close()
}

func (db *DB) migrate() error {
	schema := `
	CREATE TABLE IF NOT EXISTS scans (
		id TEXT PRIMARY KEY,
		started_at DATETIME NOT NULL,
		finished_at DATETIME,
		status TEXT NOT NULL,
		findings_count INTEGER DEFAULT 0,
		repos_scanned INTEGER DEFAULT 0,
		duration_seconds INTEGER DEFAULT 0
	);

	CREATE TABLE IF NOT EXISTS findings (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		scan_id TEXT NOT NULL,
		project_key TEXT NOT NULL,
		repo_slug TEXT NOT NULL,
		file_path TEXT NOT NULL,
		line_number INTEGER DEFAULT 0,
		match_text TEXT NOT NULL,
		pattern_name TEXT NOT NULL,
		pattern_type TEXT NOT NULL,
		commit_hash TEXT DEFAULT '',
		author TEXT DEFAULT '',
		status TEXT NOT NULL DEFAULT 'open',
		notes TEXT DEFAULT '',
		found_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL
	);

	CREATE TABLE IF NOT EXISTS notes (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		finding_id INTEGER NOT NULL REFERENCES findings(id) ON DELETE CASCADE,
		content TEXT NOT NULL,
		created_at DATETIME NOT NULL
	);

	CREATE INDEX IF NOT EXISTS idx_findings_scan_id ON findings(scan_id);
	CREATE INDEX IF NOT EXISTS idx_findings_status ON findings(status);
	CREATE INDEX IF NOT EXISTS idx_findings_project ON findings(project_key);
	CREATE INDEX IF NOT EXISTS idx_findings_repo ON findings(repo_slug);
	CREATE INDEX IF NOT EXISTS idx_findings_pattern ON findings(pattern_name);
	CREATE INDEX IF NOT EXISTS idx_findings_dedup ON findings(project_key, repo_slug, file_path, line_number, pattern_name);
	CREATE INDEX IF NOT EXISTS idx_notes_finding ON notes(finding_id);
	`
	_, err := db.conn.Exec(schema)
	return err
}

// InsertScan creates a new scan record.
func (db *DB) InsertScan(scan *models.Scan) error {
	_, err := db.conn.Exec(
		`INSERT INTO scans (id, started_at, finished_at, status, findings_count, repos_scanned, duration_seconds)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		scan.ID, scan.StartedAt, scan.FinishedAt, scan.Status, scan.FindingsCount, scan.ReposScanned, scan.DurationSeconds,
	)
	return err
}

// UpdateScan updates an existing scan record.
func (db *DB) UpdateScan(scan *models.Scan) error {
	_, err := db.conn.Exec(
		`UPDATE scans SET finished_at = ?, status = ?, findings_count = ?, repos_scanned = ?, duration_seconds = ? WHERE id = ?`,
		scan.FinishedAt, scan.Status, scan.FindingsCount, scan.ReposScanned, scan.DurationSeconds, scan.ID,
	)
	return err
}

// GetScan retrieves a scan by ID.
func (db *DB) GetScan(id string) (*models.Scan, error) {
	s := &models.Scan{}
	err := db.conn.QueryRow(
		`SELECT id, started_at, finished_at, status, findings_count, repos_scanned, duration_seconds FROM scans WHERE id = ?`, id,
	).Scan(&s.ID, &s.StartedAt, &s.FinishedAt, &s.Status, &s.FindingsCount, &s.ReposScanned, &s.DurationSeconds)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// ListScans returns all scans ordered by most recent first.
func (db *DB) ListScans(limit, offset int) ([]models.Scan, error) {
	rows, err := db.conn.Query(
		`SELECT id, started_at, finished_at, status, findings_count, repos_scanned, duration_seconds
		 FROM scans ORDER BY started_at DESC LIMIT ? OFFSET ?`, limit, offset,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var scans []models.Scan
	for rows.Next() {
		var s models.Scan
		if err := rows.Scan(&s.ID, &s.StartedAt, &s.FinishedAt, &s.Status, &s.FindingsCount, &s.ReposScanned, &s.DurationSeconds); err != nil {
			return nil, err
		}
		scans = append(scans, s)
	}
	return scans, rows.Err()
}

// InsertFinding creates a new finding, returning its ID. Deduplicates by project+repo+file+line+pattern.
func (db *DB) InsertFinding(f *models.Finding) (int64, error) {
	var existingID int64
	err := db.conn.QueryRow(
		`SELECT id FROM findings WHERE project_key = ? AND repo_slug = ? AND file_path = ? AND line_number = ? AND pattern_name = ? AND status != 'resolved'`,
		f.ProjectKey, f.RepoSlug, f.FilePath, f.LineNumber, f.PatternName,
	).Scan(&existingID)
	if err == nil {
		return existingID, nil
	}

	res, err := db.conn.Exec(
		`INSERT INTO findings (scan_id, project_key, repo_slug, file_path, line_number, match_text, pattern_name, pattern_type, commit_hash, author, status, notes, found_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		f.ScanID, f.ProjectKey, f.RepoSlug, f.FilePath, f.LineNumber, f.MatchText,
		f.PatternName, f.PatternType, f.CommitHash, f.Author, f.Status, f.Notes, f.FoundAt, f.UpdatedAt,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// FindingFilter holds filter criteria for listing findings.
type FindingFilter struct {
	Status     string
	ProjectKey string
	RepoSlug   string
	PatternName string
	Limit      int
	Offset     int
}

// ListFindings returns findings with optional filtering.
func (db *DB) ListFindings(filter FindingFilter) ([]models.Finding, int, error) {
	where := "WHERE 1=1"
	args := []interface{}{}

	if filter.Status != "" {
		where += " AND status = ?"
		args = append(args, filter.Status)
	}
	if filter.ProjectKey != "" {
		where += " AND project_key = ?"
		args = append(args, filter.ProjectKey)
	}
	if filter.RepoSlug != "" {
		where += " AND repo_slug = ?"
		args = append(args, filter.RepoSlug)
	}
	if filter.PatternName != "" {
		where += " AND pattern_name = ?"
		args = append(args, filter.PatternName)
	}

	var total int
	countArgs := make([]interface{}, len(args))
	copy(countArgs, args)
	err := db.conn.QueryRow("SELECT COUNT(*) FROM findings "+where, countArgs...).Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	query := fmt.Sprintf(
		`SELECT id, scan_id, project_key, repo_slug, file_path, line_number, match_text, pattern_name, pattern_type, commit_hash, author, status, notes, found_at, updated_at
		 FROM findings %s ORDER BY found_at DESC LIMIT ? OFFSET ?`, where,
	)
	args = append(args, limit, filter.Offset)

	rows, err := db.conn.Query(query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var findings []models.Finding
	for rows.Next() {
		var f models.Finding
		if err := rows.Scan(&f.ID, &f.ScanID, &f.ProjectKey, &f.RepoSlug, &f.FilePath, &f.LineNumber, &f.MatchText, &f.PatternName, &f.PatternType, &f.CommitHash, &f.Author, &f.Status, &f.Notes, &f.FoundAt, &f.UpdatedAt); err != nil {
			return nil, 0, err
		}
		findings = append(findings, f)
	}
	return findings, total, rows.Err()
}

// GetFinding retrieves a single finding by ID.
func (db *DB) GetFinding(id int64) (*models.Finding, error) {
	f := &models.Finding{}
	err := db.conn.QueryRow(
		`SELECT id, scan_id, project_key, repo_slug, file_path, line_number, match_text, pattern_name, pattern_type, commit_hash, author, status, notes, found_at, updated_at
		 FROM findings WHERE id = ?`, id,
	).Scan(&f.ID, &f.ScanID, &f.ProjectKey, &f.RepoSlug, &f.FilePath, &f.LineNumber, &f.MatchText, &f.PatternName, &f.PatternType, &f.CommitHash, &f.Author, &f.Status, &f.Notes, &f.FoundAt, &f.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return f, nil
}

// UpdateFindingStatus updates the status of a finding.
func (db *DB) UpdateFindingStatus(id int64, status string) error {
	_, err := db.conn.Exec(
		`UPDATE findings SET status = ?, updated_at = ? WHERE id = ?`,
		status, time.Now(), id,
	)
	return err
}

// BulkUpdateFindingStatus updates multiple findings at once.
func (db *DB) BulkUpdateFindingStatus(ids []int64, status string) error {
	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`UPDATE findings SET status = ?, updated_at = ? WHERE id = ?`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	now := time.Now()
	for _, id := range ids {
		if _, err := stmt.Exec(status, now, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DeleteFinding removes a finding and its notes.
func (db *DB) DeleteFinding(id int64) error {
	_, err := db.conn.Exec(`DELETE FROM findings WHERE id = ?`, id)
	return err
}

// BulkDeleteFindings removes multiple findings.
func (db *DB) BulkDeleteFindings(ids []int64) error {
	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`DELETE FROM findings WHERE id = ?`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, id := range ids {
		if _, err := stmt.Exec(id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// AddNote adds a note to a finding.
func (db *DB) AddNote(findingID int64, content string) (int64, error) {
	res, err := db.conn.Exec(
		`INSERT INTO notes (finding_id, content, created_at) VALUES (?, ?, ?)`,
		findingID, content, time.Now(),
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// GetNotes retrieves all notes for a finding.
func (db *DB) GetNotes(findingID int64) ([]models.Note, error) {
	rows, err := db.conn.Query(
		`SELECT id, finding_id, content, created_at FROM notes WHERE finding_id = ? ORDER BY created_at DESC`, findingID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var notes []models.Note
	for rows.Next() {
		var n models.Note
		if err := rows.Scan(&n.ID, &n.FindingID, &n.Content, &n.CreatedAt); err != nil {
			return nil, err
		}
		notes = append(notes, n)
	}
	return notes, rows.Err()
}

// DeleteNote removes a single note.
func (db *DB) DeleteNote(id int64) error {
	_, err := db.conn.Exec(`DELETE FROM notes WHERE id = ?`, id)
	return err
}

// DashboardStats holds summary statistics for the dashboard.
type DashboardStats struct {
	TotalFindings      int
	OpenFindings       int
	ResolvedFindings   int
	FalsePositives     int
	BySeverity         map[string]int
	ByPatternType      map[string]int
	RecentScans        []models.Scan
}

// GetDashboardStats returns aggregate statistics.
func (db *DB) GetDashboardStats() (*DashboardStats, error) {
	stats := &DashboardStats{
		BySeverity:    make(map[string]int),
		ByPatternType: make(map[string]int),
	}

	db.conn.QueryRow(`SELECT COUNT(*) FROM findings`).Scan(&stats.TotalFindings)
	db.conn.QueryRow(`SELECT COUNT(*) FROM findings WHERE status = 'open'`).Scan(&stats.OpenFindings)
	db.conn.QueryRow(`SELECT COUNT(*) FROM findings WHERE status = 'resolved'`).Scan(&stats.ResolvedFindings)
	db.conn.QueryRow(`SELECT COUNT(*) FROM findings WHERE status = 'false_positive'`).Scan(&stats.FalsePositives)

	rows, err := db.conn.Query(`SELECT pattern_type, COUNT(*) FROM findings WHERE status = 'open' GROUP BY pattern_type`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var t string
			var c int
			rows.Scan(&t, &c)
			stats.ByPatternType[t] = c
		}
	}

	stats.RecentScans, _ = db.ListScans(5, 0)
	return stats, nil
}

// GetDistinctProjects returns all distinct project keys.
func (db *DB) GetDistinctProjects() ([]string, error) {
	rows, err := db.conn.Query(`SELECT DISTINCT project_key FROM findings ORDER BY project_key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var projects []string
	for rows.Next() {
		var p string
		rows.Scan(&p)
		projects = append(projects, p)
	}
	return projects, rows.Err()
}

// GetDistinctPatterns returns all distinct pattern names.
func (db *DB) GetDistinctPatterns() ([]string, error) {
	rows, err := db.conn.Query(`SELECT DISTINCT pattern_name FROM findings ORDER BY pattern_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var patterns []string
	for rows.Next() {
		var p string
		rows.Scan(&p)
		patterns = append(patterns, p)
	}
	return patterns, rows.Err()
}
