package models

import "time"

type Finding struct {
	ID          int64     `json:"id"`
	ScanID      string    `json:"scan_id"`
	ProjectKey  string    `json:"project_key"`
	RepoSlug    string    `json:"repo_slug"`
	FilePath    string    `json:"file_path"`
	LineNumber  int       `json:"line_number"`
	MatchText   string    `json:"match_text"`
	PatternName string    `json:"pattern_name"`
	PatternType string    `json:"pattern_type"`
	CommitHash  string    `json:"commit_hash,omitempty"`
	Author      string    `json:"author,omitempty"`
	Status      string    `json:"status"`
	Notes       string    `json:"notes"`
	FoundAt     time.Time `json:"found_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type Scan struct {
	ID              string     `json:"id"`
	StartedAt       time.Time  `json:"started_at"`
	FinishedAt      *time.Time `json:"finished_at,omitempty"`
	Status          string     `json:"status"`
	FindingsCount   int        `json:"findings_count"`
	ReposScanned    int        `json:"repos_scanned"`
	DurationSeconds int        `json:"duration_seconds"`
}

type Note struct {
	ID        int64     `json:"id"`
	FindingID int64     `json:"finding_id"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

type Pattern struct {
	Name        string `yaml:"name" json:"name"`
	Type        string `yaml:"type" json:"type"`
	Severity    string `yaml:"severity" json:"severity"`
	Regex       string `yaml:"regex" json:"regex"`
	Description string `yaml:"description" json:"description"`
}

type PatternFile struct {
	Patterns []Pattern `yaml:"patterns" json:"patterns"`
}
