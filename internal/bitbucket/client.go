package bitbucket

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"golang.org/x/time/rate"
)

type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
	limiter    *rate.Limiter
}

func NewClient(baseURL, token string, rps float64) *Client {
	return &Client{
		baseURL: baseURL,
		token:   token,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		limiter: rate.NewLimiter(rate.Limit(rps), int(rps)+1),
	}
}

func (c *Client) do(ctx context.Context, method, path string, params url.Values) ([]byte, error) {
	if err := c.limiter.Wait(ctx); err != nil {
		return nil, fmt.Errorf("rate limiter: %w", err)
	}

	u := c.baseURL + path
	if params != nil {
		u += "?" + params.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 429 {
		retryAfter := resp.Header.Get("Retry-After")
		wait := 5 * time.Second
		if retryAfter != "" {
			if secs, err := strconv.Atoi(retryAfter); err == nil {
				wait = time.Duration(secs) * time.Second
			}
		}
		select {
		case <-time.After(wait):
			return c.do(ctx, method, path, params)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("api error (status %d): %s", resp.StatusCode, string(body))
	}

	return io.ReadAll(resp.Body)
}

// Project represents a Bitbucket project.
type Project struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

// Repository represents a Bitbucket repository.
type Repository struct {
	Slug    string  `json:"slug"`
	Name    string  `json:"name"`
	Project Project `json:"project"`
}

// SearchResult represents a single code search hit.
type SearchResult struct {
	File       SearchFile       `json:"file"`
	HitContexts []HitContext   `json:"hitContexts"`
}

type SearchFile struct {
	Path   string `json:"path"`
	Repo   string `json:"repository"`
	Project string `json:"project"`
}

type HitContext struct {
	Lines []HitLine `json:"lines"`
}

type HitLine struct {
	Text string `json:"text"`
	Line int    `json:"line"`
}

type searchResponse struct {
	Values        []json.RawMessage `json:"values"`
	Size          int               `json:"size"`
	IsLastPage    bool              `json:"isLastPage"`
	NextPageStart int               `json:"nextPageStart"`
}

type repoResponse struct {
	Values        []Repository `json:"values"`
	IsLastPage    bool         `json:"isLastPage"`
	NextPageStart int          `json:"nextPageStart"`
}

// SearchCode performs a code search query and returns all pages of results.
func (c *Client) SearchCode(ctx context.Context, query string) ([]SearchResult, error) {
	var allResults []SearchResult
	start := 0

	for {
		params := url.Values{}
		params.Set("query", query)
		params.Set("type", "code")
		params.Set("limit", "25")
		params.Set("start", strconv.Itoa(start))

		body, err := c.do(ctx, http.MethodGet, "/rest/search/1.0/search", params)
		if err != nil {
			return allResults, err
		}

		var resp searchResponse
		if err := json.Unmarshal(body, &resp); err != nil {
			return allResults, fmt.Errorf("decode search response: %w", err)
		}

		for _, raw := range resp.Values {
			var result SearchResult
			if err := json.Unmarshal(raw, &result); err != nil {
				continue
			}
			allResults = append(allResults, result)
		}

		if resp.IsLastPage || resp.Size == 0 {
			break
		}
		start = resp.NextPageStart
	}

	return allResults, nil
}

// ListAllRepos returns all repositories accessible by the token.
func (c *Client) ListAllRepos(ctx context.Context) ([]Repository, error) {
	var allRepos []Repository
	start := 0

	for {
		params := url.Values{}
		params.Set("limit", "100")
		params.Set("start", strconv.Itoa(start))

		body, err := c.do(ctx, http.MethodGet, "/rest/api/1.0/repos", params)
		if err != nil {
			return allRepos, err
		}

		var resp repoResponse
		if err := json.Unmarshal(body, &resp); err != nil {
			return allRepos, fmt.Errorf("decode repos response: %w", err)
		}

		allRepos = append(allRepos, resp.Values...)

		if resp.IsLastPage {
			break
		}
		start = resp.NextPageStart
	}

	return allRepos, nil
}

// GetRepoCloneURL returns the HTTP clone URL for a repo.
func (c *Client) GetRepoCloneURL(projectKey, repoSlug string) string {
	return fmt.Sprintf("%s/scm/%s/%s.git", c.baseURL, projectKey, repoSlug)
}
