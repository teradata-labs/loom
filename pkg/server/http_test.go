// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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
				_, _ = w.Write([]byte("OK"))
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

// TestSSEStreamWrapperSendUsesProtoJSON guards against regressing to
// encoding/json for WeaveProgress SSE frames. SSE clients (e.g. the
// AgentRuntime UI's parseLoomWeaveProgress) expect proto-JSON: camelCase
// field names and string enum values. Plain encoding/json instead emits the
// struct's snake_case json tags and raw numeric enums, which those clients
// silently ignore, producing an empty response.
func TestSSEStreamWrapperSendUsesProtoJSON(t *testing.T) {
	rr := httptest.NewRecorder()
	wrapper := &sseStreamWrapper{
		ctx:     nil, //nolint:staticcheck // test-only stream wrapper, no context needed
		writer:  rr,
		flusher: rr,
		logger:  zap.NewNop(),
	}

	progress := &loomv1.WeaveProgress{
		Stage:          loomv1.ExecutionStage_EXECUTION_STAGE_COMPLETED,
		IsTokenStream:  true,
		PartialContent: "hello world",
		TokenCount:     2,
	}

	require.NoError(t, wrapper.Send(progress))

	body := rr.Body.String()
	assert.Contains(t, body, `"stage":"EXECUTION_STAGE_COMPLETED"`)
	assert.Contains(t, body, `"isTokenStream":true`)
	assert.Contains(t, body, `"partialContent":"hello world"`)
	assert.Contains(t, body, `"tokenCount":2`)

	// snake_case field names must NOT appear on the wire.
	assert.NotContains(t, body, "is_token_stream")
	assert.NotContains(t, body, "partial_content")
}

// TestSSEStreamWrapperWriteHeartbeat guards the idle-connection keepalive:
// writeHeartbeat must emit an SSE comment line (starts with ":", not
// "data:") so spec-compliant SSE clients ignore it, while still producing
// bytes on the wire so upstream idle-timeout proxies/load balancers (which
// may kill a silent SSE connection, e.g. during a long skill-decompose LLM
// call) see traffic and don't terminate the request before the agent emits
// its first real WeaveProgress event.
func TestSSEStreamWrapperWriteHeartbeat(t *testing.T) {
	rr := httptest.NewRecorder()
	wrapper := &sseStreamWrapper{
		ctx:     nil, //nolint:staticcheck // test-only stream wrapper, no context needed
		writer:  rr,
		flusher: rr,
		logger:  zap.NewNop(),
	}

	require.NoError(t, wrapper.writeHeartbeat())

	body := rr.Body.String()
	assert.Equal(t, ": heartbeat\n\n", body)
	assert.False(t, strings.HasPrefix(body, "data:"), "heartbeat must not be parsed as a data event")
}

// TestSSEStreamWrapperWritesConcurrent guards against data races among progress,
// heartbeat, and terminal error writes to the same ResponseWriter/Flusher.
// Run with -race to catch regressions if the mutex is ever removed.
func TestSSEStreamWrapperWritesConcurrent(t *testing.T) {
	rr := httptest.NewRecorder()
	wrapper := &sseStreamWrapper{
		ctx:     nil, //nolint:staticcheck // test-only stream wrapper, no context needed
		writer:  rr,
		flusher: rr,
		logger:  zap.NewNop(),
	}

	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			_ = wrapper.writeHeartbeat()
		}
	}()
	go func() {
		defer wg.Done()
		progress := &loomv1.WeaveProgress{PartialContent: "chunk"}
		for i := 0; i < 20; i++ {
			_ = wrapper.Send(progress)
		}
	}()
	go func() {
		defer wg.Done()
		wrapper.writeError(errors.New("stream failed"))
	}()
	wg.Wait()
	assert.Contains(t, rr.Body.String(), `"stage":"EXECUTION_STAGE_FAILED"`)
}

// TestIsClientCanceled verifies that client-initiated stream cancellations
// (context.Canceled and gRPC codes.Canceled) are distinguished from genuine
// server-side failures, so handleStreamWeaveSSE can log the former at a
// lower severity instead of raising false-positive "StreamWeave failed"
// errors for routine client disconnects (e.g. closed EventSource/tab).
func TestIsClientCanceled(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "context.Canceled directly",
			err:  context.Canceled,
			want: true,
		},
		{
			name: "wrapped context.Canceled",
			err:  fmt.Errorf("stream ended: %w", context.Canceled),
			want: true,
		},
		{
			name: "gRPC codes.Canceled status",
			err:  status.Error(codes.Canceled, "client cancelled request"),
			want: true,
		},
		{
			name: "unrelated error",
			err:  errors.New("tool not found: base_readQuery"),
			want: false,
		},
		{
			name: "gRPC codes.Internal status",
			err:  status.Error(codes.Internal, "boom"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isClientCanceled(tt.err))
		})
	}
}
