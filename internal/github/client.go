package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Client struct {
	pool *pgxpool.Pool
	http *http.Client
}

func New(pool *pgxpool.Pool) *Client {
	return &Client{pool: pool, http: &http.Client{Timeout: 30 * time.Second}}
}

type rateLimits struct {
	Core, Search, CodeSearch, GraphQL limitCat
}

type limitCat struct { Limit, Remaining int; Reset time.Time }

type rateAPIResp struct {
	Resources map[string]struct{
		Limit int `json:"limit"`
		Used int `json:"used"`
		Remaining int `json:"remaining"`
		Reset int64 `json:"reset"`
	} `json:"resources"`
}

func (c *Client) refreshRate(ctx context.Context, tokenID string, token string) {
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/rate_limit", nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	if token != "" { req.Header.Set("Authorization", "Bearer "+token) }
	resp, err := c.http.Do(req)
	if err != nil { return }
	defer resp.Body.Close()
	var rr rateAPIResp
	_ = json.NewDecoder(resp.Body).Decode(&rr)
	for k, v := range rr.Resources {
		reset := time.Unix(v.Reset, 0)
		_, _ = c.pool.Exec(ctx, `INSERT INTO token_rate_limits(token_id,category,rate_limit,remaining,reset,updated_at) VALUES($1,$2,$3,$4,$5,now()) ON CONFLICT (token_id,category) DO UPDATE SET rate_limit=EXCLUDED.rate_limit, remaining=EXCLUDED.remaining, reset=EXCLUDED.reset, updated_at=now()`, tokenID, k, v.Limit, v.Remaining, reset)
	}
	_, _ = c.pool.Exec(ctx, `UPDATE donated_tokens SET last_ok_at=now() WHERE id=$1`, tokenID)
}

func (c *Client) chooseToken(ctx context.Context, category string) (id string, token string, err error) {
	rows, err := c.pool.Query(ctx, `SELECT id::text, token FROM donated_tokens WHERE revoked=false ORDER BY COALESCE(last_ok_at, 'epoch') ASC`)
	if err != nil { return "", "", err }
	defer rows.Close()
	type tk struct{ id, token string; remaining int; reset time.Time }
	var toks []tk
	for rows.Next() {
		var id, tkstr string
		if err := rows.Scan(&id, &tkstr); err != nil { return "", "", err }
		var rem int; var reset time.Time
		_ = c.pool.QueryRow(ctx, `SELECT remaining, reset FROM token_rate_limits WHERE token_id=$1 AND category=$2`, id, category).Scan(&rem, &reset)
		toks = append(toks, tk{id: id, token: tkstr, remaining: rem, reset: reset})
	}
	if len(toks) == 0 { return "", "", errors.New("no donated tokens") }
	sort.Slice(toks, func(i,j int) bool { if toks[i].remaining==toks[j].remaining { return toks[i].reset.Before(toks[j].reset) }; return toks[i].remaining>toks[j].remaining })
	ch := toks[0]
	return ch.id, ch.token, nil
}

func categoryFor(url string) string {
	if strings.Contains(url, "/graphql") { return "graphql" }
	if strings.Contains(url, "/search/code") { return "code_search" }
	if strings.Contains(url, "/search/") { return "search" }
	return "core"
}

func (c *Client) Do(ctx context.Context, method, url string, body []byte) (status int, headers http.Header, respBody []byte, usedToken string, err error) {
	cat := categoryFor(url)
	id, token, err := c.chooseToken(ctx, cat)
	if err != nil { return 0, nil, nil, "", err }
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil { return 0, nil, nil, "", err }
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "gh-proxy/1.0")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.http.Do(req)
	if err != nil { return 0, nil, nil, "", err }
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		var user string
		_ = c.pool.QueryRow(ctx, `SELECT github_user FROM donated_tokens WHERE id::text=$1`, id).Scan(&user)
		// Only revoke on 401 or explicit bad credentials
		shouldRevoke := resp.StatusCode == 401
		if resp.StatusCode == 403 {
			var em struct{ Message string `json:"message"` }
			_ = json.Unmarshal(b, &em)
			if strings.Contains(strings.ToLower(em.Message), "bad credentials") {
				shouldRevoke = true
			}
		}
		if shouldRevoke {
			_, _ = c.pool.Exec(ctx, `UPDATE donated_tokens SET revoked=true WHERE id=$1`, id)
			logMsg := "token unauthorized; marked revoked"
			if user != "" { logMsg += " (@" + user + ")" }
			return resp.StatusCode, resp.Header, b, id, fmt.Errorf(logMsg)
		}
	}
	// update rate limits from headers if present
	// Alternatively call /rate_limit periodically
	go c.refreshRate(context.Background(), id, token)
	return resp.StatusCode, resp.Header, b, id, nil
}
