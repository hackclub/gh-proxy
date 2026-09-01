package server

import (
	"encoding/json"
	"encoding/xml"
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gh-proxy/internal/config"
)

const testBaseURL = "https://gh-proxy.hackclub.com"

// newDiscoveryServer builds a Server wired for the routes that never touch the
// database (discovery documents, 404, 405).
func newDiscoveryServer(t *testing.T) *Server {
	t.Helper()
	s := &Server{cfg: config.Config{BaseURL: testBaseURL}}
	s.tmpl = template.Must(template.ParseFS(templatesFS, "templates/*.html"))
	return s
}

func do(t *testing.T, s *Server, method, path string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, nil)
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	return w
}

// A nonexistent path must be a real 404 with a body an agent can act on, not
// Go's bare "404 page not found" and never a 200 app shell.
func TestNotFoundMarkdown(t *testing.T) {
	s := newDiscoveryServer(t)
	w := do(t, s, http.MethodGet, "/some-path-that-does-not-exist", map[string]string{"Accept": "*/*"})

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/markdown") {
		t.Errorf("Content-Type = %q, want text/markdown", ct)
	}
	body := w.Body.String()
	if !strings.HasPrefix(body, "# 404 Not Found") {
		t.Errorf("body does not start with a markdown heading:\n%s", body)
	}
	for _, want := range []string{
		"/some-path-that-does-not-exist",
		testBaseURL + "/llms.txt",
		testBaseURL + "/openapi.json",
		testBaseURL + "/docs",
		testBaseURL + "/sitemap.xml",
		"/gh/graphql",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("404 markdown body missing %q:\n%s", want, body)
		}
	}
}

func TestNotFoundJSON(t *testing.T) {
	cases := []struct {
		name    string
		path    string
		headers map[string]string
	}{
		{"accept json", "/nope", map[string]string{"Accept": "application/json"}},
		{"json suffix", "/nope.json", nil},
		{"api prefix", "/api/v1/nope", nil},
		{"api key header", "/nope", map[string]string{"X-API-Key": "k"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newDiscoveryServer(t)
			w := do(t, s, http.MethodGet, tc.path, tc.headers)

			if w.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404", w.Code)
			}
			if ct := w.Header().Get("Content-Type"); ct != "application/json" {
				t.Fatalf("Content-Type = %q, want application/json", ct)
			}
			var body errorBody
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("body is not valid JSON: %v (%s)", err, w.Body.String())
			}
			if body.Error.Code != "NOT_FOUND" {
				t.Errorf("error.code = %q, want NOT_FOUND", body.Error.Code)
			}
			if body.Error.Hint == "" {
				t.Error("error.hint is empty; agents need a recovery hint")
			}
			if body.Error.DocumentationURL != testBaseURL+"/docs" {
				t.Errorf("documentation_url = %q", body.Error.DocumentationURL)
			}
			rels := map[string]string{}
			for _, l := range body.Error.Links {
				rels[l.Rel] = l.Href
			}
			for _, rel := range []string{"service-desc", "llms-txt", "documentation", "sitemap"} {
				if rels[rel] == "" {
					t.Errorf("error.links missing rel %q (got %v)", rel, rels)
				}
			}
			if got := rels["service-desc"]; got != testBaseURL+"/openapi.json" {
				t.Errorf("service-desc href = %q", got)
			}
		})
	}
}

func TestNotFoundHTMLForBrowsers(t *testing.T) {
	s := newDiscoveryServer(t)
	w := do(t, s, http.MethodGet, "/nope", map[string]string{
		"Accept": "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
	})

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("Content-Type = %q, want text/html", ct)
	}
	body := w.Body.String()
	for _, want := range []string{"404 Not Found", "/nope", "/llms.txt", "/openapi.json", "/docs"} {
		if !strings.Contains(body, want) {
			t.Errorf("404 page missing %q", want)
		}
	}
}

func TestMethodNotAllowed(t *testing.T) {
	s := newDiscoveryServer(t)

	w := do(t, s, http.MethodDelete, "/docs", map[string]string{"Accept": "application/json"})
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", w.Code)
	}
	var body errorBody
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not valid JSON: %v (%s)", err, w.Body.String())
	}
	if body.Error.Code != "METHOD_NOT_ALLOWED" {
		t.Errorf("error.code = %q, want METHOD_NOT_ALLOWED", body.Error.Code)
	}

	w = do(t, s, http.MethodDelete, "/docs", map[string]string{"Accept": "*/*"})
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/markdown") {
		t.Errorf("Content-Type = %q, want text/markdown", ct)
	}
}

func TestLLMsTxt(t *testing.T) {
	s := newDiscoveryServer(t)
	w := do(t, s, http.MethodGet, "/llms.txt", nil)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain", ct)
	}
	body := w.Body.String()
	// llmstxt.org: H1 title, then a blockquote summary, then link sections.
	lines := strings.Split(body, "\n")
	if !strings.HasPrefix(lines[0], "# ") {
		t.Errorf("first line = %q, want an H1 title", lines[0])
	}
	if !strings.Contains(body, "\n> ") {
		t.Error("llms.txt missing the blockquote summary required by llmstxt.org")
	}
	for _, want := range []string{
		"## Docs",
		"](" + testBaseURL + "/docs)",
		"](" + testBaseURL + "/openapi.json)",
		"X-API-Key",
		"RateLimit",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("llms.txt missing %q:\n%s", want, body)
		}
	}
}

func TestRobotsTxt(t *testing.T) {
	s := newDiscoveryServer(t)
	w := do(t, s, http.MethodGet, "/robots.txt", nil)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{"User-agent: *", "Disallow: /admin", "Sitemap: " + testBaseURL + "/sitemap.xml"} {
		if !strings.Contains(body, want) {
			t.Errorf("robots.txt missing %q:\n%s", want, body)
		}
	}
}

func TestSitemap(t *testing.T) {
	s := newDiscoveryServer(t)
	w := do(t, s, http.MethodGet, "/sitemap.xml", nil)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/xml") {
		t.Errorf("Content-Type = %q, want application/xml", ct)
	}
	var doc struct {
		URLs []struct {
			Loc string `xml:"loc"`
		} `xml:"url"`
	}
	if err := xml.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatalf("sitemap is not valid XML: %v (%s)", err, w.Body.String())
	}
	got := map[string]bool{}
	for _, u := range doc.URLs {
		got[u.Loc] = true
	}
	for _, want := range []string{testBaseURL + "/", testBaseURL + "/docs", testBaseURL + "/openapi.json", testBaseURL + "/llms.txt"} {
		if !got[want] {
			t.Errorf("sitemap missing %q (got %v)", want, got)
		}
	}
}

// Without BASE_URL configured the links must still be absolute, derived from
// the request.
func TestBaseURLFallsBackToRequest(t *testing.T) {
	s := &Server{}
	s.tmpl = template.Must(template.ParseFS(templatesFS, "templates/*.html"))
	r := httptest.NewRequest(http.MethodGet, "http://example.test/nope", nil)
	r.Header.Set("X-Forwarded-Proto", "https")
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)

	if !strings.Contains(w.Body.String(), "https://example.test/llms.txt") {
		t.Errorf("404 body should link to the requested host:\n%s", w.Body.String())
	}
}

func TestWantsJSON(t *testing.T) {
	cases := []struct {
		path   string
		accept string
		key    string
		want   bool
	}{
		{"/nope", "*/*", "", false},
		{"/nope", "text/html,*/*;q=0.8", "", false},
		{"/nope", "application/json", "", true},
		{"/nope", "application/vnd.api+json", "", true},
		{"/gh/user", "*/*", "", true},
		{"/admin/keys.json", "*/*", "", true},
		{"/api/anything", "*/*", "", true},
		{"/thing.json", "*/*", "", true},
		{"/nope", "*/*", "abc", true},
	}
	for _, tc := range cases {
		r := httptest.NewRequest(http.MethodGet, tc.path, nil)
		r.Header.Set("Accept", tc.accept)
		if tc.key != "" {
			r.Header.Set("X-API-Key", tc.key)
		}
		if got := wantsJSON(r); got != tc.want {
			t.Errorf("wantsJSON(%s, Accept=%q, key=%q) = %v, want %v", tc.path, tc.accept, tc.key, got, tc.want)
		}
	}
}

// The 404 must never be a 200 with the app shell.
func TestKnownRoutesAreNot404(t *testing.T) {
	s := newDiscoveryServer(t)
	for _, path := range []string{"/openapi.json", "/llms.txt", "/robots.txt", "/sitemap.xml"} {
		w := do(t, s, http.MethodGet, path, nil)
		if w.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, w.Code)
		}
	}
}
