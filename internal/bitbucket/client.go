package bitbucket

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
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
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	return &Client{
		baseURL: baseURL,
		token:   token,
		httpClient: &http.Client{
			Timeout:   30 * time.Second,
			Transport: transport,
		},
		limiter: rate.NewLimiter(rate.Limit(rps), int(rps)+1),
	}
}

func (c *Client) doGet(ctx context.Context, path string, params url.Values) ([]byte, error) {
	if err := c.limiter.Wait(ctx); err != nil {
		return nil, fmt.Errorf("rate limiter: %w", err)
	}

	u := c.baseURL + path
	if params != nil {
		u += "?" + params.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	return c.execute(ctx, req, "GET", path, params, nil)
}

func (c *Client) doPost(ctx context.Context, path string, payload interface{}) ([]byte, error) {
	if err := c.limiter.Wait(ctx); err != nil {
		return nil, fmt.Errorf("rate limiter: %w", err)
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal request body: %w", err)
	}

	u := c.baseURL + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	return c.execute(ctx, req, "POST", path, nil, payload)
}

func (c *Client) execute(ctx context.Context, req *http.Request, method, path string, params url.Values, payload interface{}) ([]byte, error) {
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
			if method == "POST" {
				return c.doPost(ctx, path, payload)
			}
			return c.doGet(ctx, path, params)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("api error (status %d): %s", resp.StatusCode, string(respBody))
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

// searchRequest is the JSON body for the Bitbucket code search API.
type searchRequest struct {
	Query    string         `json:"query"`
	Entities searchEntities `json:"entities"`
}

type searchEntities struct {
	Code searchPagination `json:"code"`
}

type searchPagination struct {
	Start int `json:"start"`
	Limit int `json:"limit"`
}

type searchResponse struct {
	Code *searchCodeResponse `json:"code"`
}

type searchCodeResponse struct {
	Values        []json.RawMessage `json:"values"`
	Count         int               `json:"count"`
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
	maxPages := 100

	for page := 0; page < maxPages; page++ {
		if ctx.Err() != nil {
			return allResults, ctx.Err()
		}

		reqBody := searchRequest{
			Query: query,
			Entities: searchEntities{
				Code: searchPagination{
					Start: start,
					Limit: 25,
				},
			},
		}

		body, err := c.doPost(ctx, "/rest/search/latest/search", reqBody)
		if err != nil {
			return allResults, err
		}

		if page == 0 {
			log.Printf("[search] Raw first page response for %q: %s", query, truncateLog(body, 500))
		}

		var resp searchResponse
		if err := json.Unmarshal(body, &resp); err != nil {
			return allResults, fmt.Errorf("decode search response: %w", err)
		}

		if resp.Code == nil || len(resp.Code.Values) == 0 {
			break
		}

		for _, raw := range resp.Code.Values {
			var result SearchResult
			if err := json.Unmarshal(raw, &result); err != nil {
				continue
			}
			allResults = append(allResults, result)
		}

		log.Printf("[search] Query %q: page %d, got %d results (total so far: %d)", query, page+1, len(resp.Code.Values), len(allResults))

		if resp.Code.IsLastPage {
			break
		}

		nextStart := resp.Code.NextPageStart
		if nextStart <= start {
			log.Printf("[search] Query %q: nextPageStart (%d) not advancing past current (%d), stopping", query, nextStart, start)
			break
		}
		start = nextStart
	}

	return allResults, nil
}

func truncateLog(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "...(truncated)"
}

// ListAllRepos returns all repositories accessible by the token.
func (c *Client) ListAllRepos(ctx context.Context) ([]Repository, error) {
	var allRepos []Repository
	start := 0

	for {
		params := url.Values{}
		params.Set("limit", "100")
		params.Set("start", strconv.Itoa(start))

		body, err := c.doGet(ctx, "/rest/api/1.0/repos", params)
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
