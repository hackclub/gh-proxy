package server

import (
	"encoding/json"
	"html/template"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"gh-proxy/internal/config"
)

// Test helpers
func setupTestDB(t *testing.T) (*pgxpool.Pool, func()) {
	// Use in-memory SQLite or skip if no test database available
	t.Skip("Test database not configured")
	return nil, func() {}
}

func TestFormatNumber(t *testing.T) {
	tests := map[int64]string{
		0:        "0",
		999:      "999",
		1000:     "1,000",
		21558981: "21,558,981",
	}
	for input, want := range tests {
		if got := formatNumber(input); got != want {
			t.Errorf("formatNumber(%d) = %q, want %q", input, got, want)
		}
	}
}

func TestOpenAPIEndpoint(t *testing.T) {
	// Check if OpenAPI spec file exists
	specPath := "openapi.json"
	if _, err := os.Stat(specPath); os.IsNotExist(err) {
		t.Fatalf("OpenAPI spec file not found at %s", specPath)
	}

	// Validate JSON structure
	spec, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("Failed to read OpenAPI spec: %v", err)
	}

	var specMap map[string]interface{}
	if err := json.Unmarshal(spec, &specMap); err != nil {
		t.Fatalf("Invalid JSON in OpenAPI spec: %v", err)
	}

	// Check required fields
	if specMap["openapi"] != "3.0.3" {
		t.Error("OpenAPI version should be 3.0.3")
	}

	// Check paths exist
	paths, ok := specMap["paths"].(map[string]interface{})
	if !ok {
		t.Fatal("OpenAPI spec missing paths")
	}

	requiredPaths := []string{"/openapi.json", "/gh/{path}", "/gh/graphql"}
	for _, path := range requiredPaths {
		if _, exists := paths[path]; !exists {
			t.Errorf("Required path %s not in OpenAPI spec", path)
		}
	}

	// Check components/schemas/Error exists
	components, ok := specMap["components"].(map[string]interface{})
	if !ok {
		t.Fatal("OpenAPI spec missing components")
	}
	schemas, ok := components["schemas"].(map[string]interface{})
	if !ok {
		t.Fatal("OpenAPI spec missing schemas")
	}
	if _, exists := schemas["Error"]; !exists {
		t.Error("Error schema not in OpenAPI spec")
	}
}

func TestJSONErrorHelper(t *testing.T) {
	tests := []struct {
		name       string
		code       string
		message    string
		hint       string
		status     int
		wantFields []string
	}{
		{
			name:       "missing_api_key",
			code:       "MISSING_API_KEY",
			message:    "Missing X-API-Key header",
			hint:       "Include your API key in the X-API-Key header",
			status:     401,
			wantFields: []string{"error", "code", "message", "hint"},
		},
		{
			name:       "rate_limit",
			code:       "RATE_LIMIT_EXCEEDED",
			message:    "Rate limit exceeded",
			hint:       "Implement exponential backoff",
			status:     429,
			wantFields: []string{"error", "code", "message", "hint"},
		},
		{
			name:       "api_key_disabled",
			code:       "API_KEY_DISABLED",
			message:    "API key disabled",
			hint:       "Contact administrator to re-enable",
			status:     403,
			wantFields: []string{"error", "code", "message", "hint"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()

			s := &Server{}
			s.jsonError(w, tt.code, tt.message, tt.hint, tt.status)

			resp := w.Result()
			defer resp.Body.Close()

			if resp.StatusCode != tt.status {
				t.Errorf("Status = %d, want %d", resp.StatusCode, tt.status)
			}

			contentType := resp.Header.Get("Content-Type")
			if contentType != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", contentType)
			}

			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("Failed to read response body: %v", err)
			}

			var result map[string]interface{}
			if err := json.Unmarshal(body, &result); err != nil {
				t.Fatalf("Response body is not valid JSON: %v", err)
			}

			// Check error object exists
			if errMsg, ok := result["error"].(map[string]interface{}); ok {
				if code, exists := errMsg["code"]; !exists || code != tt.code {
					t.Errorf("Error code = %v, want %s", code, tt.code)
				}
				if msg, exists := errMsg["message"]; !exists || msg != tt.message {
					t.Errorf("Error message = %v, want %s", msg, tt.message)
				}
				if hint, exists := errMsg["hint"]; exists && hint != tt.hint {
					t.Errorf("Error hint = %v, want %s", hint, tt.hint)
				}
			} else {
				t.Error("Response missing error object")
			}
		})
	}
}

func TestOpenAPIEndpointIntegration(t *testing.T) {
	// This test requires a running server - skip for now
	// The OpenAPI spec is validated by TestOpenAPIEndpoint
	t.Skip("Integration test requires running server")
}

func TestHandleOpenAPI(t *testing.T) {
	// The handler must serve the embedded spec regardless of the working
	// directory (production runs from a binary-only container with no source tree).
	s := &Server{}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)

	s.handleOpenAPI(w, r)

	resp := w.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Status = %d, want 200 (body: %s)", resp.StatusCode, w.Body.String())
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var spec map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &spec); err != nil {
		t.Fatalf("Response is not valid JSON: %v", err)
	}
	if spec["openapi"] != "3.0.3" {
		t.Errorf("openapi = %v, want 3.0.3", spec["openapi"])
	}
}

// TestErrorCodes validates all expected error codes are defined
func TestErrorCodes(t *testing.T) {
	expectedCodes := []string{
		"MISSING_API_KEY",
		"INVALID_API_KEY",
		"API_KEY_DISABLED",
		"RATE_LIMIT_EXCEEDED",
		"REQUEST_TOO_LARGE",
		"INTERNAL_ERROR",
		"INVALID_REQUEST",
		"CSRF_FAILED",
		"DB_ERROR",
	}

	spec, err := os.ReadFile("openapi.json")
	if err != nil {
		t.Fatalf("Failed to read OpenAPI spec: %v", err)
	}

	var specMap map[string]interface{}
	json.Unmarshal(spec, &specMap)

	components := specMap["components"].(map[string]interface{})
	schemas := components["schemas"].(map[string]interface{})
	errorSchema := schemas["Error"].(map[string]interface{})
	properties := errorSchema["properties"].(map[string]interface{})
	errorObj := properties["error"].(map[string]interface{})
	errorProps := errorObj["properties"].(map[string]interface{})
	codeEnum := errorProps["code"].(map[string]interface{})
	enumValues := codeEnum["enum"].([]interface{})

	var foundCodes []string
	for _, v := range enumValues {
		foundCodes = append(foundCodes, v.(string))
	}

	for _, code := range expectedCodes {
		found := false
		for _, c := range foundCodes {
			if c == code {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Error code %q not in OpenAPI enum", code)
		}
	}
}

// TestProxyMissingAPIKey covers the one proxy branch that returns before
// touching the database: it must be a JSON error carrying rate limit headers.
func TestProxyMissingAPIKey(t *testing.T) {
	s := &Server{cfg: config.Config{BaseURL: testBaseURL}}
	s.tmpl = template.Must(template.ParseFS(templatesFS, "templates/*.html"))

	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/gh/user", nil))

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	for _, h := range []string{"RateLimit", "RateLimit-Policy", "RateLimit-Limit", "RateLimit-Remaining", "RateLimit-Reset"} {
		if w.Header().Get(h) == "" {
			t.Errorf("missing %s header on 401", h)
		}
	}
	var body errorBody
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not valid JSON: %v (%s)", err, w.Body.String())
	}
	if body.Error.Code != "MISSING_API_KEY" {
		t.Errorf("error.code = %q, want MISSING_API_KEY", body.Error.Code)
	}
	if body.Error.Hint == "" {
		t.Error("error.hint is empty")
	}
	if body.Error.DocumentationURL != testBaseURL+"/docs" {
		t.Errorf("documentation_url = %q", body.Error.DocumentationURL)
	}
}

// Every JSON error carries a documentation_url so an agent can find the codes.
func TestJSONErrorIncludesDocumentationURL(t *testing.T) {
	s := &Server{cfg: config.Config{BaseURL: testBaseURL}}
	w := httptest.NewRecorder()
	s.jsonError(w, "INTERNAL_ERROR", "boom", "retry", 500)

	var body errorBody
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}
	if body.Error.DocumentationURL != testBaseURL+"/docs" {
		t.Errorf("documentation_url = %q, want %s/docs", body.Error.DocumentationURL, testBaseURL)
	}
	if len(body.Error.Links) != 0 {
		t.Errorf("links = %v, want none for a plain error", body.Error.Links)
	}
	if got := w.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
}

// The spec must describe the discovery documents, the error codes the server
// can actually emit, and the rate limit headers it sends.
func TestOpenAPIDocumentsAgentContract(t *testing.T) {
	spec, err := os.ReadFile("openapi.json")
	if err != nil {
		t.Fatalf("Failed to read OpenAPI spec: %v", err)
	}
	var specMap map[string]interface{}
	if err := json.Unmarshal(spec, &specMap); err != nil {
		t.Fatalf("Invalid JSON in OpenAPI spec: %v", err)
	}

	paths := specMap["paths"].(map[string]interface{})
	for _, p := range []string{"/", "/docs", "/llms.txt", "/robots.txt", "/sitemap.xml"} {
		if _, ok := paths[p]; !ok {
			t.Errorf("discovery path %s not documented in the OpenAPI spec", p)
		}
	}

	components := specMap["components"].(map[string]interface{})

	headers, ok := components["headers"].(map[string]interface{})
	if !ok {
		t.Fatal("OpenAPI spec missing components/headers")
	}
	for _, h := range []string{"RateLimit", "RateLimit-Policy", "RateLimit-Limit", "RateLimit-Remaining", "RateLimit-Reset", "Retry-After"} {
		if _, ok := headers[h]; !ok {
			t.Errorf("rate limit header %s not documented", h)
		}
	}

	// The 429 response must document Retry-After.
	responses := components["responses"].(map[string]interface{})
	tooMany, ok := responses["TooManyRequests"].(map[string]interface{})
	if !ok {
		t.Fatal("OpenAPI spec missing the TooManyRequests response")
	}
	if hdrs, ok := tooMany["headers"].(map[string]interface{}); !ok {
		t.Error("TooManyRequests response documents no headers")
	} else if _, ok := hdrs["Retry-After"]; !ok {
		t.Error("TooManyRequests response does not document Retry-After")
	}

	// The proxy operations must reference the JSON error responses.
	get := paths["/gh/{path}"].(map[string]interface{})["get"].(map[string]interface{})
	getResponses := get["responses"].(map[string]interface{})
	for _, status := range []string{"401", "403", "413", "429", "502", "503"} {
		if _, ok := getResponses[status]; !ok {
			t.Errorf("GET /gh/{path} does not document a %s response", status)
		}
	}

	// The Error schema must expose the recovery fields agents rely on.
	errProps := components["schemas"].(map[string]interface{})["Error"].(map[string]interface{})["properties"].(map[string]interface{})["error"].(map[string]interface{})["properties"].(map[string]interface{})
	for _, f := range []string{"code", "message", "hint", "documentation_url", "links"} {
		if _, ok := errProps[f]; !ok {
			t.Errorf("Error schema missing %q", f)
		}
	}
	enum := errProps["code"].(map[string]interface{})["enum"].([]interface{})
	have := map[string]bool{}
	for _, v := range enum {
		have[v.(string)] = true
	}
	for _, code := range []string{"NOT_FOUND", "METHOD_NOT_ALLOWED", "MISSING_FIELDS", "UNAUTHORIZED", "UPSTREAM_ERROR"} {
		if !have[code] {
			t.Errorf("error code %q not in the OpenAPI enum", code)
		}
	}
}
