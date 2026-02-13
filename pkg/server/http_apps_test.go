// Copyright 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockHTMLProvider implements AppHTMLProvider for testing.
type mockHTMLProvider struct {
	names []string
	html  map[string][]byte
}

func (m *mockHTMLProvider) AppNames() []string { return m.names }
func (m *mockHTMLProvider) AppHTML(name string) ([]byte, error) {
	h, ok := m.html[name]
	if !ok {
		return nil, fmt.Errorf("not found: %s", name)
	}
	return h, nil
}

func newTestProvider() *mockHTMLProvider {
	return &mockHTMLProvider{
		names: []string{"data-chart", "session-viewer"},
		html: map[string][]byte{
			"data-chart":     []byte(`<html><body>Data Chart App</body></html>`),
			"session-viewer": []byte(`<html><body>Session Viewer App</body></html>`),
		},
	}
}

func TestHandleApps_RedirectWithoutTrailingSlash(t *testing.T) {
	srv := &HTTPServer{appHTMLProvider: newTestProvider()}

	// Register routes on a mux to test the redirect handler
	mux := http.NewServeMux()
	mux.HandleFunc("/apps/", srv.handleApps)
	mux.HandleFunc("/apps", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/apps/", http.StatusMovedPermanently)
	})

	req := httptest.NewRequest("GET", "/apps", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusMovedPermanently, rr.Code)
	assert.Equal(t, "/apps/", rr.Header().Get("Location"))
}

func TestHandleAppsIndex(t *testing.T) {
	srv := &HTTPServer{appHTMLProvider: newTestProvider()}

	req := httptest.NewRequest("GET", "/apps/", nil)
	rr := httptest.NewRecorder()
	srv.handleApps(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "text/html; charset=utf-8", rr.Header().Get("Content-Type"))

	body := rr.Body.String()
	assert.Contains(t, body, "Loom Apps")
	assert.Contains(t, body, `href="/apps/data-chart"`)
	assert.Contains(t, body, `href="/apps/session-viewer"`)
	assert.Contains(t, body, "data-chart")
	assert.Contains(t, body, "session-viewer")
}

func TestHandleAppsIndex_Empty(t *testing.T) {
	srv := &HTTPServer{
		appHTMLProvider: &mockHTMLProvider{
			names: []string{},
			html:  map[string][]byte{},
		},
	}

	req := httptest.NewRequest("GET", "/apps/", nil)
	rr := httptest.NewRecorder()
	srv.handleApps(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Body.String(), "No apps available.")
}

func TestHandleAppHTML_Found(t *testing.T) {
	srv := &HTTPServer{appHTMLProvider: newTestProvider()}

	req := httptest.NewRequest("GET", "/apps/data-chart", nil)
	rr := httptest.NewRecorder()
	srv.handleApps(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, `<html><body>Data Chart App</body></html>`, rr.Body.String())
}

func TestHandleAppHTML_NotFound(t *testing.T) {
	srv := &HTTPServer{appHTMLProvider: newTestProvider()}

	req := httptest.NewRequest("GET", "/apps/nonexistent", nil)
	rr := httptest.NewRecorder()
	srv.handleApps(rr, req)

	assert.Equal(t, http.StatusNotFound, rr.Code)
	assert.Contains(t, rr.Body.String(), "App not found")
}

func TestHandleAppHTML_SecurityHeaders(t *testing.T) {
	srv := &HTTPServer{appHTMLProvider: newTestProvider()}

	req := httptest.NewRequest("GET", "/apps/data-chart", nil)
	rr := httptest.NewRecorder()
	srv.handleApps(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)

	headers := rr.Header()
	assert.Equal(t, "text/html; charset=utf-8", headers.Get("Content-Type"))
	assert.Equal(t, "nosniff", headers.Get("X-Content-Type-Options"))
	assert.Equal(t, "SAMEORIGIN", headers.Get("X-Frame-Options"))

	csp := headers.Get("Content-Security-Policy")
	assert.Contains(t, csp, "default-src 'self'")
	assert.Contains(t, csp, "script-src 'self' 'unsafe-inline' https://cdn.jsdelivr.net")
	assert.Contains(t, csp, "style-src 'self' 'unsafe-inline'")
	assert.Contains(t, csp, "img-src 'self' data:")
	assert.Contains(t, csp, "connect-src 'self'")
	assert.Contains(t, csp, "frame-ancestors 'self'")
}

func TestHandleAppHTML_AllApps(t *testing.T) {
	provider := newTestProvider()
	srv := &HTTPServer{appHTMLProvider: provider}

	tests := []struct {
		name         string
		path         string
		expectedCode int
		expectedBody string
	}{
		{
			name:         "data-chart app",
			path:         "/apps/data-chart",
			expectedCode: http.StatusOK,
			expectedBody: "Data Chart App",
		},
		{
			name:         "session-viewer app",
			path:         "/apps/session-viewer",
			expectedCode: http.StatusOK,
			expectedBody: "Session Viewer App",
		},
		{
			name:         "nonexistent app",
			path:         "/apps/does-not-exist",
			expectedCode: http.StatusNotFound,
			expectedBody: "App not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", tt.path, nil)
			rr := httptest.NewRecorder()
			srv.handleApps(rr, req)

			assert.Equal(t, tt.expectedCode, rr.Code)
			assert.Contains(t, rr.Body.String(), tt.expectedBody)
		})
	}
}

func TestSetAppHTMLProvider(t *testing.T) {
	srv := &HTTPServer{}
	assert.Nil(t, srv.appHTMLProvider)

	provider := newTestProvider()
	srv.SetAppHTMLProvider(provider)
	assert.Equal(t, provider, srv.appHTMLProvider)
}

func TestHandleApps_MethodNotAllowed(t *testing.T) {
	srv := &HTTPServer{appHTMLProvider: newTestProvider()}

	methods := []string{"POST", "PUT", "DELETE", "PATCH"}
	for _, method := range methods {
		t.Run(method, func(t *testing.T) {
			req := httptest.NewRequest(method, "/apps/", nil)
			rr := httptest.NewRecorder()
			srv.handleApps(rr, req)

			assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
			assert.Contains(t, rr.Body.String(), "Method not allowed")
		})
	}
}

func TestHandleApps_InvalidAppName(t *testing.T) {
	srv := &HTTPServer{appHTMLProvider: newTestProvider()}

	tests := []struct {
		name         string
		path         string
		expectedCode int
		expectedBody string
	}{
		{
			name:         "path traversal with dots",
			path:         "/apps/../../etc/passwd",
			expectedCode: http.StatusBadRequest,
			expectedBody: "Invalid app name",
		},
		{
			name:         "path with slashes",
			path:         "/apps/foo/bar",
			expectedCode: http.StatusBadRequest,
			expectedBody: "Invalid app name",
		},
		{
			name:         "path with dot",
			path:         "/apps/foo.bar",
			expectedCode: http.StatusBadRequest,
			expectedBody: "Invalid app name",
		},
		{
			name:         "path with percent encoding",
			path:         "/apps/foo%20bar",
			expectedCode: http.StatusBadRequest,
			expectedBody: "Invalid app name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", tt.path, nil)
			rr := httptest.NewRecorder()
			srv.handleApps(rr, req)

			assert.Equal(t, tt.expectedCode, rr.Code)
			assert.Contains(t, rr.Body.String(), tt.expectedBody)
		})
	}
}

func TestIsValidAppName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"simple name", "data-chart", true},
		{"underscores", "my_app", true},
		{"alphanumeric", "app123", true},
		{"mixed case", "MyApp", true},
		{"hyphen only", "a-b-c", true},
		{"empty string", "", false},
		{"path traversal", "../etc", false},
		{"slash", "foo/bar", false},
		{"space", "foo bar", false},
		{"dot", "foo.bar", false},
		{"angle bracket", "foo<bar>", false},
		{"semicolon", "foo;bar", false},
		{"percent", "foo%20bar", false},
		{"null byte", "foo\x00bar", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isValidAppName(tt.input))
		})
	}
}

func TestAppsRoutesNotRegistered_WhenProviderNil(t *testing.T) {
	// When no provider is set, /apps/ should fall through to the default mux
	// (which would be the grpc-gateway catch-all in production).
	// We test this by creating a mux that only registers the apps routes
	// conditionally, same as Start() does.
	srv := &HTTPServer{} // no provider set

	mux := http.NewServeMux()
	mux.HandleFunc("/fallback", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("fallback"))
	})

	// Conditionally register apps routes (same pattern as Start)
	if srv.appHTMLProvider != nil {
		mux.HandleFunc("/apps/", srv.handleApps)
		mux.HandleFunc("/apps", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/apps/", http.StatusMovedPermanently)
		})
	}

	// /apps/ should not be registered, expect 404 from the default mux
	req := httptest.NewRequest("GET", "/apps/", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusNotFound, rr.Code)
}
