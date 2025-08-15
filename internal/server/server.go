package server

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5/pgxpool"

	"gh-proxy/internal/cache"
	"gh-proxy/internal/config"
	gh "gh-proxy/internal/github"
)

type Server struct {
	Router *mux.Router
	pool *pgxpool.Pool
	cfg config.Config
	cache *cache.Cache
	gh *gh.Client
	u upgrader
	// metrics
	totalReq atomic.Int64
	cacheHits atomic.Int64
	hub *wsHub
	tmpl *template.Template
	// rate limiting
	ratelimit *rateLimiter
}

func New(pool *pgxpool.Pool, cfg config.Config) *Server {
	s := &Server{
		pool: pool,
		cfg: cfg,
		cache: cache.New(pool, cfg.MaxCacheTime.Duration(), cfg.MaxCacheSizeMB),
		gh: gh.New(pool),
		u: upgrader{Upgrader: websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}},
		hub: newWSHub(),
		ratelimit: newRateLimiter(),
	}
	s.tmpl = template.Must(template.ParseFS(templatesFS, "templates/*.html"))
	go s.hub.run()
	go s.cacheJanitor()

	r := mux.NewRouter()
	r.Use(s.requestLogger)
	r.HandleFunc("/", s.handleIndex).Methods("GET")
	r.HandleFunc("/auth/github/login", s.handleGitHubLogin).Methods("GET")
	// support POST /auth/github to mimic provided form
	r.HandleFunc("/auth/github", s.handleGitHubLogin).Methods("POST")
	r.HandleFunc("/auth/github/callback", s.handleGitHubCallback).Methods("GET")

	ar := r.PathPrefix("/admin").Subrouter()
	ar.Use(s.basicAuth)
	ar.HandleFunc("", s.handleAdmin).Methods("GET")
	ar.HandleFunc("/ws", s.handleAdminWS)
	ar.HandleFunc("/apikeys", s.handleAPIKeys).Methods("POST")
	ar.HandleFunc("/apikeys/{key}/disable", s.handleDisableAPIKey).Methods("POST")
	ar.HandleFunc("/keys.json", s.handleAdminKeysJSON).Methods("GET")
	ar.HandleFunc("/keys_usage.json", s.handleAdminKeysUsageJSON).Methods("GET")
	ar.HandleFunc("/recent.json", s.handleAdminRecentJSON).Methods("GET")

	r.HandleFunc("/gh/{rest:.*}", s.handleProxyREST)
	r.HandleFunc("/gh/graphql", s.handleProxyGraphQL)

	s.Router = r
	return s
}

func (s *Server) cacheJanitor() {
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		_ = s.cache.Cleanup(ctx)
		cancel()
		time.Sleep(1 * time.Minute)
	}
}

func (s *Server) basicAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != s.cfg.AdminUser || pass != s.cfg.AdminPass {
			w.Header().Set("WWW-Authenticate", "Basic realm=Restricted")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte("Unauthorized"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	var donors int
	var lastUser, lastURL, lastAgo string
	var lastAt *time.Time
	_ = s.pool.QueryRow(r.Context(), `SELECT COUNT(*) FROM donated_tokens WHERE revoked=false`).Scan(&donors)
	_ = s.pool.QueryRow(r.Context(), `SELECT github_user, created_at FROM donated_tokens WHERE revoked=false ORDER BY created_at DESC LIMIT 1`).Scan(&lastUser, &lastAt)
	if lastUser != "" { lastURL = "https://github.com/" + lastUser }
	if lastAt != nil { lastAgo = humanizeDuration(time.Since(*lastAt)) }
	data := map[string]any{
		"Donors": donors,
		"LastUser": lastUser,
		"LastURL": lastURL,
		"LastAgo": lastAgo,
	}
	s.render(w, "index.html", data)
}

func humanizeDuration(d time.Duration) string {
	if d < time.Minute { return "just now" }
	if d < time.Hour { return fmt.Sprintf("%d minutes ago", int(d.Minutes())) }
	if d < 24*time.Hour { return fmt.Sprintf("%d hours ago", int(d.Hours())) }
	days := int(d.Hours() / 24)
	if days < 30 { return fmt.Sprintf("%d days ago", days) }
	months := days / 30
	if months < 12 { return fmt.Sprintf("%d months ago", months) }
	years := months / 12
	return fmt.Sprintf("%d years ago", years)
}

func (s *Server) handleAdmin(w http.ResponseWriter, r *http.Request) {
	// initial render with current stats; live updates via WS
	s.render(w, "admin.html", s.stats())
}

func (s *Server) handleAdminWS(w http.ResponseWriter, r *http.Request) {
	conn, err := s.u.Upgrade(w, r, nil)
	if err != nil { return }
	client := &wsClient{conn: conn, send: make(chan []byte, 16)}
	s.hub.register <- client
	go client.writePump(s.hub)
	go client.readPump(s.hub)
}

func (s *Server) handleAPIKeys(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil { http.Error(w, err.Error(), 400); return }
	hc := r.FormValue("hc_username")
	app := r.FormValue("app_name")
	machine := r.FormValue("machine")
	rl := r.FormValue("rate_limit")
	if hc==""||app==""||machine=="" { http.Error(w, "missing fields", 400); return }
	per := 10
	if rl != "" { if x, err := strconv.Atoi(rl); err==nil && x>0 { per = x } }
	prefix := fmt.Sprintf("%s_%s_%s_", hc, app, machine)
	suffix := randString(24)
	key := prefix + suffix
	_, err := s.pool.Exec(r.Context(), `INSERT INTO api_keys(key,hc_username,app_name,machine,rate_limit_per_sec) VALUES($1,$2,$3,$4,$5)`, key, hc, app, machine, per)
	if err != nil { http.Error(w, err.Error(), 500); return }
	log.Printf("created api key for %s/%s on %s: %s", hc, app, machine, maskKey(key))
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (s *Server) handleDisableAPIKey(w http.ResponseWriter, r *http.Request) {
	key := mux.Vars(r)["key"]
	_, err := s.pool.Exec(r.Context(), `UPDATE api_keys SET disabled=true WHERE key=$1`, key)
	if err != nil { http.Error(w, err.Error(), 500); return }
	log.Printf("disabled api key %s", maskKey(key))
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (s *Server) handleProxyREST(w http.ResponseWriter, r *http.Request) {
	s.serveProxy(w, r, "https://api.github.com/"+mux.Vars(r)["rest"])
}

func (s *Server) handleProxyGraphQL(w http.ResponseWriter, r *http.Request) {
	s.serveProxy(w, r, "https://api.github.com/graphql")
}

func (s *Server) serveProxy(w http.ResponseWriter, r *http.Request, target string) {
	apiKey := parseAPIKey(r.Header.Get("X-API-Key"))
	if apiKey == "" { http.Error(w, "missing X-API-Key", 401); return }
	// rate limit and disabled check
	var disabled bool
	var perSec int
	_ = s.pool.QueryRow(r.Context(), `SELECT disabled, rate_limit_per_sec FROM api_keys WHERE key=$1`, apiKey).Scan(&disabled, &perSec)
	if disabled { log.Printf("deny disabled key: %s", maskKey(apiKey)); http.Error(w, "api key disabled", 403); return }
	if !s.ratelimit.Allow(apiKey, perSec) { log.Printf("429 rate limit for key %s", maskKey(apiKey)); http.Error(w, "rate limit exceeded", 429); return }

	body, _ := io.ReadAll(r.Body)
	defer r.Body.Close()

	fullTarget := targetWithQuery(target, r.URL.RawQuery)

	// Try cache first
	if status, hdrJSON, cached, hit, err := s.cache.Get(r.Context(), r.Method, fullTarget, body); err == nil && hit {
		wHeaderFromJSON(w.Header(), hdrJSON)
		w.Header().Set("X-Cache-Hit", "1")
		w.WriteHeader(status)
		_, _ = w.Write(cached)
		s.afterRequest(r.Context(), apiKey, r.Method, r.URL.Path, status, true)
		return
	}

	// Fetch from GitHub and cache
	status, hdr, respBody, usedToken, err := s.gh.Do(r.Context(), r.Method, fullTarget, body)
	if err != nil { log.Println("proxy error:", err) }
	hdrJSON, _ := json.Marshal(hdr)
	_ = s.cache.Put(r.Context(), r.Method, fullTarget, body, status, hdrJSON, respBody)

	wHeaderCopy(w.Header(), hdr)
	// annotate which token/user was used
	if usedToken != "" {
		var user string
		_ = s.pool.QueryRow(r.Context(), `SELECT github_user FROM donated_tokens WHERE id::text=$1`, usedToken).Scan(&user)
		if user != "" { w.Header().Set("X-Proxy-Token-User", user) }
	}
	w.WriteHeader(status)
	_, _ = w.Write(respBody)

	s.afterRequest(r.Context(), apiKey, r.Method, r.URL.Path, status, false)
}

func (s *Server) afterRequest(ctx context.Context, apiKey, method, path string, status int, hit bool) {
	if hit { s.cacheHits.Add(1) }
	s.totalReq.Add(1)
	s.logRequest(ctx, apiKey, method, path, status, hit)
	log.Printf("%s %s -> %d (%s)", method, path, status, map[bool]string{true:"cache", false:"origin"}[hit])
	s.hub.broadcastRecent(fmt.Sprintf("%s %s", method, path))
	s.hub.broadcastStat(s.stats())
}

func (s *Server) logRequest(ctx context.Context, apiKey, method, path string, status int, hit bool) {
	_, _ = s.pool.Exec(ctx, `INSERT INTO request_logs(api_key,method,path,status,cache_hit) VALUES($1,$2,$3,$4,$5)`, apiKey, method, path, status, hit)
	_, _ = s.pool.Exec(ctx, `UPDATE api_keys SET last_used_at=now() WHERE key=$1`, apiKey)
}

// prune request_logs to keep only latest N rows periodically (avoid doing it on hot path)
func (s *Server) LogsJanitor() {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for range t.C {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		// delete rows with id <= (max(id) - 1000)
		_, _ = s.pool.Exec(ctx, `DELETE FROM request_logs WHERE id <= GREATEST((SELECT COALESCE(MAX(id),0) FROM request_logs) - 1000, 0)`)
		cancel()
	}
}

func (s *Server) stats() map[string]any {
	ctx := context.Background()
	var total int64
	_ = s.pool.QueryRow(ctx, `SELECT count(*) FROM request_logs`).Scan(&total)
	var hitPct float64
	_ = s.pool.QueryRow(ctx, `SELECT COALESCE(AVG(CASE WHEN cache_hit THEN 1.0 ELSE 0.0 END)*100,0) FROM request_logs`).Scan(&hitPct)
	var today int64
	_ = s.pool.QueryRow(ctx, `SELECT count(*) FROM request_logs WHERE created_at::date = now()::date`).Scan(&today)
	var activeDonated int64
	_ = s.pool.QueryRow(ctx, `SELECT count(*) FROM donated_tokens WHERE revoked=false`).Scan(&activeDonated)
	return map[string]any{
		"totalRequests": total,
		"cacheHitRate": fmt.Sprintf("%.1f%%", hitPct),
		"today": today,
		"activeTokens": activeDonated,
	}
}

func percent(a, b int64) float64 { if b==0 { return 0 }; return float64(a) * 100 / float64(b) }

func (s *Server) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, name, data); err != nil { http.Error(w, err.Error(), 500) }
}

// utilities

func targetWithQuery(target, raw string) string { if raw=="" { return target }; if strings.Contains(target, "?") { return target+"&"+raw }; return target+"?"+raw }

func wHeaderCopy(dst http.Header, src http.Header) {
	for k, v := range src {
		if isHopByHop(k) { continue }
		for _, vv := range v { dst.Add(k, vv) }
	}
}

func wHeaderFromJSON(dst http.Header, b []byte) {
	var m map[string][]string
	_ = json.Unmarshal(b, &m)
	for k, v := range m {
		if isHopByHop(k) { continue }
		for _, vv := range v { dst.Add(k, vv) }
	}
}

func isHopByHop(h string) bool {
	switch strings.ToLower(h) {
	case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade":
		return true
	default:
		return false
	}
}

func parseAPIKey(v string) string { return strings.TrimSpace(v) }

func maskKey(k string) string {
	k = strings.TrimSpace(k)
	if len(k) <= 6 { return "***" }
	return k[:6] + "…" + k[len(k)-4:]
}

func randString(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b { b[i] = letters[int(time.Now().UnixNano()+int64(i))%len(letters)] }
	return string(b)
}

// WebSocket hub

type wsHub struct {
	clients map[*wsClient]bool
	broadcast chan []byte
	register chan *wsClient
	unregister chan *wsClient
}

// request logger middleware
func (s *Server) requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		lrw := &loggingResponseWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(lrw, r)
		dur := time.Since(start)
		apiKey := maskKey(r.Header.Get("X-API-Key"))
		log.Printf("%s %s %d (%s) ua=%q key=%s", r.Method, r.URL.Path, lrw.status, dur.Truncate(time.Millisecond), r.UserAgent(), apiKey)
	})
}

type loggingResponseWriter struct {
	http.ResponseWriter
	status int
}

func (l *loggingResponseWriter) WriteHeader(code int) {
	l.status = code
	l.ResponseWriter.WriteHeader(code)
}

// simple in-memory token bucket per API key
type rateLimiter struct {
	mu sync.Mutex
	buckets map[string]*bucket
}

type bucket struct {
	capacity int
	tokens float64
	last time.Time
}

func newRateLimiter() *rateLimiter { return &rateLimiter{buckets: make(map[string]*bucket)} }

func (rl *rateLimiter) Allow(key string, perSec int) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	b := rl.buckets[key]
	if b == nil { b = &bucket{capacity: perSec, tokens: float64(perSec), last: time.Now()}; rl.buckets[key] = b }
	// refill
	now := time.Now()
	dt := now.Sub(b.last).Seconds()
	b.last = now
	b.tokens += dt * float64(perSec)
	if b.tokens > float64(b.capacity) { b.tokens = float64(b.capacity) }
	if b.tokens >= 1 {
		b.tokens -= 1
		return true
	}
	return false
}

func newWSHub() *wsHub { return &wsHub{clients: map[*wsClient]bool{}, broadcast: make(chan []byte, 16), register: make(chan *wsClient), unregister: make(chan *wsClient)} }

func (h *wsHub) run() {
	for {
		select {
		case c := <-h.register:
			h.clients[c] = true
		case c := <-h.unregister:
			if _, ok := h.clients[c]; ok { delete(h.clients, c); close(c.send) }
		case msg := <-h.broadcast:
			for c := range h.clients { select { case c.send <- msg: default: delete(h.clients, c); close(c.send) } }
		}
	}
}

func (h *wsHub) broadcastStat(m map[string]any) { b, _ := json.Marshal(map[string]any{"type":"stats","data":m}); h.broadcast <- b }
func (h *wsHub) broadcastRecent(s string) { b, _ := json.Marshal(map[string]any{"type":"recent","data":s}); h.broadcast <- b }

type wsClient struct { conn *websocket.Conn; send chan []byte }

func (c *wsClient) readPump(h *wsHub) { defer func(){ h.unregister<-c; c.conn.Close() }(); for { if _, _, err := c.conn.ReadMessage(); err != nil { break } } }

func (c *wsClient) writePump(h *wsHub) { ticker := time.NewTicker(10*time.Second); defer func(){ ticker.Stop(); c.conn.Close() }(); for { select { case msg, ok := <-c.send: if !ok { _ = c.conn.WriteMessage(websocket.CloseMessage, []byte{}); return }; _ = c.conn.WriteMessage(websocket.TextMessage, msg); case <-ticker.C: _ = c.conn.WriteControl(websocket.PingMessage, []byte("ping"), time.Now().Add(5*time.Second)) } } }

// templates

//go:embed templates/*.html
var templatesFS embed.FS

type upgrader struct{ websocket.Upgrader }
