// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestCORSMiddleware(t *testing.T) {
	tests := []struct {
		name               string
		corsConfig         CORSConfig
		requestOrigin      string
		requestMethod      string
		expectedOrigin     string
		expectedMethods    string
		expectedHeaders    string
		expectedStatusCode int
	}{
		{
			name: "CORS enabled with wildcard origin",
			corsConfig: CORSConfig{
				Enabled:        true,
				AllowedOrigins: []string{"*"},
				AllowedMethods: []string{"GET", "POST"},
				AllowedHeaders: []string{"Content-Type"},
			},
			requestOrigin:      "https://example.com",
			requestMethod:      "GET",
			expectedOrigin:     "*",
			expectedMethods:    "GET, POST",
			expectedHeaders:    "Content-Type",
			expectedStatusCode: http.StatusOK,
		},
		{
			name: "CORS enabled with specific origin",
			corsConfig: CORSConfig{
				Enabled:        true,
				AllowedOrigins: []string{"https://example.com"},
				AllowedMethods: []string{"GET", "POST", "DELETE"},
				AllowedHeaders: []string{"*"},
			},
			requestOrigin:      "https://example.com",
			requestMethod:      "GET",
			expectedOrigin:     "https://example.com",
			expectedMethods:    "GET, POST, DELETE",
			expectedHeaders:    "*",
			expectedStatusCode: http.StatusOK,
		},
		{
			name: "CORS disabled",
			corsConfig: CORSConfig{
				Enabled: false,
			},
			requestOrigin:      "https://example.com",
			requestMethod:      "GET",
			expectedOrigin:     "",
			expectedStatusCode: http.StatusOK,
		},
		{
			name: "OPTIONS preflight request",
			corsConfig: CORSConfig{
				Enabled:        true,
				AllowedOrigins: []string{"*"},
				AllowedMethods: []string{"GET", "POST", "OPTIONS"},
				AllowedHeaders: []string{"Content-Type", "Authorization"},
			},
			requestOrigin:      "https://example.com",
			requestMethod:      "OPTIONS",
			expectedOrigin:     "*",
			expectedMethods:    "GET, POST, OPTIONS",
			expectedHeaders:    "Content-Type, Authorization",
			expectedStatusCode: http.StatusNoContent,
		},
		{
			name: "Origin not allowed",
			corsConfig: CORSConfig{
				Enabled:        true,
				AllowedOrigins: []string{"https://allowed.com"},
				AllowedMethods: []string{"GET"},
			},
			requestOrigin:      "https://not-allowed.com",
			requestMethod:      "GET",
			expectedOrigin:     "",
			expectedStatusCode: http.StatusOK,
		},
		{
			name: "CORS with credentials",
			corsConfig: CORSConfig{
				Enabled:          true,
				AllowedOrigins:   []string{"https://example.com"},
				AllowedMethods:   []string{"GET", "POST"},
				AllowCredentials: true,
			},
			requestOrigin:      "https://example.com",
			requestMethod:      "GET",
			expectedOrigin:     "https://example.com",
			expectedMethods:    "GET, POST",
			expectedStatusCode: http.StatusOK,
		},
		{
			name: "CORS with max age",
			corsConfig: CORSConfig{
				Enabled:        true,
				AllowedOrigins: []string{"*"},
				AllowedMethods: []string{"GET"},
				MaxAge:         3600,
			},
			requestOrigin:      "https://example.com",
			requestMethod:      "OPTIONS",
			expectedOrigin:     "*",
			expectedMethods:    "GET",
			expectedStatusCode: http.StatusNoContent,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a test handler that returns 200 OK
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				w.Write([]byte("OK"))
			})

			// Create HTTP server with CORS config
			httpServer := &HTTPServer{
				corsConfig: tt.corsConfig,
			}

			// Wrap handler with CORS middleware if enabled
			var wrappedHandler http.Handler = handler
			if tt.corsConfig.Enabled {
				wrappedHandler = httpServer.corsMiddleware(handler)
			}

			// Create test request
			req := httptest.NewRequest(tt.requestMethod, "/test", nil)
			if tt.requestOrigin != "" {
				req.Header.Set("Origin", tt.requestOrigin)
			}

			// Record response
			rr := httptest.NewRecorder()
			wrappedHandler.ServeHTTP(rr, req)

			// Verify status code
			assert.Equal(t, tt.expectedStatusCode, rr.Code, "status code should match")

			// Verify CORS headers
			if tt.expectedOrigin != "" {
				assert.Equal(t, tt.expectedOrigin, rr.Header().Get("Access-Control-Allow-Origin"),
					"Access-Control-Allow-Origin should match")
			} else if tt.corsConfig.Enabled {
				assert.Empty(t, rr.Header().Get("Access-Control-Allow-Origin"),
					"Access-Control-Allow-Origin should be empty when origin not allowed")
			}

			if tt.expectedMethods != "" {
				assert.Equal(t, tt.expectedMethods, rr.Header().Get("Access-Control-Allow-Methods"),
					"Access-Control-Allow-Methods should match")
			}

			if tt.expectedHeaders != "" {
				assert.Equal(t, tt.expectedHeaders, rr.Header().Get("Access-Control-Allow-Headers"),
					"Access-Control-Allow-Headers should match")
			}

			// Verify credentials header
			if tt.corsConfig.AllowCredentials && tt.expectedOrigin != "" {
				assert.Equal(t, "true", rr.Header().Get("Access-Control-Allow-Credentials"),
					"Access-Control-Allow-Credentials should be true")
			}

			// Verify max age header
			if tt.corsConfig.MaxAge > 0 && tt.requestMethod == "OPTIONS" {
				assert.NotEmpty(t, rr.Header().Get("Access-Control-Max-Age"),
					"Access-Control-Max-Age should be set for OPTIONS")
			}
		})
	}
}

func TestDefaultCORSConfig(t *testing.T) {
	config := DefaultCORSConfig()

	assert.True(t, config.Enabled, "CORS should be enabled by default")
	assert.Contains(t, config.AllowedOrigins, "*", "should allow all origins by default")
	assert.Contains(t, config.AllowedMethods, "GET", "should allow GET by default")
	assert.Contains(t, config.AllowedMethods, "POST", "should allow POST by default")
	assert.Contains(t, config.AllowedHeaders, "*", "should allow all headers by default")
	assert.False(t, config.AllowCredentials, "credentials should not be allowed by default")
	assert.Equal(t, 86400, config.MaxAge, "max age should be 24 hours by default")
}

func TestGetAllowedOrigin(t *testing.T) {
	tests := []struct {
		name           string
		allowedOrigins []string
		requestOrigin  string
		expectedResult string
	}{
		{
			name:           "wildcard allows any origin",
			allowedOrigins: []string{"*"},
			requestOrigin:  "https://example.com",
			expectedResult: "*",
		},
		{
			name:           "exact match",
			allowedOrigins: []string{"https://example.com", "https://another.com"},
			requestOrigin:  "https://example.com",
			expectedResult: "https://example.com",
		},
		{
			name:           "no match",
			allowedOrigins: []string{"https://allowed.com"},
			requestOrigin:  "https://not-allowed.com",
			expectedResult: "",
		},
		{
			name:           "empty origin",
			allowedOrigins: []string{"*"},
			requestOrigin:  "",
			expectedResult: "",
		},
		{
			name:           "empty allowed list",
			allowedOrigins: []string{},
			requestOrigin:  "https://example.com",
			expectedResult: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			httpServer := &HTTPServer{
				corsConfig: CORSConfig{
					AllowedOrigins: tt.allowedOrigins,
				},
			}

			result := httpServer.getAllowedOrigin(tt.requestOrigin)
			assert.Equal(t, tt.expectedResult, result)
		})
	}
}

func TestSwaggerUIHandler(t *testing.T) {
	httpServer := &HTTPServer{}

	req := httptest.NewRequest("GET", "/swagger-ui", nil)
	rr := httptest.NewRecorder()

	httpServer.handleSwaggerUI(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "text/html; charset=utf-8", rr.Header().Get("Content-Type"))
	assert.Contains(t, rr.Body.String(), "Loom API Documentation")
	assert.Contains(t, rr.Body.String(), "swagger-ui")
	assert.Contains(t, rr.Body.String(), "/openapi.json")
}

func TestOpenAPISpecHandler(t *testing.T) {
	// This test would require the actual spec file to exist
	// For now, we'll test the error case
	httpServer := &HTTPServer{}
	// Use no-op logger to avoid nil pointer issues
	httpServer.logger, _ = zap.NewDevelopment()

	req := httptest.NewRequest("GET", "/openapi.json", nil)
	rr := httptest.NewRecorder()

	httpServer.handleOpenAPISpec(rr, req)

	// Should return 404 since the spec file doesn't exist in test environment
	// In actual deployment, the spec would be available
	assert.Equal(t, http.StatusNotFound, rr.Code)
}

func TestNewHTTPServer(t *testing.T) {
	httpServer := NewHTTPServer(nil, "localhost:8080", "localhost:9090", nil)

	require.NotNil(t, httpServer)
	assert.True(t, httpServer.corsConfig.Enabled, "CORS should be enabled by default")
	assert.NotNil(t, httpServer.httpServer)
}

func TestNewHTTPServerWithCORS(t *testing.T) {
	customCORS := CORSConfig{
		Enabled:        false,
		AllowedOrigins: []string{"https://custom.com"},
	}

	httpServer := NewHTTPServerWithCORS(nil, "localhost:8080", "localhost:9090", nil, customCORS)

	require.NotNil(t, httpServer)
	assert.False(t, httpServer.corsConfig.Enabled)
	assert.Equal(t, []string{"https://custom.com"}, httpServer.corsConfig.AllowedOrigins)
}
