package server

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func TestRateLimiterAllowReportsState(t *testing.T) {
	rl := newRateLimiter()

	allowed, st := rl.Allow("k", 3)
	if !allowed {
		t.Fatal("first request should be allowed")
	}
	if st.Limit != 3 {
		t.Errorf("Limit = %d, want 3", st.Limit)
	}
	if st.Remaining != 2 {
		t.Errorf("Remaining = %d, want 2", st.Remaining)
	}
	if st.Reset != 1 {
		t.Errorf("Reset = %d, want 1 (one window to refill)", st.Reset)
	}

	// Drain the bucket.
	var last rateLimitState
	for i := range 2 {
		if allowed, last = rl.Allow("k", 3); !allowed {
			t.Fatalf("request %d should still be allowed", i+2)
		}
	}
	if last.Remaining != 0 {
		t.Errorf("Remaining after draining = %d, want 0", last.Remaining)
	}

	allowed, st = rl.Allow("k", 3)
	if allowed {
		t.Fatal("request past the quota should be denied")
	}
	if st.Remaining != 0 {
		t.Errorf("Remaining on denial = %d, want 0", st.Remaining)
	}
	if st.Limit != 3 {
		t.Errorf("Limit on denial = %d, want 3", st.Limit)
	}
	if got := retryAfterSeconds(st); got < 1 {
		t.Errorf("retryAfterSeconds = %d, want >= 1", got)
	}
}

func TestRateLimiterRefills(t *testing.T) {
	rl := newRateLimiter()
	for range 2 {
		rl.Allow("k", 2)
	}
	if allowed, _ := rl.Allow("k", 2); allowed {
		t.Fatal("bucket should be empty")
	}
	time.Sleep(600 * time.Millisecond)
	if allowed, _ := rl.Allow("k", 2); !allowed {
		t.Fatal("bucket should have refilled")
	}
}

// A key whose configured limit changes must not keep advertising the old one.
func TestRateLimiterPicksUpNewLimit(t *testing.T) {
	rl := newRateLimiter()
	rl.Allow("k", 10)
	_, st := rl.Allow("k", 4)
	if st.Limit != 4 {
		t.Errorf("Limit = %d, want 4 after the key's quota changed", st.Limit)
	}
	if st.Remaining > 4 {
		t.Errorf("Remaining = %d, must not exceed the new limit", st.Remaining)
	}
}

func TestRateLimiterRejectsZeroQuota(t *testing.T) {
	rl := newRateLimiter()
	allowed, st := rl.Allow("k", 0)
	if allowed {
		t.Error("a key with no quota must not be allowed")
	}
	if st.Limit != 0 {
		t.Errorf("Limit = %d, want 0", st.Limit)
	}
}

func TestSetRateLimitHeaders(t *testing.T) {
	h := http.Header{}
	setRateLimitHeaders(h, rateLimitState{Limit: 10, Remaining: 9, Reset: 1})

	want := map[string]string{
		"RateLimit-Limit":     "10",
		"RateLimit-Remaining": "9",
		"RateLimit-Reset":     "1",
		"RateLimit-Policy":    `"default";q=10;w=1`,
		"RateLimit":           `"default";r=9;t=1`,
	}
	for k, v := range want {
		if got := h.Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
}

func TestSetRateLimitHeadersSkipsUnknownQuota(t *testing.T) {
	h := http.Header{}
	setRateLimitHeaders(h, rateLimitState{})
	if got := h.Get("RateLimit-Limit"); got != "" {
		t.Errorf("RateLimit-Limit = %q, want no header when the quota is unknown", got)
	}
}

// Every response, including static documents and 404s, advertises the policy
// so an agent can discover it without spending a request.
func TestRateLimitPolicyHeaderOnEveryResponse(t *testing.T) {
	s := &Server{}
	s.tmpl = template.Must(template.ParseFS(templatesFS, "templates/*.html"))
	router := s.routes()

	for _, path := range []string{"/openapi.json", "/llms.txt", "/robots.txt", "/sitemap.xml", "/no-such-path"} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		want := `"default";q=` + strconv.Itoa(defaultRateLimitPerSec) + `;w=1`
		if got := w.Header().Get("RateLimit-Policy"); got != want {
			t.Errorf("GET %s: RateLimit-Policy = %q, want %q", path, got, want)
		}
	}
}

// An unattributed request (no key) still gets the full header set describing
// the default policy, so an agent learns the limit before it has a key.
func TestDefaultRateLimitState(t *testing.T) {
	st := defaultRateLimitState()
	if st.Limit != defaultRateLimitPerSec {
		t.Errorf("Limit = %d, want %d", st.Limit, defaultRateLimitPerSec)
	}
	if st.Remaining != defaultRateLimitPerSec {
		t.Errorf("Remaining = %d, want %d (no quota consumed)", st.Remaining, defaultRateLimitPerSec)
	}

	h := http.Header{}
	setRateLimitHeaders(h, st)
	if h.Get("RateLimit-Limit") == "" || h.Get("RateLimit") == "" {
		t.Error("unauthenticated responses must still carry RateLimit headers")
	}
}
