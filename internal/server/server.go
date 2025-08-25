package server

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
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
		hub: newWSHub(),
		ratelimit: newRateLimiter(),
	}
	s.u = upgrader{Upgrader: websocket.Upgrader{CheckOrigin: s.checkWebsocketOrigin}}
	s.tmpl = template.Must(template.ParseFS(templatesFS, "templates/*.html"))
	go s.hub.run()
	go s.cacheJanitor()

	r := mux.NewRouter()
	r.Use(s.requestLogger)
	r.HandleFunc("/", s.handleIndex).Methods("GET")
	r.HandleFunc("/docs", s.handleDocs).Methods("GET")
	r.HandleFunc("/auth/github/login", s.handleGitHubLogin).Methods("GET")
	// support POST /auth/github to mimic provided form
	r.HandleFunc("/auth/github", s.handleGitHubLogin).Methods("POST")
	r.HandleFunc("/auth/github/callback", s.handleGitHubCallback).Methods("GET")

	ar := r.PathPrefix("/admin").Subrouter()
	ar.Use(s.basicAuth)
	ar.HandleFunc("", s.handleAdmin).Methods("GET")
	ar.HandleFunc("/ws", s.handleAdminWS)
	ar.HandleFunc("/apikeys", s.handleAPIKeys).Methods("POST")
	ar.HandleFunc("/apikeys/{id}/disable", s.handleDisableAPIKey).Methods("POST")
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
		if !ok ||
			subtle.ConstantTimeCompare([]byte(user), []byte(s.cfg.AdminUser)) != 1 ||
			subtle.ConstantTimeCompare([]byte(pass), []byte(s.cfg.AdminPass)) != 1 {
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

func (s *Server) handleDocs(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{
		"BaseURL": s.cfg.BaseURL,
	}
	s.render(w, "docs.html", data)
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
	// issue CSRF token cookie and pass value to template
	csrf := s.issueCSRFCookie(w, r)
	data := s.stats()
	data["csrf"] = csrf
	s.render(w, "admin.html", data)
}

func (s *Server) handleAdminWS(w http.ResponseWriter, r *http.Request) {
	conn, err := s.u.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("admin ws upgrade failed: %v", err)
		return
	}
	client := &wsClient{conn: conn, send: make(chan []byte, 16)}
	s.hub.register <- client
	go client.writePump(s.hub)
	go client.readPump(s.hub)
}

func (s *Server) handleAPIKeys(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil { http.Error(w, err.Error(), 400); return }
	if !s.checkCSRF(r) { http.Error(w, "bad csrf", 403); return }
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
	keyHash := sha256Hex(key)
	// random segment is after last underscore
	randSeg := ""
	if i := strings.LastIndex(key, "_"); i >= 0 && i+1 < len(key) { randSeg = key[i+1:] }
	hint := randSeg
	if len(hint) > 6 { hint = hint[:6] }
	_, err := s.pool.Exec(r.Context(), `INSERT INTO api_keys(key_hash,key_hint,hc_username,app_name,machine,rate_limit_per_sec) VALUES($1,$2,$3,$4,$5,$6)`, keyHash, hint, hc, app, machine, per)
	if err != nil { http.Error(w, err.Error(), 500); return }
	log.Printf("created api key for %s/%s on %s: %s", hc, app, machine, maskKey(key))
	// Show the key once to the admin immediately
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, "<html><body><p>Created key for %s/%s on %s.</p><p><strong>Copy now, you won't see it again:</strong></p><pre>%s</pre><p><a href=\"/admin\">Back to admin</a></p></body></html>", hc, app, machine, key)
}

func (s *Server) handleDisableAPIKey(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err == nil {
		if !s.checkCSRF(r) { http.Error(w, "bad csrf", 403); return }
	}
	id := mux.Vars(r)["id"]
	_, err := s.pool.Exec(r.Context(), `UPDATE api_keys SET disabled=true WHERE id::text=$1`, id)
	if err != nil { http.Error(w, err.Error(), 500); return }
	log.Printf("disabled api key id=%s", id)
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
	apiKeyHash := sha256Hex(apiKey)
	_ = s.pool.QueryRow(r.Context(), `SELECT disabled, rate_limit_per_sec FROM api_keys WHERE key_hash=$1`, apiKeyHash).Scan(&disabled, &perSec)
	if disabled { log.Printf("deny disabled key: %s", maskKey(apiKey)); http.Error(w, "api key disabled", 403); return }
	if !s.ratelimit.Allow(apiKeyHash, perSec) { log.Printf("429 rate limit for key %s", maskKey(apiKey)); http.Error(w, "rate limit exceeded", 429); return }

	// bound body size for safety (configurable)
	if r.ContentLength > 0 && s.cfg.MaxProxyBodyBytes > 0 && r.ContentLength > s.cfg.MaxProxyBodyBytes {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge); return
	}
	if s.cfg.MaxProxyBodyBytes > 0 {
		r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxProxyBodyBytes)
	}
	body, _ := io.ReadAll(r.Body)
	defer r.Body.Close()

	fullTarget := targetWithQuery(target, r.URL.RawQuery)

	cacheable := r.Method == http.MethodGet || r.Method == http.MethodHead
	// Try cache first (GET/HEAD only)
	if cacheable {
		if status, hdrJSON, cached, hit, err := s.cache.Get(r.Context(), r.Method, fullTarget, body); err == nil && hit {
			wHeaderFromJSON(w.Header(), hdrJSON)
			// add debug headers
			w.Header().Set("X-Gh-Proxy-Cache", "hit")
			w.Header().Set("X-Gh-Proxy-Category", ghCategory(fullTarget))
			if disp := s.lookupClientDisplay(r.Context(), apiKeyHash); disp != "" { w.Header().Set("X-Gh-Proxy-Client", disp) }
			w.WriteHeader(status)
			_, _ = w.Write(cached)
			s.afterRequest(r.Context(), apiKeyHash, r.Method, r.URL.Path, status, true)
			return
		}
	}

	// Fetch from GitHub and cache
	status, hdr, respBody, usedToken, err := s.gh.Do(r.Context(), r.Method, fullTarget, body)
	if err != nil { log.Println("proxy error:", err) }
	// Cache successful, cacheable responses (GitHub API responses are safe to cache even if private)
	if cacheable && status == http.StatusOK {
		// Skip caching only if explicitly no-cache or no-store
		if cc := strings.ToLower(hdr.Get("Cache-Control")); !strings.Contains(cc, "no-cache") && !strings.Contains(cc, "no-store") {
			hdrJSON, _ := json.Marshal(hdr)
			_ = s.cache.Put(r.Context(), r.Method, fullTarget, body, status, hdrJSON, respBody)
		}
	}

	wHeaderCopy(w.Header(), hdr)
	// annotate debug headers
	w.Header().Set("X-Gh-Proxy-Cache", "miss")
	w.Header().Set("X-Gh-Proxy-Category", ghCategory(fullTarget))
	if disp := s.lookupClientDisplay(r.Context(), apiKeyHash); disp != "" { w.Header().Set("X-Gh-Proxy-Client", disp) }
	if usedToken != "" {
		var user string
		_ = s.pool.QueryRow(r.Context(), `SELECT github_user FROM donated_tokens WHERE id::text=$1`, usedToken).Scan(&user)
		if user != "" { w.Header().Set("X-Gh-Proxy-Donor", user) }
	}
	w.WriteHeader(status)
	_, _ = w.Write(respBody)

	s.afterRequest(r.Context(), apiKeyHash, r.Method, r.URL.Path, status, false)
}

func (s *Server) afterRequest(ctx context.Context, apiKeyHash, method, path string, status int, hit bool) {
	if hit { s.cacheHits.Add(1) }
	s.totalReq.Add(1)
	s.logRequest(ctx, apiKeyHash, method, path, status, hit)
	log.Printf("%s %s -> %d (%s)", method, path, status, map[bool]string{true:"cache", false:"origin"}[hit])
	s.hub.broadcastRecent(map[string]any{"method":method, "path":path, "created_at": time.Now(), "display": s.lookupClientDisplay(ctx, apiKeyHash)})
	s.hub.broadcastStat(s.stats())
}

func (s *Server) logRequest(ctx context.Context, apiKeyHash, method, path string, status int, hit bool) {
	_, _ = s.pool.Exec(ctx, `INSERT INTO request_logs(api_key,method,path,status,cache_hit) VALUES($1,$2,$3,$4,$5)`, apiKeyHash, method, path, status, hit)
	_, _ = s.pool.Exec(ctx, `UPDATE api_keys SET last_used_at=now() WHERE key_hash=$1`, apiKeyHash)
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
	var hitPct float64
	_ = s.pool.QueryRow(ctx, `SELECT COALESCE(AVG(CASE WHEN cache_hit THEN 1.0 ELSE 0.0 END)*100,0) FROM request_logs`).Scan(&hitPct)
	var today int64
	_ = s.pool.QueryRow(ctx, `SELECT count(*) FROM request_logs WHERE created_at::date = now()::date`).Scan(&today)
	var activeDonated int64
	_ = s.pool.QueryRow(ctx, `SELECT count(*) FROM donated_tokens WHERE revoked=false`).Scan(&activeDonated)
	return map[string]any{
		"totalRequests": s.totalReq.Load(), // incrementing counter since process start
		"cacheHitRate": fmt.Sprintf("%.1f%%", hitPct),
		"today": today,
		"activeTokens": activeDonated,
	}
}

func percent(a, b int64) float64 { if b==0 { return 0 }; return float64(a) * 100 / float64(b) }

func (s *Server) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
	// allow inline script in admin.html (current page uses inline <script>)
	w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src https: data:; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; connect-src 'self'")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, name, data); err != nil { http.Error(w, err.Error(), 500) }
}

// utilities

func targetWithQuery(target, raw string) string { if raw=="" { return target }; if strings.Contains(target, "?") { return target+"&"+raw }; return target+"?"+raw }

func ghCategory(u string) string {
	if strings.Contains(u, "/graphql") { return "graphql" }
	if strings.Contains(u, "/search/code") { return "code_search" }
	if strings.Contains(u, "/search/") { return "search" }
	return "core"
}

func wHeaderCopy(dst http.Header, src http.Header) {
	for k, v := range src {
		if isHopByHop(k) || isBlockedResponseHeader(k) { continue }
		for _, vv := range v { dst.Add(k, vv) }
	}
}

func wHeaderFromJSON(dst http.Header, b []byte) {
	var m map[string][]string
	_ = json.Unmarshal(b, &m)
	for k, v := range m {
		if isHopByHop(k) || isBlockedResponseHeader(k) { continue }
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

func isBlockedResponseHeader(h string) bool {
	switch strings.ToLower(h) {
	case "set-cookie", "strict-transport-security", "public-key-pins", "content-length":
		return true
	default:
		return false
	}
}

// Build display form for a key: hc_app_machine_hint (hint optional)
func formatKeyDisplay(hc, app, machine, hint string) string {
	base := fmt.Sprintf("%s_%s_%s", hc, app, machine)
	if hint == "" { return base }
	return base + "_" + hint
}

// lookup display from api_keys by hash
func (s *Server) lookupClientDisplay(ctx context.Context, keyHash string) string {
	var hc, app, machine, h string
	if err := s.pool.QueryRow(ctx, `SELECT hc_username, app_name, machine, COALESCE(key_hint,'') FROM api_keys WHERE key_hash=$1`, keyHash).Scan(&hc, &app, &machine, &h); err == nil {
		return formatKeyDisplay(hc, app, machine, h)
	}
	return ""
}

func parseAPIKey(v string) string { return strings.TrimSpace(v) }

func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func maskKey(k string) string {
	k = strings.TrimSpace(k)
	if len(k) <= 6 { return "***" }
	return k[:6] + "…" + k[len(k)-4:]
}

// cryptographically secure random string in [a-z0-9]
func randString(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz0123456789"
	rb := make([]byte, n)
	if _, err := rand.Read(rb); err != nil { panic(err) }
	for i := range rb { rb[i] = letters[int(rb[i])%len(letters)] }
	return string(rb)
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

// Ensure websocket upgrades work through our wrapper
func (l *loggingResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hj, ok := l.ResponseWriter.(http.Hijacker); ok {
		return hj.Hijack()
	}
	return nil, nil, errors.New("hijack not supported")
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
	if perSec <= 0 { return false } // do not create buckets for invalid/disabled keys
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

// CSRF helpers for admin (double-submit cookie)
func (s *Server) issueCSRFCookie(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie("admin_csrf"); err == nil && len(c.Value) >= 20 {
		return c.Value
	}
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	token := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)
	http.SetCookie(w, &http.Cookie{
		Name:     "admin_csrf",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   strings.HasPrefix(s.cfg.BaseURL, "https://"),
		MaxAge:   86400 * 7,
	})
	return token
}

func (s *Server) checkCSRF(r *http.Request) bool {
	c, err := r.Cookie("admin_csrf")
	if err != nil { return false }
	return subtle.ConstantTimeCompare([]byte(c.Value), []byte(r.FormValue("csrf"))) == 1
}

func (s *Server) checkWebsocketOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" { return false }
	base, err := url.Parse(s.cfg.BaseURL)
	if err != nil { return false }
	u, err := url.Parse(o)
	if err != nil { return false }
	return u.Scheme == base.Scheme && u.Host == base.Host
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
func (h *wsHub) broadcastRecent(v any) { b, _ := json.Marshal(map[string]any{"type":"recent","data":v}); h.broadcast <- b }

type wsClient struct { conn *websocket.Conn; send chan []byte }

func (c *wsClient) readPump(h *wsHub) { defer func(){ h.unregister<-c; c.conn.Close() }(); for { if _, _, err := c.conn.ReadMessage(); err != nil { break } } }

func (c *wsClient) writePump(h *wsHub) { ticker := time.NewTicker(10*time.Second); defer func(){ ticker.Stop(); c.conn.Close() }(); for { select { case msg, ok := <-c.send: if !ok { _ = c.conn.WriteMessage(websocket.CloseMessage, []byte{}); return }; _ = c.conn.WriteMessage(websocket.TextMessage, msg); case <-ticker.C: _ = c.conn.WriteControl(websocket.PingMessage, []byte("ping"), time.Now().Add(5*time.Second)) } } }

// templates

//go:embed templates/*.html
var templatesFS embed.FS

type upgrader struct{ websocket.Upgrader }
