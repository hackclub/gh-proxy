package server

import (
	"fmt"
	"net/http"
	"strings"
)

// entryPoint is one machine-readable place an agent can go next. The list is
// rendered into the 404 markdown body, the 404 JSON body, /llms.txt and
// /sitemap.xml so all four stay in sync.
type entryPoint struct {
	Path    string // path on this host
	Rel     string // link relation used in JSON error bodies
	Desc    string
	Sitemap bool // include in sitemap.xml (crawlable HTML/text documents only)
}

var entryPoints = []entryPoint{
	{Path: "/", Rel: "home", Desc: "Project home page", Sitemap: true},
	{Path: "/docs", Rel: "documentation", Desc: "API documentation, examples and error reference", Sitemap: true},
	{Path: "/openapi.json", Rel: "service-desc", Desc: "OpenAPI 3.0.3 specification for the whole API", Sitemap: true},
	{Path: "/llms.txt", Rel: "llms-txt", Desc: "Machine-readable site map for agents (llmstxt.org)", Sitemap: true},
	{Path: "/sitemap.xml", Rel: "sitemap", Desc: "XML sitemap"},
}

// baseURL returns the absolute origin for this deployment, falling back to the
// request when BASE_URL is unset (tests, local runs behind a proxy).
func (s *Server) baseURL(r *http.Request) string {
	if s.cfg.BaseURL != "" {
		return strings.TrimRight(s.cfg.BaseURL, "/")
	}
	scheme := "http"
	if r != nil {
		if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
			scheme = "https"
		}
		if r.Host != "" {
			return scheme + "://" + r.Host
		}
	}
	return ""
}

// docsURL is the absolute (or root-relative) documentation URL embedded in
// every JSON error body.
func (s *Server) docsURL() string {
	if s.cfg.BaseURL != "" {
		return strings.TrimRight(s.cfg.BaseURL, "/") + "/docs"
	}
	return "/docs"
}

// wantsJSON reports whether the client is an API client that should receive a
// structured JSON error rather than a human-readable page.
func wantsJSON(r *http.Request) bool {
	accept := strings.ToLower(r.Header.Get("Accept"))
	if strings.Contains(accept, "application/json") || strings.Contains(accept, "+json") {
		return true
	}
	p := r.URL.Path
	if strings.HasPrefix(p, "/gh/") || p == "/gh" ||
		strings.HasPrefix(p, "/admin/") ||
		strings.HasPrefix(p, "/api/") ||
		strings.HasSuffix(p, ".json") {
		return true
	}
	// Explicit API-client signals.
	if r.Header.Get("X-API-Key") != "" {
		return true
	}
	return false
}

// wantsHTML reports whether the client is a browser asking for a page.
func wantsHTML(r *http.Request) bool {
	return strings.Contains(strings.ToLower(r.Header.Get("Accept")), "text/html")
}

// notFoundMarkdown is the short recovery document agents get for a missing
// path. Keep it short: it exists so an agent can pick its next request.
func (s *Server) notFoundMarkdown(r *http.Request, title, detail string) string {
	base := s.baseURL(r)
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n%s\n\n## Where to look next\n\n", title, detail)
	for _, e := range entryPoints {
		fmt.Fprintf(&b, "- [%s](%s%s) — %s\n", e.Path, base, e.Path, e.Desc)
	}
	b.WriteString("\n## API endpoints\n\n")
	fmt.Fprintf(&b, "- `GET %s/gh/{github-rest-path}` — GitHub REST proxy (requires an `X-API-Key` header)\n", base)
	fmt.Fprintf(&b, "- `POST %s/gh/graphql` — GitHub GraphQL proxy (requires an `X-API-Key` header)\n", base)
	b.WriteString("\nErrors are returned as JSON (`{\"error\":{\"code\",\"message\",\"hint\"}}`) for API paths or when the request sends `Accept: application/json`.\n")
	return b.String()
}

// notFoundLinks are the JSON link objects that accompany a 404/405 error body.
func (s *Server) notFoundLinks(r *http.Request) []errorLink {
	base := s.baseURL(r)
	links := make([]errorLink, 0, len(entryPoints))
	for _, e := range entryPoints {
		links = append(links, errorLink{Rel: e.Rel, Href: base + e.Path, Description: e.Desc})
	}
	return links
}

// handleNotFound serves a 404 that every kind of client can act on: JSON for
// API clients, HTML for browsers, markdown for everything else (curl, agents).
func (s *Server) handleNotFound(w http.ResponseWriter, r *http.Request) {
	detail := fmt.Sprintf("`%s` is not a route on this server.", r.URL.Path)
	switch {
	case wantsJSON(r):
		s.jsonErrorLinks(w, "NOT_FOUND", "No such path: "+r.URL.Path,
			"Check /openapi.json for the list of valid paths, or /llms.txt for a site map.",
			http.StatusNotFound, s.notFoundLinks(r))
	case wantsHTML(r) && s.tmpl != nil:
		s.renderStatus(w, http.StatusNotFound, "404.html", map[string]any{
			"Path":        r.URL.Path,
			"EntryPoints": entryPoints,
		})
	default:
		writeMarkdown(w, http.StatusNotFound, s.notFoundMarkdown(r, "404 Not Found", detail))
	}
}

// handleMethodNotAllowed mirrors handleNotFound for the 405 case.
func (s *Server) handleMethodNotAllowed(w http.ResponseWriter, r *http.Request) {
	msg := fmt.Sprintf("Method %s is not allowed on %s", r.Method, r.URL.Path)
	if wantsJSON(r) {
		s.jsonErrorLinks(w, "METHOD_NOT_ALLOWED", msg,
			"See /openapi.json for the methods each path accepts.",
			http.StatusMethodNotAllowed, s.notFoundLinks(r))
		return
	}
	writeMarkdown(w, http.StatusMethodNotAllowed,
		s.notFoundMarkdown(r, "405 Method Not Allowed", "`"+msg+"`."))
}

func writeMarkdown(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

// handleLLMsTxt serves /llms.txt in the llmstxt.org format.
func (s *Server) handleLLMsTxt(w http.ResponseWriter, r *http.Request) {
	base := s.baseURL(r)
	var b strings.Builder
	b.WriteString("# gh-proxy\n\n")
	b.WriteString("> A Hack Club GitHub API proxy. It forwards REST and GraphQL calls to api.github.com using a pool of donated GitHub tokens, caches responses, and rate limits each API key. Every proxied call needs an `X-API-Key` header.\n\n")
	b.WriteString("Errors are JSON: `{\"error\":{\"code\":\"...\",\"message\":\"...\",\"hint\":\"...\"}}`. Rate limit state is returned on every `/gh/` response in the `RateLimit`, `RateLimit-Policy`, `RateLimit-Limit`, `RateLimit-Remaining` and `RateLimit-Reset` headers, plus `Retry-After` on a 429.\n\n")

	b.WriteString("## Docs\n\n")
	fmt.Fprintf(&b, "- [API documentation](%s/docs): endpoints, authentication, caching, error codes and rate limit headers\n", base)
	fmt.Fprintf(&b, "- [OpenAPI specification](%s/openapi.json): OpenAPI 3.0.3 description of every endpoint and error shape\n", base)

	b.WriteString("\n## API\n\n")
	fmt.Fprintf(&b, "- [GitHub REST proxy](%s/gh/): `GET|POST|PATCH|PUT|DELETE %s/gh/{github-rest-path}` mirrors `https://api.github.com/{github-rest-path}`\n", base, base)
	fmt.Fprintf(&b, "- [GitHub GraphQL proxy](%s/gh/graphql): `POST` a `{\"query\":\"...\"}` body, mirrors `https://api.github.com/graphql`\n", base)

	b.WriteString("\n## Optional\n\n")
	fmt.Fprintf(&b, "- [Home](%s/): what the project is and how to donate a GitHub token\n", base)
	fmt.Fprintf(&b, "- [Sitemap](%s/sitemap.xml): XML sitemap\n", base)
	fmt.Fprintf(&b, "- [Admin panel](%s/admin): usage dashboard, password protected\n", base)

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(b.String()))
}

// handleRobotsTxt serves /robots.txt, pointing crawlers at the sitemap and
// keeping them out of authenticated paths.
func (s *Server) handleRobotsTxt(w http.ResponseWriter, r *http.Request) {
	body := "User-agent: *\n" +
		"Allow: /\n" +
		"Disallow: /admin\n" +
		"Disallow: /auth/\n" +
		"Disallow: /gh/\n" +
		"\n" +
		"Sitemap: " + s.baseURL(r) + "/sitemap.xml\n"
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
}

// handleSitemap serves a minimal sitemaps.org 0.9 XML sitemap.
func (s *Server) handleSitemap(w http.ResponseWriter, r *http.Request) {
	base := s.baseURL(r)
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">` + "\n")
	for _, e := range entryPoints {
		if !e.Sitemap {
			continue
		}
		fmt.Fprintf(&b, "  <url><loc>%s%s</loc></url>\n", base, e.Path)
	}
	b.WriteString("</urlset>\n")
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(b.String()))
}
