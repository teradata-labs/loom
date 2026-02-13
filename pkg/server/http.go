// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

// CORSConfig holds CORS configuration
type CORSConfig struct {
	Enabled          bool
	AllowedOrigins   []string
	AllowedMethods   []string
	AllowedHeaders   []string
	ExposedHeaders   []string
	AllowCredentials bool
	MaxAge           int
}

// DefaultCORSConfig returns a permissive CORS configuration
func DefaultCORSConfig() CORSConfig {
	return CORSConfig{
		Enabled:          true,
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS", "PATCH"},
		AllowedHeaders:   []string{"*"},
		ExposedHeaders:   []string{"Content-Length", "Content-Type"},
		AllowCredentials: false,
		MaxAge:           86400, // 24 hours
	}
}

// AppHTMLProvider provides HTML content for UI apps.
// Used by the HTTP server to serve apps in the browser.
type AppHTMLProvider interface {
	AppNames() []string
	AppHTML(name string) ([]byte, error)
}

// HTTPServer wraps gRPC server with HTTP/REST+SSE endpoints
type HTTPServer struct {
	grpcServer      *MultiAgentServer
	httpServer      *http.Server
	logger          *zap.Logger
	grpcAddr        string
	corsConfig      CORSConfig
	appHTMLProvider AppHTMLProvider
}

// NewHTTPServer creates an HTTP server that proxies to gRPC
func NewHTTPServer(grpcServer *MultiAgentServer, httpAddr, grpcAddr string, logger *zap.Logger) *HTTPServer {
	return NewHTTPServerWithCORS(grpcServer, httpAddr, grpcAddr, logger, DefaultCORSConfig())
}

// NewHTTPServerWithCORS creates an HTTP server with custom CORS configuration
func NewHTTPServerWithCORS(grpcServer *MultiAgentServer, httpAddr, grpcAddr string, logger *zap.Logger, corsConfig CORSConfig) *HTTPServer {
	if logger == nil {
		logger = zap.NewNop()
	}

	return &HTTPServer{
		grpcServer: grpcServer,
		logger:     logger,
		grpcAddr:   grpcAddr,
		corsConfig: corsConfig,
		httpServer: &http.Server{
			Addr:         httpAddr,
			ReadTimeout:  30 * time.Second,
			WriteTimeout: 0, // No timeout for SSE
			IdleTimeout:  120 * time.Second,
		},
	}
}

// SetAppHTMLProvider sets the provider used to serve UI apps over HTTP.
// Must be called before Start(); not safe for concurrent use.
func (h *HTTPServer) SetAppHTMLProvider(p AppHTMLProvider) {
	h.appHTMLProvider = p
}

// Start starts the HTTP server
func (h *HTTPServer) Start(ctx context.Context) error {
	// Create gRPC-gateway mux
	mux := runtime.NewServeMux(
		runtime.WithMarshalerOption(runtime.MIMEWildcard, &runtime.JSONPb{}),
	)

	// Connect to gRPC server
	opts := []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}
	err := loomv1.RegisterLoomServiceHandlerFromEndpoint(ctx, mux, h.grpcAddr, opts)
	if err != nil {
		return fmt.Errorf("failed to register gateway: %w", err)
	}

	// Create root mux with custom handlers
	rootMux := http.NewServeMux()

	// Health check
	rootMux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"healthy"}`))
	})

	// Swagger UI endpoint
	rootMux.HandleFunc("/swagger-ui", h.handleSwaggerUI)
	rootMux.HandleFunc("/swagger-ui/", h.handleSwaggerUI)
	rootMux.HandleFunc("/openapi.json", h.handleOpenAPISpec)

	// SSE endpoint for streaming (custom handler)
	rootMux.HandleFunc("/v1/weave:stream", h.handleStreamWeaveSSE)

	// UI Apps browser endpoint
	if h.appHTMLProvider != nil {
		rootMux.HandleFunc("/apps/", h.handleApps)
		rootMux.HandleFunc("/apps", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/apps/", http.StatusMovedPermanently)
		})
	}

	// All other endpoints use grpc-gateway
	rootMux.Handle("/", mux)

	// Wrap with CORS middleware if enabled
	var handler http.Handler = rootMux
	if h.corsConfig.Enabled {
		handler = h.corsMiddleware(rootMux)
	}

	h.httpServer.Handler = handler

	// Start server
	h.logger.Info("Starting HTTP server", zap.String("addr", h.httpServer.Addr))
	if err := h.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("HTTP server failed: %w", err)
	}

	return nil
}

// Stop gracefully stops the HTTP server
func (h *HTTPServer) Stop(ctx context.Context) error {
	h.logger.Info("Stopping HTTP server")
	return h.httpServer.Shutdown(ctx)
}

// corsMiddleware adds CORS headers to HTTP responses
func (h *HTTPServer) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")

		// Check if origin is allowed
		allowedOrigin := h.getAllowedOrigin(origin)
		if allowedOrigin != "" {
			w.Header().Set("Access-Control-Allow-Origin", allowedOrigin)
		}

		// Set other CORS headers
		if h.corsConfig.AllowCredentials {
			w.Header().Set("Access-Control-Allow-Credentials", "true")
		}

		if len(h.corsConfig.AllowedMethods) > 0 {
			methods := ""
			for i, method := range h.corsConfig.AllowedMethods {
				if i > 0 {
					methods += ", "
				}
				methods += method
			}
			w.Header().Set("Access-Control-Allow-Methods", methods)
		}

		if len(h.corsConfig.AllowedHeaders) > 0 {
			headers := ""
			for i, header := range h.corsConfig.AllowedHeaders {
				if i > 0 {
					headers += ", "
				}
				headers += header
			}
			w.Header().Set("Access-Control-Allow-Headers", headers)
		}

		if len(h.corsConfig.ExposedHeaders) > 0 {
			headers := ""
			for i, header := range h.corsConfig.ExposedHeaders {
				if i > 0 {
					headers += ", "
				}
				headers += header
			}
			w.Header().Set("Access-Control-Expose-Headers", headers)
		}

		if h.corsConfig.MaxAge > 0 {
			w.Header().Set("Access-Control-Max-Age", fmt.Sprintf("%d", h.corsConfig.MaxAge))
		}

		// Handle preflight OPTIONS requests
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		// Call next handler
		next.ServeHTTP(w, r)
	})
}

// getAllowedOrigin checks if the origin is allowed and returns it, or empty string if not
func (h *HTTPServer) getAllowedOrigin(origin string) string {
	if origin == "" {
		return ""
	}

	// Check if wildcard is allowed
	for _, allowed := range h.corsConfig.AllowedOrigins {
		if allowed == "*" {
			return "*"
		}
		if allowed == origin {
			return origin
		}
	}

	return ""
}

// handleSwaggerUI serves the Swagger UI interface
func (h *HTTPServer) handleSwaggerUI(w http.ResponseWriter, r *http.Request) {
	html := `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <title>Loom API Documentation</title>
    <link rel="stylesheet" type="text/css" href="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5/swagger-ui.css" />
    <style>
        html { box-sizing: border-box; overflow: -moz-scrollbars-vertical; overflow-y: scroll; }
        *, *:before, *:after { box-sizing: inherit; }
        body { margin: 0; padding: 0; }
    </style>
</head>
<body>
    <div id="swagger-ui"></div>
    <script src="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5/swagger-ui-bundle.js"></script>
    <script src="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5/swagger-ui-standalone-preset.js"></script>
    <script>
        window.onload = function() {
            const ui = SwaggerUIBundle({
                url: "/openapi.json",
                dom_id: '#swagger-ui',
                deepLinking: true,
                presets: [
                    SwaggerUIBundle.presets.apis,
                    SwaggerUIStandalonePreset
                ],
                plugins: [
                    SwaggerUIBundle.plugins.DownloadUrl
                ],
                layout: "StandaloneLayout"
            });
            window.ui = ui;
        };
    </script>
</body>
</html>`

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(html))
}

// handleOpenAPISpec serves the OpenAPI specification
func (h *HTTPServer) handleOpenAPISpec(w http.ResponseWriter, r *http.Request) {
	// Serve the main loom.swagger.json file
	// In a production setup, this could be embedded or read from disk
	specPath := "gen/openapiv2/loom/v1/loom.swagger.json"

	// Try to read the spec file
	spec, err := os.ReadFile(specPath)
	if err != nil {
		h.logger.Error("Failed to read OpenAPI spec", zap.Error(err))
		http.Error(w, "OpenAPI spec not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(spec)
}

// handleStreamWeaveSSE handles SSE streaming for /v1/weave:stream
func (h *HTTPServer) handleStreamWeaveSSE(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse request body
	var req loomv1.WeaveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid request: %v", err), http.StatusBadRequest)
		return
	}

	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // Disable nginx buffering

	// Flush headers immediately
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}

	// Create gRPC stream
	stream := &sseStreamWrapper{
		ctx:     r.Context(),
		writer:  w,
		flusher: w.(http.Flusher),
		logger:  h.logger,
	}

	// Execute StreamWeave
	if err := h.grpcServer.StreamWeave(&req, stream); err != nil {
		h.logger.Error("StreamWeave failed", zap.Error(err))
		// Send error as SSE event
		h.sendSSEError(w, stream.flusher, err)
	}
}

// sseStreamWrapper implements loomv1.LoomService_StreamWeaveServer for SSE
type sseStreamWrapper struct {
	ctx     context.Context
	writer  http.ResponseWriter
	flusher http.Flusher
	logger  *zap.Logger
}

func (s *sseStreamWrapper) Send(progress *loomv1.WeaveProgress) error {
	// Convert progress to JSON
	data, err := json.Marshal(progress)
	if err != nil {
		return fmt.Errorf("failed to marshal progress: %w", err)
	}

	// Write SSE event
	_, err = fmt.Fprintf(s.writer, "data: %s\n\n", data)
	if err != nil {
		return err
	}

	// Flush immediately
	s.flusher.Flush()

	return nil
}

func (s *sseStreamWrapper) SetHeader(md metadata.MD) error {
	// SSE doesn't support setting headers after response started
	return nil
}

func (s *sseStreamWrapper) SendHeader(md metadata.MD) error {
	// SSE doesn't support setting headers after response started
	return nil
}

func (s *sseStreamWrapper) SetTrailer(md metadata.MD) {
	// SSE doesn't support trailers
}

func (s *sseStreamWrapper) Context() context.Context {
	return s.ctx
}

func (s *sseStreamWrapper) SendMsg(m interface{}) error {
	return fmt.Errorf("SendMsg not implemented for SSE")
}

func (s *sseStreamWrapper) RecvMsg(m interface{}) error {
	return fmt.Errorf("RecvMsg not implemented for SSE")
}

// handleApps dispatches to either the app index or individual app HTML.
func (h *HTTPServer) handleApps(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// "/apps/" -> index, "/apps/data-chart" -> individual app
	name := strings.TrimPrefix(r.URL.Path, "/apps/")
	if name == "" {
		h.handleAppsIndex(w, r)
		return
	}

	// Reject path traversal or invalid characters in app name.
	if !isValidAppName(name) {
		http.Error(w, "Invalid app name", http.StatusBadRequest)
		return
	}
	h.handleAppHTML(w, r, name)
}

// isValidAppName returns true if the name contains only safe characters
// (alphanumeric, hyphens, underscores). Rejects path traversal attempts.
func isValidAppName(name string) bool {
	for _, c := range name {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
			return false
		}
	}
	return len(name) > 0
}

// handleAppsIndex serves an HTML index page listing all available apps.
func (h *HTTPServer) handleAppsIndex(w http.ResponseWriter, _ *http.Request) {
	names := h.appHTMLProvider.AppNames()

	// Build a simple HTML page with links
	var sb strings.Builder
	sb.WriteString(`<!DOCTYPE html><html lang="en"><head><meta charset="UTF-8">`)
	sb.WriteString(`<title>Loom Apps</title>`)
	sb.WriteString(`<style>body{font-family:system-ui,sans-serif;max-width:600px;margin:40px auto;padding:0 20px}`)
	sb.WriteString(`a{display:block;padding:12px 16px;margin:8px 0;background:#f5f5f5;border-radius:8px;`)
	sb.WriteString(`text-decoration:none;color:#333}a:hover{background:#e8e8e8}</style></head><body>`)
	sb.WriteString(`<h1>Loom Apps</h1>`)
	if len(names) == 0 {
		sb.WriteString(`<p>No apps available.</p>`)
	}
	for _, name := range names {
		sb.WriteString(fmt.Sprintf(`<a href="/apps/%s">%s</a>`, url.PathEscape(name), html.EscapeString(name)))
	}
	sb.WriteString(`</body></html>`)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "SAMEORIGIN")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(sb.String()))
}

// handleAppHTML serves the raw HTML for a specific app with security headers.
func (h *HTTPServer) handleAppHTML(w http.ResponseWriter, _ *http.Request, name string) {
	content, err := h.appHTMLProvider.AppHTML(name)
	if err != nil {
		http.Error(w, "App not found", http.StatusNotFound)
		return
	}

	// Security headers
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "SAMEORIGIN")
	w.Header().Set("Content-Security-Policy",
		"default-src 'self'; script-src 'self' 'unsafe-inline' https://cdn.jsdelivr.net; "+
			"style-src 'self' 'unsafe-inline'; img-src 'self' data:; "+
			"connect-src 'self'; frame-ancestors 'self'")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(content)
}

// sendSSEError sends an error event via SSE
func (h *HTTPServer) sendSSEError(w http.ResponseWriter, flusher http.Flusher, err error) {
	errorEvent := map[string]interface{}{
		"error":    err.Error(),
		"stage":    "EXECUTION_STAGE_FAILED",
		"progress": 0,
	}

	data, _ := json.Marshal(errorEvent)
	fmt.Fprintf(w, "data: %s\n\n", data)
	flusher.Flush()
}
