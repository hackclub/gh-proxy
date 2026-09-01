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
	"io/fs"
	"log"
	"math"
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
	"github.com/jackc/pgx/v5"
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

	s.Router = s.routes()
	return s
}

// routes builds the HTTP router. Kept separate from New so it can be
// exercised without a database.
func (s *Server) routes() *mux.Router {
	r := mux.NewRouter()
	r.Use(s.requestLogger)
	r.Use(s.rateLimitPolicyHeader)
	r.HandleFunc("/", s.handleIndex).Methods("GET", "HEAD")
	r.HandleFunc("/docs", s.handleDocs).Methods("GET", "HEAD")
	r.HandleFunc("/openapi.json", s.handleOpenAPI).Methods("GET", "HEAD")
	r.HandleFunc("/llms.txt", s.handleLLMsTxt).Methods("GET", "HEAD")
	r.HandleFunc("/robots.txt", s.handleRobotsTxt).Methods("GET", "HEAD")
	r.HandleFunc("/sitemap.xml", s.handleSitemap).Methods("GET", "HEAD")
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

	// Unknown paths and wrong methods still get an actionable, machine
	// readable body instead of Go's bare "404 page not found".
	r.NotFoundHandler = s.wrapMiddleware(http.HandlerFunc(s.handleNotFound))
	r.MethodNotAllowedHandler = s.wrapMiddleware(http.HandlerFunc(s.handleMethodNotAllowed))

	return r
}

// wrapMiddleware applies the router-level middleware to handlers gorilla/mux
// invokes outside the normal match path (NotFound / MethodNotAllowed).
func (s *Server) wrapMiddleware(h http.Handler) http.Handler {
	return s.requestLogger(s.rateLimitPolicyHeader(h))
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
			if wantsJSON(r) {
				s.jsonError(w, "UNAUTHORIZED", "Admin authentication required", "Send HTTP Basic credentials for the admin user", http.StatusUnauthorized)
				return
			}
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

func (s *Server) handleOpenAPI(w http.ResponseWriter, r *http.Request) {
	data, err := fs.ReadFile(openapiFS, "openapi.json")
	if err != nil {
		s.jsonError(w, "INTERNAL_ERROR", "Failed to load OpenAPI specification", "", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
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
	if err := r.ParseForm(); err != nil { s.jsonError(w, "INVALID_REQUEST", "Failed to parse form data", err.Error(), 400); return }
	if !s.checkCSRF(r) { s.jsonError(w, "CSRF_FAILED", "Invalid CSRF token", "Please refresh the page and try again", 403); return }
	hc := r.FormValue("hc_username")
	app := r.FormValue("app_name")
	machine := r.FormValue("machine")
	rl := r.FormValue("rate_limit")
	if hc==""||app==""||machine=="" { s.jsonError(w, "MISSING_FIELDS", "Missing required fields", "Provide hc_username, app_name, and machine", 400); return }
	per := defaultRateLimitPerSec
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
	if err != nil { s.jsonError(w, "DB_ERROR", "Failed to create API key", err.Error(), 500); return }
	log.Printf("created api key for %s/%s on %s: %s", hc, app, machine, maskKey(key))
	// Show the key once to the admin immediately
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, "<html><body><p>Created key for %s/%s on %s.</p><p><strong>Copy now, you won't see it again:</strong></p><pre>%s</pre><p><a href=\"/admin\">Back to admin</a></p></body></html>", hc, app, machine, key)
}

func (s *Server) handleDisableAPIKey(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err == nil {
		if !s.checkCSRF(r) { s.jsonError(w, "CSRF_FAILED", "Invalid CSRF token", "Please refresh the page and try again", 403); return }
	}
	id := mux.Vars(r)["id"]
	_, err := s.pool.Exec(r.Context(), `UPDATE api_keys SET disabled=true WHERE id::text=$1`, id)
	if err != nil { s.jsonError(w, "DB_ERROR", "Failed to disable API key", err.Error(), 500); return }
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
	if apiKey == "" {
		setRateLimitHeaders(w.Header(), defaultRateLimitState())
		s.jsonError(w, "MISSING_API_KEY", "Missing X-API-Key header", "Include your API key in the X-API-Key header", 401)
		return
	}
	// rate limit and disabled check
	var disabled bool
	var perSec int
	apiKeyHash := sha256Hex(apiKey)
	err := s.pool.QueryRow(r.Context(), `SELECT disabled, rate_limit_per_sec FROM api_keys WHERE key_hash=$1`, apiKeyHash).Scan(&disabled, &perSec)
	if errors.Is(err, pgx.ErrNoRows) {
		log.Printf("deny unknown key: %s", maskKey(apiKey))
		setRateLimitHeaders(w.Header(), defaultRateLimitState())
		s.jsonError(w, "INVALID_API_KEY", "Unknown API key", "Check the X-API-Key header value, or ask an administrator for a key", 401)
		return
	}
	if err != nil {
		log.Printf("api key lookup failed: %v", err)
		s.jsonError(w, "DB_ERROR", "Could not verify the API key", "Transient server error; retry with backoff", http.StatusServiceUnavailable)
		return
	}
	if disabled {
		log.Printf("deny disabled key: %s", maskKey(apiKey))
		setRateLimitHeaders(w.Header(), rateLimitState{Limit: perSec, Remaining: 0, Reset: 0})
		s.jsonError(w, "API_KEY_DISABLED", "API key disabled", "Contact an administrator to re-enable your key", 403)
		return
	}
	allowed, rlState := s.ratelimit.Allow(apiKeyHash, perSec)
	if !allowed {
		log.Printf("429 rate limit for key %s", maskKey(apiKey))
		setRateLimitHeaders(w.Header(), rlState)
		w.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds(rlState)))
		s.jsonError(w, "RATE_LIMIT_EXCEEDED", "Rate limit exceeded", fmt.Sprintf("This key allows %d requests/second; retry after %d second(s) and back off exponentially", rlState.Limit, retryAfterSeconds(rlState)), 429)
		return
	}

	// bound body size for safety (configurable)
	if r.ContentLength > 0 && s.cfg.MaxProxyBodyBytes > 0 && r.ContentLength > s.cfg.MaxProxyBodyBytes {
		setRateLimitHeaders(w.Header(), rlState)
		s.jsonError(w, "REQUEST_TOO_LARGE", "Request body too large", fmt.Sprintf("Maximum allowed size is %d bytes", s.cfg.MaxProxyBodyBytes), http.StatusRequestEntityTooLarge); return
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
			setRateLimitHeaders(w.Header(), rlState)
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
	// The upstream call never reached GitHub (no usable donated token, DNS,
	// TLS, timeout). Report it as a JSON error rather than writing a zero
	// status, which would abort the connection.
	if status == 0 {
		setRateLimitHeaders(w.Header(), rlState)
		s.jsonError(w, "UPSTREAM_ERROR", "Could not reach the GitHub API", "The proxy could not complete the upstream request; retry with backoff", http.StatusBadGateway)
		s.afterRequest(r.Context(), apiKeyHash, r.Method, r.URL.Path, http.StatusBadGateway, false)
		return
	}
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
	setRateLimitHeaders(w.Header(), rlState)
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
	
	// Update cumulative stats - system level
	s.updateSystemStats(ctx, hit)
	
	// Update cumulative stats - per API key level
	if hit {
		_, _ = s.pool.Exec(ctx, `UPDATE api_keys SET last_used_at=now(), total_requests=total_requests+1, total_cached_requests=total_cached_requests+1 WHERE key_hash=$1`, apiKeyHash)
	} else {
		_, _ = s.pool.Exec(ctx, `UPDATE api_keys SET last_used_at=now(), total_requests=total_requests+1 WHERE key_hash=$1`, apiKeyHash)
	}
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

// updateSystemStats increments system-wide cumulative counters and handles daily reset
func (s *Server) updateSystemStats(ctx context.Context, hit bool) {
	// Use NYC Eastern Time for daily reset as requested
	loc, _ := time.LoadLocation("America/New_York")
	currentDate := time.Now().In(loc).Format("2006-01-02")
	
	if hit {
		// Increment both total requests and cached requests
		_, _ = s.pool.Exec(ctx, `
			INSERT INTO system_stats (id, total_requests, total_cached_requests, today_requests, today_date) 
			VALUES (1, 1, 1, 1, $1::date)
			ON CONFLICT (id) DO UPDATE SET
				total_requests = system_stats.total_requests + 1,
				total_cached_requests = system_stats.total_cached_requests + 1,
				today_requests = CASE 
					WHEN system_stats.today_date = $1::date THEN system_stats.today_requests + 1
					ELSE 1
				END,
				today_date = $1::date,
				updated_at = now()
		`, currentDate)
	} else {
		// Increment only total requests
		_, _ = s.pool.Exec(ctx, `
			INSERT INTO system_stats (id, total_requests, total_cached_requests, today_requests, today_date) 
			VALUES (1, 1, 0, 1, $1::date)
			ON CONFLICT (id) DO UPDATE SET
				total_requests = system_stats.total_requests + 1,
				today_requests = CASE 
					WHEN system_stats.today_date = $1::date THEN system_stats.today_requests + 1
					ELSE 1
				END,
				today_date = $1::date,
				updated_at = now()
		`, currentDate)
	}
}

func (s *Server) stats() map[string]any {
	ctx := context.Background()
	
	// Get cumulative stats from system_stats table
	var totalRequests, totalCached, todayRequests int64
	_ = s.pool.QueryRow(ctx, `
		SELECT total_requests, total_cached_requests, today_requests 
		FROM system_stats WHERE id = 1
	`).Scan(&totalRequests, &totalCached, &todayRequests)
	
	// Calculate cache hit rate from cumulative stats
	var hitPct float64
	if totalRequests > 0 {
		hitPct = float64(totalCached) * 100.0 / float64(totalRequests)
	}
	
	var activeDonated int64
	_ = s.pool.QueryRow(ctx, `SELECT count(*) FROM donated_tokens WHERE revoked=false`).Scan(&activeDonated)
	
	return map[string]any{
		"totalRequests": totalRequests,
		"cacheHitRate": fmt.Sprintf("%.1f%%", hitPct),
		"today": todayRequests,
		"activeTokens": activeDonated,
	}
}

func percent(a, b int64) float64 { if b==0 { return 0 }; return float64(a) * 100 / float64(b) }

func (s *Server) render(w http.ResponseWriter, name string, data any) {
	s.renderStatus(w, http.StatusOK, name, data)
}

func (s *Server) renderStatus(w http.ResponseWriter, status int, name string, data any) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
	// allow inline script in admin.html (current page uses inline <script>)
	w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src https: data:; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; connect-src 'self'")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := s.tmpl.ExecuteTemplate(w, name, data); err != nil { log.Printf("template %s failed: %v", name, err) }
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

// errorLink points an agent at somewhere it can recover from an error.
type errorLink struct {
	Rel         string `json:"rel"`
	Href        string `json:"href"`
	Description string `json:"description,omitempty"`
}

// errorBody is the single error envelope used by every JSON error response.
// It is mirrored by components/schemas/Error in openapi.json.
type errorBody struct {
	Error struct {
		Code             string      `json:"code"`
		Message          string      `json:"message"`
		Hint             string      `json:"hint,omitempty"`
		DocumentationURL string      `json:"documentation_url,omitempty"`
		Links            []errorLink `json:"links,omitempty"`
	} `json:"error"`
}

// jsonError writes a structured JSON error response
func (s *Server) jsonError(w http.ResponseWriter, code, message, hint string, status int) {
	s.jsonErrorLinks(w, code, message, hint, status, nil)
}

// jsonErrorLinks writes a structured JSON error response with recovery links.
func (s *Server) jsonErrorLinks(w http.ResponseWriter, code, message, hint string, status int, links []errorLink) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	r := errorBody{}
	r.Error.Code = code
	r.Error.Message = message
	r.Error.Hint = hint
	r.Error.DocumentationURL = s.docsURL()
	r.Error.Links = links
	_ = json.NewEncoder(w).Encode(r)
}

// Rate limit policy advertised to clients. The window is one second and the
// quota is the per-key rate_limit_per_sec column (defaultRateLimitPerSec when
// a key has not overridden it).
const (
	defaultRateLimitPerSec = 10
	rateLimitWindowSeconds = 1
	rateLimitPolicyName    = "default"
)

// rateLimitState is a snapshot of a key's quota, rendered into the RFC-style
// RateLimit response headers.
type rateLimitState struct {
	Limit     int // requests allowed per window
	Remaining int // requests still available right now
	Reset     int // seconds until the quota is fully replenished
}

// defaultRateLimitState describes the policy that applies to a request we
// could not attribute to a key (missing or unknown X-API-Key). No quota was
// consumed, so the full default allowance is reported.
func defaultRateLimitState() rateLimitState {
	return rateLimitState{Limit: defaultRateLimitPerSec, Remaining: defaultRateLimitPerSec, Reset: 0}
}

// setRateLimitHeaders writes both the widely deployed RateLimit-* triple and
// the current IETF draft RateLimit / RateLimit-Policy fields, so agents can
// self-throttle whichever convention they understand.
func setRateLimitHeaders(h http.Header, st rateLimitState) {
	if st.Limit <= 0 { return }
	if st.Remaining < 0 { st.Remaining = 0 }
	if st.Reset < 0 { st.Reset = 0 }
	h.Set("RateLimit-Limit", strconv.Itoa(st.Limit))
	h.Set("RateLimit-Remaining", strconv.Itoa(st.Remaining))
	h.Set("RateLimit-Reset", strconv.Itoa(st.Reset))
	h.Set("RateLimit-Policy", fmt.Sprintf("%q;q=%d;w=%d", rateLimitPolicyName, st.Limit, rateLimitWindowSeconds))
	h.Set("RateLimit", fmt.Sprintf("%q;r=%d;t=%d", rateLimitPolicyName, st.Remaining, st.Reset))
}

// retryAfterSeconds is the Retry-After value to pair with a 429.
func retryAfterSeconds(st rateLimitState) int {
	if st.Reset > 0 { return st.Reset }
	return rateLimitWindowSeconds
}

// rateLimitPolicyHeader advertises the API rate limit policy on every
// response. Live counters are added per request by serveProxy.
func (s *Server) rateLimitPolicyHeader(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("RateLimit-Policy", fmt.Sprintf("%q;q=%d;w=%d", rateLimitPolicyName, defaultRateLimitPerSec, rateLimitWindowSeconds))
		next.ServeHTTP(w, r)
	})
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

// Allow consumes one token for key and reports whether the request may
// proceed, along with the resulting quota snapshot.
func (rl *rateLimiter) Allow(key string, perSec int) (bool, rateLimitState) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	if perSec <= 0 { return false, rateLimitState{} } // do not create buckets for invalid/disabled keys
	b := rl.buckets[key]
	if b == nil { b = &bucket{capacity: perSec, tokens: float64(perSec), last: time.Now()}; rl.buckets[key] = b }
	// a key's configured limit can change while its bucket is alive
	if b.capacity != perSec {
		b.capacity = perSec
		if b.tokens > float64(perSec) { b.tokens = float64(perSec) }
	}
	// refill
	now := time.Now()
	dt := now.Sub(b.last).Seconds()
	b.last = now
	b.tokens += dt * float64(perSec)
	if b.tokens > float64(b.capacity) { b.tokens = float64(b.capacity) }
	allowed := b.tokens >= 1
	if allowed { b.tokens -= 1 }
	return allowed, b.state()
}

// state snapshots the bucket. Reset is how long until the bucket is full
// again, rounded up to whole seconds.
func (b *bucket) state() rateLimitState {
	remaining := int(math.Floor(b.tokens))
	if remaining < 0 { remaining = 0 }
	reset := 0
	if rate := float64(b.capacity); rate > 0 && b.tokens < rate {
		reset = int(math.Ceil((rate - b.tokens) / rate))
	}
	return rateLimitState{Limit: b.capacity, Remaining: remaining, Reset: reset}
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

//go:embed openapi.json
var openapiFS embed.FS

type upgrader struct{ websocket.Upgrader }
