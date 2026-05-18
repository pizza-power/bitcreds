package patterns

import (
	"embed"
	"fmt"
	"os"
	"regexp"

	"gopkg.in/yaml.v3"

	"github.com/user/bitcreds/internal/models"
)

//go:embed defaults.yaml
var defaultPatternsFS embed.FS

// CompiledPattern is a pattern with its compiled regex.
type CompiledPattern struct {
	models.Pattern
	Re *regexp.Regexp
}

// Load reads built-in patterns and optionally merges a user-supplied file.
func Load(userFile string) ([]CompiledPattern, error) {
	builtIn, err := loadEmbedded()
	if err != nil {
		return nil, fmt.Errorf("load built-in patterns: %w", err)
	}

	patterns := builtIn
	if userFile != "" {
		user, err := loadFromFile(userFile)
		if err != nil {
			return nil, fmt.Errorf("load user patterns: %w", err)
		}
		patterns = mergePatterns(patterns, user)
	}

	return compile(patterns)
}

func loadEmbedded() ([]models.Pattern, error) {
	data, err := defaultPatternsFS.ReadFile("defaults.yaml")
	if err != nil {
		return nil, err
	}
	var pf models.PatternFile
	if err := yaml.Unmarshal(data, &pf); err != nil {
		return nil, err
	}
	return pf.Patterns, nil
}

func loadFromFile(path string) ([]models.Pattern, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var pf models.PatternFile
	if err := yaml.Unmarshal(data, &pf); err != nil {
		return nil, err
	}
	return pf.Patterns, nil
}

func mergePatterns(base, overlay []models.Pattern) []models.Pattern {
	byName := make(map[string]models.Pattern)
	for _, p := range base {
		byName[p.Name] = p
	}
	for _, p := range overlay {
		byName[p.Name] = p
	}
	result := make([]models.Pattern, 0, len(byName))
	for _, p := range byName {
		result = append(result, p)
	}
	return result
}

func compile(patterns []models.Pattern) ([]CompiledPattern, error) {
	compiled := make([]CompiledPattern, 0, len(patterns))
	for _, p := range patterns {
		re, err := regexp.Compile(p.Regex)
		if err != nil {
			return nil, fmt.Errorf("compile pattern %q: %w", p.Name, err)
		}
		compiled = append(compiled, CompiledPattern{
			Pattern: p,
			Re:      re,
		})
	}
	return compiled, nil
}

// ToSearchQueries converts patterns to Bitbucket code search query strings.
// Since Bitbucket search doesn't support full regex, we extract key literal
// fragments to use as search terms, then post-filter with the full regex.
func ToSearchQueries(patterns []CompiledPattern) map[string][]CompiledPattern {
	queryMap := make(map[string][]CompiledPattern)
	for _, p := range patterns {
		query := extractSearchTerm(p.Pattern)
		if query != "" {
			queryMap[query] = append(queryMap[query], p)
		}
	}
	return queryMap
}

// extractSearchTerm pulls a useful literal string from the pattern for use
// in Bitbucket's Elasticsearch-based code search.
func extractSearchTerm(p models.Pattern) string {
	literals := map[string]string{
		"aws_access_key":          "AKIA",
		"aws_secret_key":          "aws_secret_access_key",
		"private_key":             "BEGIN PRIVATE KEY",
		"github_token":            "ghp_",
		"generic_api_key":         "api_key",
		"generic_secret":          "secret_key",
		"generic_password":        "password",
		"slack_token":             "xoxb-",
		"slack_webhook":           "hooks.slack.com",
		"gcp_service_account":     "service_account",
		"azure_connection_string": "DefaultEndpointsProtocol",
		"jwt_token":               "eyJ",
		"database_url":            "://",
		"bearer_token":            "Bearer",
	}
	if q, ok := literals[p.Name]; ok {
		return q
	}
	return p.Name
}
