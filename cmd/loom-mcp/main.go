// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// loom-mcp is a lightweight MCP (Model Context Protocol) server that bridges
// between MCP clients and a running Loom server. It connects to a running looms
// server via gRPC and exposes all Loom capabilities as MCP tools, plus MCP Apps
// UI resources (like the conversation viewer).
//
// It supports two transports:
//
//   - stdio (default): JSON-RPC over stdin/stdout, for Claude Desktop and IDEs.
//   - http: Streamable HTTP (MCP 2025-03-26) for remote clients. The loom_weave
//     tool streams progress via POST-response Server-Sent Events. The HTTP
//     transport has no built-in auth and binds to localhost by default.
//
// Usage:
//
//	loom-mcp --grpc-addr localhost:60051
//	loom-mcp --transport http --http-addr 127.0.0.1:8765
//
// Claude Desktop configuration (claude_desktop_config.json):
//
//	{
//	  "mcpServers": {
//	    "loom": {
//	      "command": "/path/to/loom-mcp",
//	      "args": ["--grpc-addr", "localhost:60051"]
//	    }
//	  }
//	}
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/teradata-labs/loom/internal/version"
	"github.com/teradata-labs/loom/pkg/mcp/apps"
	"github.com/teradata-labs/loom/pkg/mcp/protocol"
	"github.com/teradata-labs/loom/pkg/mcp/server"
	"github.com/teradata-labs/loom/pkg/mcp/transport"
	loomserver "github.com/teradata-labs/loom/pkg/server"
	"github.com/teradata-labs/loom/pkg/skills"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

const serverName = "loom-mcp"

func main() {
	grpcAddr := flag.String("grpc-addr", "localhost:60051", "Address of the running looms gRPC server")
	transportKind := flag.String("transport", "stdio", "MCP transport: stdio (default, for Claude Desktop/IDEs) or http (Streamable HTTP for remote clients)")
	httpAddr := flag.String("http-addr", "127.0.0.1:8765", "Listen address for --transport=http (localhost-only by default; see security notes)")
	tlsCert := flag.String("tls-cert", "", "Path to PEM-encoded CA certificate for TLS (enables TLS when set)")
	tlsSkipVerify := flag.Bool("tls-skip-verify", false, "Skip TLS server certificate verification (NOT recommended for production)")
	logFile := flag.String("log-file", "", "Log file path (defaults to stderr redirect to /dev/null)")
	logLevel := flag.String("log-level", "info", "Log level (debug, info, warn, error)")
	flag.Parse()

	// Configure logging -- CRITICAL: never write to stdout (that's the MCP transport)
	logger := setupLogger(*logFile, *logLevel)
	defer func() { _ = logger.Sync() }()

	logger.Info("starting loom-mcp server",
		zap.String("grpc_addr", *grpcAddr),
		zap.String("transport", *transportKind),
		zap.String("version", version.Get()),
	)

	// Create UI resource registry with embedded apps
	uiRegistry := apps.NewUIResourceRegistry()
	if err := apps.RegisterEmbeddedApps(uiRegistry); err != nil {
		logger.Fatal("failed to register embedded apps", zap.Error(err))
	}
	logger.Info("registered UI resources", zap.Int("count", uiRegistry.Count()))

	// Build bridge options
	var bridgeOpts []server.BridgeOption
	if *tlsCert != "" || *tlsSkipVerify {
		bridgeOpts = append(bridgeOpts, server.WithTLS(*tlsCert, *tlsSkipVerify))
		logger.Info("TLS enabled for gRPC connection",
			zap.String("cert_file", *tlsCert),
			zap.Bool("skip_verify", *tlsSkipVerify),
		)
	}

	// Initialize skills library and orchestrator for MCP skill tools
	skillsDir := os.Getenv("LOOM_SKILLS_DIR")
	if skillsDir == "" {
		home, _ := os.UserHomeDir()
		if home != "" {
			skillsDir = home + "/.loom/skills"
		}
	}
	if skillsDir != "" {
		skillLib := skills.NewLibrary(skills.WithSearchPaths(skillsDir))
		skillOrch := skills.NewOrchestrator(skillLib)
		bridgeOpts = append(bridgeOpts, server.WithSkillOrchestrator(skillOrch))
		logger.Info("skills orchestrator initialized", zap.String("skills_dir", skillsDir))
	}

	// Connect to running looms via gRPC
	bridge, err := server.NewLoomBridge(*grpcAddr, uiRegistry, logger, bridgeOpts...)
	if err != nil {
		logger.Fatal("failed to connect to looms", zap.Error(err))
	}
	defer func() { _ = bridge.Close() }()
	logger.Info("connected to looms", zap.String("addr", *grpcAddr))

	// Create MCP server with bridge as provider
	mcpServer := server.NewMCPServer(serverName, version.Get(), logger,
		server.WithToolProvider(bridge),
		server.WithResourceProvider(bridge),
		server.WithExtensions(protocol.ServerAppsExtension()),
	)

	// Wire MCP server to bridge so app mutations trigger resource list change notifications.
	bridge.SetMCPServer(mcpServer)

	// Set up signal handling for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		logger.Info("received signal, shutting down", zap.String("signal", sig.String()))
		cancel()
	}()

	switch *transportKind {
	case "stdio":
		runStdio(ctx, mcpServer, logger)
	case "http":
		runHTTP(ctx, mcpServer, *httpAddr, logger)
	default:
		logger.Fatal("unknown transport (use stdio or http)", zap.String("transport", *transportKind))
	}
}

// runStdio serves the MCP server over stdio (JSON-RPC on stdin/stdout) — the
// default, used by Claude Desktop and IDE clients.
func runStdio(ctx context.Context, mcpServer *server.MCPServer, logger *zap.Logger) {
	stdioTransport := transport.NewStdioServerTransport(os.Stdin, os.Stdout)
	logger.Info("MCP server ready, awaiting client connections on stdio")
	if err := mcpServer.Serve(ctx, stdioTransport); err != nil {
		if ctx.Err() != nil {
			logger.Info("server stopped gracefully")
		} else {
			logger.Error("server error", zap.Error(err))
			os.Exit(1)
		}
	}
}

// runHTTP serves the MCP server over Streamable HTTP (MCP 2025-03-26) for remote
// clients. The bridge's loom_weave tool streams progress via POST-response SSE.
//
// SECURITY: this transport has no built-in authentication. It binds to localhost
// by default; exposing it on a network interface (or via a tunnel) without an
// authenticating layer in front grants unauthenticated access to all MCP tools.
// Phase 1C adds native Supabase-JWT auth in front of this handler.
func runHTTP(ctx context.Context, mcpServer *server.MCPServer, addr string, logger *zap.Logger) {
	transport.WarnIfNotLocalhost(logger, addr)

	httpSrv, err := transport.NewStreamableHTTPServer(transport.StreamableHTTPServerConfig{
		Handler:       func(msg []byte) ([]byte, error) { return mcpServer.HandleMessage(context.Background(), msg) },
		StreamHandler: mcpServer,
		Logger:        logger,
		SessionTTL:    transport.DefaultSessionTTL,
	})
	if err != nil {
		logger.Fatal("failed to create HTTP-MCP transport", zap.Error(err))
	}
	defer httpSrv.Close()

	// Authenticate the exposed endpoint with Supabase JWTs when configured. The
	// edge validates the bearer (401 on failure) and forwards the caller identity
	// to looms on streaming calls (loom_weave). Reads the same LOOM_SERVER_AUTH_*
	// env vars as looms so a single config drives both.
	var handler http.Handler = httpSrv
	if authr, required, enabled := buildEdgeAuthenticator(logger); enabled {
		handler = authMiddleware(authr, required, logger, httpSrv)
		logger.Info("HTTP-MCP endpoint authentication enabled (Supabase JWT)", zap.Bool("required", required))
	} else {
		logger.Warn("HTTP-MCP endpoint authentication is DISABLED; the endpoint is unauthenticated")
	}

	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Shut the HTTP server down when the context is cancelled (signal received).
	go func() {
		<-ctx.Done()
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelShutdown()
		_ = srv.Shutdown(shutdownCtx)
	}()

	logger.Info("HTTP-MCP server ready",
		zap.String("addr", addr),
		zap.String("endpoint", fmt.Sprintf("POST http://%s/ (Streamable HTTP, MCP 2025-03-26)", addr)),
	)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("HTTP server error", zap.Error(err))
		os.Exit(1)
	}
	logger.Info("server stopped gracefully")
}

// buildEdgeAuthenticator builds the HTTP-MCP edge authenticator from the same
// LOOM_SERVER_AUTH_* environment variables looms uses. Returns enabled=false
// when auth is not configured. JWKS URL and issuer are derived from the project
// ref when not set (mirrors the looms config derivation).
func buildEdgeAuthenticator(logger *zap.Logger) (auth *loomserver.Authenticator, required, enabled bool) {
	if !envBool("LOOM_SERVER_AUTH_ENABLED") {
		return nil, false, false
	}
	ref := os.Getenv("LOOM_SERVER_AUTH_SUPABASE_PROJECT_REF")
	jwksURL := os.Getenv("LOOM_SERVER_AUTH_SUPABASE_JWKS_URL")
	issuer := os.Getenv("LOOM_SERVER_AUTH_SUPABASE_ISSUER")
	audience := os.Getenv("LOOM_SERVER_AUTH_SUPABASE_AUDIENCE")
	if audience == "" {
		audience = "authenticated"
	}
	if ref != "" {
		if jwksURL == "" {
			jwksURL = fmt.Sprintf("https://%s.supabase.co/auth/v1/.well-known/jwks.json", ref)
		}
		if issuer == "" {
			issuer = fmt.Sprintf("https://%s.supabase.co/auth/v1", ref)
		}
	}
	var secret []byte
	if s := os.Getenv("LOOM_SERVER_AUTH_SUPABASE_JWT_SECRET"); s != "" {
		secret = []byte(s)
	}
	required = os.Getenv("LOOM_SERVER_AUTH_MODE") != "optional"

	authr, err := loomserver.NewAuthenticator(context.Background(), loomserver.AuthConfig{
		Required:    required,
		HS256Secret: secret,
		JWKSURL:     jwksURL,
		Audience:    audience,
		Issuer:      issuer,
		Logger:      logger,
	})
	if err != nil {
		logger.Fatal("failed to initialize HTTP-MCP authenticator", zap.Error(err))
	}
	return authr, required, true
}

// authMiddleware validates the inbound Supabase JWT and forwards the caller
// identity to looms (on streaming calls). A missing token yields 401 when auth
// is required; a present-but-invalid token is always 401.
func authMiddleware(auth *loomserver.Authenticator, required bool, logger *zap.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := edgeBearer(r.Header.Get("Authorization"))
		if raw == "" {
			if required {
				w.Header().Set("WWW-Authenticate", "Bearer")
				http.Error(w, "authorization bearer token required", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		claims, err := auth.ValidateToken(raw)
		if err != nil {
			logger.Debug("edge JWT validation failed", zap.Error(err))
			http.Error(w, "invalid authentication token", http.StatusUnauthorized)
			return
		}
		sub, _ := claims["sub"].(string)
		// Forward the bearer (looms re-validates) and the subject as x-user-id
		// (used when looms auth is disabled). Effective on streaming calls, which
		// carry the request context through to the bridge's gRPC calls.
		ctx := server.ContextWithOutgoingAuth(r.Context(), raw, sub)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// envBool reports whether an environment variable is set to a truthy value.
func envBool(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// edgeBearer strips a case-insensitive "Bearer " prefix from an Authorization header.
func edgeBearer(header string) string {
	header = strings.TrimSpace(header)
	if len(header) >= 7 && strings.EqualFold(header[:7], "bearer ") {
		return strings.TrimSpace(header[7:])
	}
	return ""
}

// setupLogger creates a zap logger that writes to a file (or stderr if no file specified).
// IMPORTANT: The logger must NEVER write to stdout because stdout is the MCP stdio transport.
func setupLogger(logFile, logLevel string) *zap.Logger {
	logger, err := buildLogger(logFile, logLevel)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to setup logger: %v\n", err)
		os.Exit(1)
	}
	return logger
}

// buildLogger is the testable core of setupLogger. It returns an error instead
// of calling os.Exit so tests can exercise all code paths.
func buildLogger(logFile, logLevel string) (*zap.Logger, error) {
	level := parseLogLevel(logLevel)

	var output zapcore.WriteSyncer
	if logFile != "" {
		f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600) // #nosec G304 -- log file path from CLI flag
		if err != nil {
			return nil, fmt.Errorf("open log file %s: %w", logFile, err)
		}
		output = zapcore.AddSync(f)
	} else {
		// Write to stderr (not stdout!) as a fallback
		output = zapcore.AddSync(os.Stderr)
	}

	encoderConfig := zap.NewProductionEncoderConfig()
	encoderConfig.TimeKey = "ts"
	encoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder

	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(encoderConfig),
		output,
		level,
	)

	return zap.New(core), nil
}

// parseLogLevel converts a string log level to a zapcore.Level.
func parseLogLevel(logLevel string) zapcore.Level {
	switch logLevel {
	case "debug":
		return zap.DebugLevel
	case "warn":
		return zap.WarnLevel
	case "error":
		return zap.ErrorLevel
	default:
		return zap.InfoLevel
	}
}
