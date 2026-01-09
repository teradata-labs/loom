// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

// HTTPServer wraps gRPC server with HTTP/REST+SSE endpoints
type HTTPServer struct {
	grpcServer *MultiAgentServer
	httpServer *http.Server
	logger     *zap.Logger
	grpcAddr   string
}

// NewHTTPServer creates an HTTP server that proxies to gRPC
func NewHTTPServer(grpcServer *MultiAgentServer, httpAddr, grpcAddr string, logger *zap.Logger) *HTTPServer {
	if logger == nil {
		logger = zap.NewNop()
	}

	return &HTTPServer{
		grpcServer: grpcServer,
		logger:     logger,
		grpcAddr:   grpcAddr,
		httpServer: &http.Server{
			Addr:         httpAddr,
			ReadTimeout:  30 * time.Second,
			WriteTimeout: 0, // No timeout for SSE
			IdleTimeout:  120 * time.Second,
		},
	}
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

	// SSE endpoint for streaming (custom handler)
	rootMux.HandleFunc("/v1/weave:stream", h.handleStreamWeaveSSE)

	// All other endpoints use grpc-gateway
	rootMux.Handle("/", mux)

	h.httpServer.Handler = rootMux

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
