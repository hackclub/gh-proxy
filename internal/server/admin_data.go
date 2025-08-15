package server

import (
	"encoding/json"
	"net/http"
	"time"
)

// Expose API keys and usage for admin UI
func (s *Server) handleAdminKeysJSON(w http.ResponseWriter, r *http.Request) {
	rows, err := s.pool.Query(r.Context(), `
SELECT k.key,
       COALESCE((SELECT count(*) FROM request_logs rl WHERE rl.api_key=k.key),0) AS total,
       COALESCE((SELECT AVG(CASE WHEN rl.cache_hit THEN 1 ELSE 0 END) * 100 FROM request_logs rl WHERE rl.api_key=k.key),0) AS hit_rate,
       k.last_used_at,
       k.disabled
FROM api_keys k
ORDER BY k.created_at DESC`)
	if err != nil { http.Error(w, err.Error(), 500); return }
	defer rows.Close()
	type row struct { Key string `json:"key"`; Total int64 `json:"total"`; HitRate float64 `json:"hit_rate"`; LastUsed *time.Time `json:"last_used"`; Disabled bool `json:"disabled"` }
	var out []row
	for rows.Next() { var rr row; if err := rows.Scan(&rr.Key, &rr.Total, &rr.HitRate, &rr.LastUsed, &rr.Disabled); err!=nil { http.Error(w, err.Error(), 500); return }; out = append(out, rr) }
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// 7-day per-key daily usage
func (s *Server) handleAdminKeysUsageJSON(w http.ResponseWriter, r *http.Request) {
	rows, err := s.pool.Query(r.Context(), `
WITH days AS (
  SELECT generate_series((now()::date - INTERVAL '6 day'), now()::date, INTERVAL '1 day')::date AS d
)
SELECT k.key,
       d.d AS day,
       COALESCE(count(rl.id),0) AS c
FROM api_keys k
CROSS JOIN days d
LEFT JOIN request_logs rl ON rl.api_key=k.key AND rl.created_at::date = d.d
GROUP BY k.key, d.d
ORDER BY k.key, d.d;
`)
	if err != nil { http.Error(w, err.Error(), 500); return }
	defer rows.Close()
	type point struct{ Day string `json:"day"`; C int64 `json:"c"` }
	m := map[string][]point{}
	for rows.Next() {
		var key string
		var day time.Time
		var c int64
		if err := rows.Scan(&key, &day, &c); err != nil { http.Error(w, err.Error(), 500); return }
		m[key] = append(m[key], point{Day: day.Format("2006-01-02"), C: c})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(m)
}

// Last N recent requests (default: 1000)
func (s *Server) handleAdminRecentJSON(w http.ResponseWriter, r *http.Request) {
	rows, err := s.pool.Query(r.Context(), `
SELECT method, path, status, created_at
FROM request_logs
ORDER BY id DESC
LIMIT 1000`)
	if err != nil { http.Error(w, err.Error(), 500); return }
	defer rows.Close()
	type row struct{ Method string `json:"method"`; Path string `json:"path"`; Status int `json:"status"`; CreatedAt time.Time `json:"created_at"` }
	var out []row
	for rows.Next() { var rr row; if err := rows.Scan(&rr.Method, &rr.Path, &rr.Status, &rr.CreatedAt); err!=nil { http.Error(w, err.Error(), 500); return }; out = append(out, rr) }
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}
