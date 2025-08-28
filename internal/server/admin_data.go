package server

import (
	"encoding/json"
	"net/http"
	"time"
)

// Expose API keys and usage for admin UI
func (s *Server) handleAdminKeysJSON(w http.ResponseWriter, r *http.Request) {
	rows, err := s.pool.Query(r.Context(), `
SELECT k.id::text,
       k.hc_username,
       k.app_name,
       k.machine,
       COALESCE(k.key_hint, '') as key_hint,
       k.total_requests AS total,
       CASE WHEN k.total_requests > 0 THEN (k.total_cached_requests::float / k.total_requests::float) * 100 ELSE 0 END AS hit_rate,
       k.last_used_at,
       k.disabled
FROM api_keys k
ORDER BY k.created_at DESC`)
	if err != nil { http.Error(w, err.Error(), 500); return }
	defer rows.Close()
	type row struct {
		ID string `json:"id"`
		Display string `json:"display"`
		Total int64 `json:"total"`
		HitRate float64 `json:"hit_rate"`
		LastUsed *time.Time `json:"last_used"`
		Disabled bool `json:"disabled"`
	}
	var out []row
	for rows.Next() {
		var id, hc, app, machine, hint string
		var total int64
		var hitRate float64
		var lastUsed *time.Time
		var disabled bool
		if err := rows.Scan(&id, &hc, &app, &machine, &hint, &total, &hitRate, &lastUsed, &disabled); err!=nil { http.Error(w, err.Error(), 500); return }
		out = append(out, row{ID: id, Display: formatKeyDisplay(hc, app, machine, hint), Total: total, HitRate: hitRate, LastUsed: lastUsed, Disabled: disabled})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// 7-day per-key daily usage
func (s *Server) handleAdminKeysUsageJSON(w http.ResponseWriter, r *http.Request) {
	rows, err := s.pool.Query(r.Context(), `
WITH days AS (
  SELECT generate_series((now()::date - INTERVAL '6 day'), now()::date, INTERVAL '1 day')::date AS d
)
SELECT k.id::text,
       d.d AS day,
       COALESCE(count(rl.id),0) AS c
FROM api_keys k
CROSS JOIN days d
LEFT JOIN request_logs rl ON rl.api_key=k.key_hash AND rl.created_at::date = d.d
GROUP BY k.id, d.d
ORDER BY k.id, d.d;
`)
	if err != nil { http.Error(w, err.Error(), 500); return }
	defer rows.Close()
	type point struct{ Day string `json:"day"`; C int64 `json:"c"` }
	m := map[string][]point{}
	for rows.Next() {
		var id string
		var day time.Time
		var c int64
		if err := rows.Scan(&id, &day, &c); err != nil { http.Error(w, err.Error(), 500); return }
		m[id] = append(m[id], point{Day: day.Format("2006-01-02"), C: c})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(m)
}

// Last N recent requests (default: 1000)
func (s *Server) handleAdminRecentJSON(w http.ResponseWriter, r *http.Request) {
	rows, err := s.pool.Query(r.Context(), `
SELECT rl.method,
       rl.path,
       rl.status,
       rl.created_at,
       COALESCE(ak.hc_username||'_'||ak.app_name||'_'||ak.machine||CASE WHEN COALESCE(ak.key_hint,'')<>'' THEN '_'||ak.key_hint ELSE '' END,'') AS display
FROM request_logs rl
LEFT JOIN api_keys ak ON ak.key_hash = rl.api_key
ORDER BY rl.id DESC
LIMIT 1000`)
	if err != nil { http.Error(w, err.Error(), 500); return }
	defer rows.Close()
	type row struct{ Method string `json:"method"`; Path string `json:"path"`; Status int `json:"status"`; CreatedAt time.Time `json:"created_at"`; Display string `json:"display"` }
	var out []row
	for rows.Next() { var rr row; if err := rows.Scan(&rr.Method, &rr.Path, &rr.Status, &rr.CreatedAt, &rr.Display); err!=nil { http.Error(w, err.Error(), 500); return }; out = append(out, rr) }
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}
