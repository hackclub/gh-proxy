package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Client struct {
	pool *pgxpool.Pool
	http *http.Client
	refreshing sync.Map
}

func New(pool *pgxpool.Pool) *Client {
	// Optimized HTTP client for high throughput
	transport := &http.Transport{
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   20,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
	}

	return &Client{
		pool: pool,
		http: &http.Client{
			Timeout:   15 * time.Second, // Faster timeout for high throughput
			Transport: transport,
		},
	}
}

type rateLimits struct {
	Core, Search, CodeSearch, GraphQL limitCat
}

type limitCat struct {
	Limit, Remaining int
	Reset            time.Time
}

type rateAPIResp struct {
	Resources map[string]struct {
		Limit     int   `json:"limit"`
		Used      int   `json:"used"`
		Remaining int   `json:"remaining"`
		Reset     int64 `json:"reset"`
	} `json:"resources"`
}

func (c *Client) refreshRate(tokenID string, token string) {
	if _, busy := c.refreshing.LoadOrStore(tokenID, struct{}{}); busy {
		return
	}
	defer c.refreshing.Delete(tokenID)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/rate_limit", nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	var rr rateAPIResp
	_ = json.NewDecoder(resp.Body).Decode(&rr)
	// One connection, one round trip for every category.
	batch := &pgx.Batch{}
	for k, v := range rr.Resources {
		reset := time.Unix(v.Reset, 0)
		batch.Queue(`INSERT INTO token_rate_limits(token_id,category,rate_limit,remaining,reset,updated_at) VALUES($1,$2,$3,$4,$5,now()) ON CONFLICT (token_id,category) DO UPDATE SET rate_limit=EXCLUDED.rate_limit, remaining=EXCLUDED.remaining, reset=EXCLUDED.reset, updated_at=now()`, tokenID, k, v.Limit, v.Remaining, reset)
	}
	batch.Queue(`UPDATE donated_tokens SET last_ok_at=now() WHERE id=$1`, tokenID)
	_ = c.pool.SendBatch(ctx, batch).Close()
}

func (c *Client) chooseToken(ctx context.Context, category string) (id, token, user string, err error) {
	err = c.pool.QueryRow(ctx, `
		SELECT dt.id::text, dt.token, dt.github_user
		FROM donated_tokens dt
		LEFT JOIN token_rate_limits tr ON tr.token_id = dt.id AND tr.category = $1
		WHERE dt.revoked = false
		ORDER BY COALESCE(tr.remaining, 0) DESC, COALESCE(tr.reset, 'epoch') ASC, COALESCE(dt.last_ok_at, 'epoch') ASC
		LIMIT 1`, category).Scan(&id, &token, &user)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", "", errors.New("no donated tokens")
	}
	return id, token, user, err
}

func categoryFor(url string) string {
	if strings.Contains(url, "/graphql") {
		return "graphql"
	}
	if strings.Contains(url, "/search/code") {
		return "code_search"
	}
	if strings.Contains(url, "/search/") {
		return "search"
	}
	return "core"
}

func (c *Client) Do(ctx context.Context, method, rawURL string, body []byte) (status int, headers http.Header, respBody []byte, donor string, err error) {
	parsed, perr := url.Parse(rawURL)
	if perr != nil {
		return 0, nil, nil, "", fmt.Errorf("invalid url: %w", perr)
	}
	if parsed.Scheme != "https" || parsed.Host != "api.github.com" {
		return 0, nil, nil, "", fmt.Errorf("disallowed request target")
	}
	safeURL := parsed.String()
	cat := categoryFor(safeURL)
	id, token, user, err := c.chooseToken(ctx, cat)
	if err != nil {
		return 0, nil, nil, "", err
	}
	req, err := http.NewRequestWithContext(ctx, method, safeURL, bytes.NewReader(body))
	if err != nil {
		return 0, nil, nil, "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "gh-proxy/1.0")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, nil, "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		// Only revoke on 401 or explicit bad credentials
		shouldRevoke := resp.StatusCode == 401
		if resp.StatusCode == 403 {
			var em struct {
				Message string `json:"message"`
			}
			_ = json.Unmarshal(b, &em)
			if strings.Contains(strings.ToLower(em.Message), "bad credentials") {
				shouldRevoke = true
			}
		}
		if shouldRevoke {
			_, _ = c.pool.Exec(ctx, `UPDATE donated_tokens SET revoked=true WHERE id=$1`, id)
			logMsg := "token unauthorized; marked revoked"
			if user != "" {
				logMsg += " (@" + user + ")"
			}
			return resp.StatusCode, resp.Header, b, user, errors.New(logMsg)
		}
	}
	// update rate limits from headers if present
	// Alternatively call /rate_limit periodically
	go c.refreshRate(id, token)
	return resp.StatusCode, resp.Header, b, user, nil
}
