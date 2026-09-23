package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Client struct {
	pool       *pgxpool.Pool
	http       *http.Client
	refreshing sync.Map

	ratesMu sync.Mutex
	rates   map[rateKey]limitCat
}

type rateKey struct{ tokenID, category string }

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
		pool:  pool,
		rates: make(map[rateKey]limitCat),
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
	// GitHub reports the token's remaining quota on every response; only ask
	// /rate_limit when the headers are missing.
	if !c.recordRateHeaders(id, cat, resp.Header) {
		go c.refreshRate(id, token)
	}
	return resp.StatusCode, resp.Header, b, user, nil
}

func (c *Client) recordRateHeaders(tokenID, category string, h http.Header) bool {
	limit, err1 := strconv.Atoi(h.Get("X-RateLimit-Limit"))
	remaining, err2 := strconv.Atoi(h.Get("X-RateLimit-Remaining"))
	reset, err3 := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64)
	if err1 != nil || err2 != nil || err3 != nil {
		return false
	}
	if r := h.Get("X-RateLimit-Resource"); r != "" {
		category = r
	}
	sample := limitCat{Limit: limit, Remaining: remaining, Reset: time.Unix(reset, 0)}
	k := rateKey{tokenID, category}
	c.ratesMu.Lock()
	if cur, ok := c.rates[k]; !ok || newerRate(sample, cur) {
		c.rates[k] = sample
	}
	c.ratesMu.Unlock()
	return true
}

// newerRate reports whether a supersedes b. Responses can finish out of
// order, but within one window remaining only goes down.
func newerRate(a, b limitCat) bool {
	if !a.Reset.Equal(b.Reset) {
		return a.Reset.After(b.Reset)
	}
	return a.Remaining < b.Remaining
}

// RunRateWriter flushes recorded rate limits every interval until ctx is canceled.
func (c *Client) RunRateWriter(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			fctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			if err := c.FlushRates(fctx); err != nil {
				log.Printf("rate limit flush failed, will retry: %v", err)
			}
			cancel()
		}
	}
}

// FlushRates writes the recorded rate limits in one batch. On failure they
// are kept, unless a newer sample arrived meanwhile, for the next flush.
func (c *Client) FlushRates(ctx context.Context) error {
	c.ratesMu.Lock()
	rates := c.rates
	c.rates = make(map[rateKey]limitCat)
	c.ratesMu.Unlock()
	if len(rates) == 0 {
		return nil
	}
	var ids, cats, tokens []string
	var limits, remaining []int32
	var resets []time.Time
	seen := map[string]bool{}
	for k, v := range rates {
		ids = append(ids, k.tokenID)
		cats = append(cats, k.category)
		limits = append(limits, int32(v.Limit))
		remaining = append(remaining, int32(v.Remaining))
		resets = append(resets, v.Reset)
		if !seen[k.tokenID] {
			seen[k.tokenID] = true
			tokens = append(tokens, k.tokenID)
		}
	}
	batch := &pgx.Batch{}
	// The join skips tokens deleted since the request. The WHERE keeps another
	// instance's older sample from overwriting a newer one.
	batch.Queue(`
		INSERT INTO token_rate_limits (token_id, category, rate_limit, remaining, reset, updated_at)
		SELECT dt.id, v.category, v.rate_limit, v.remaining, v.reset, now()
		FROM unnest($1::text[], $2::text[], $3::int[], $4::int[], $5::timestamptz[]) AS v(token_id, category, rate_limit, remaining, reset)
		JOIN donated_tokens dt ON dt.id = v.token_id::uuid
		ON CONFLICT (token_id, category) DO UPDATE SET
			rate_limit = EXCLUDED.rate_limit, remaining = EXCLUDED.remaining, reset = EXCLUDED.reset, updated_at = now()
		WHERE EXCLUDED.reset > token_rate_limits.reset
		   OR (EXCLUDED.reset = token_rate_limits.reset AND EXCLUDED.remaining <= token_rate_limits.remaining)
	`, ids, cats, limits, remaining, resets)
	batch.Queue(`UPDATE donated_tokens SET last_ok_at = now() WHERE id = ANY($1::text[]::uuid[])`, tokens)
	if err := c.pool.SendBatch(ctx, batch).Close(); err != nil {
		c.ratesMu.Lock()
		for k, v := range rates {
			if cur, ok := c.rates[k]; !ok || newerRate(v, cur) {
				c.rates[k] = v
			}
		}
		c.ratesMu.Unlock()
		return err
	}
	return nil
}
