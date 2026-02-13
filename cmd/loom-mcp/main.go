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
// between MCP clients (like Claude Desktop, VS Code) and a running Loom server.
//
// It communicates with MCP clients over stdio (JSON-RPC) and connects to a
// running looms server via gRPC. All Loom capabilities are exposed as MCP tools,
// and MCP Apps UI resources (like the conversation viewer) are served as resources.
//
// Usage:
//
//	loom-mcp --grpc-addr localhost:60051
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
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/teradata-labs/loom/internal/version"
	"github.com/teradata-labs/loom/pkg/mcp/apps"
	"github.com/teradata-labs/loom/pkg/mcp/protocol"
	"github.com/teradata-labs/loom/pkg/mcp/server"
	"github.com/teradata-labs/loom/pkg/mcp/transport"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

const serverName = "loom-mcp"

func main() {
	grpcAddr := flag.String("grpc-addr", "localhost:60051", "Address of the running looms gRPC server")
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

	// Connect to running looms via gRPC
	bridge, err := server.NewLoomBridge(*grpcAddr, uiRegistry, logger, bridgeOpts...)
	if err != nil {
		logger.Fatal("failed to connect to looms", zap.Error(err))
	}
	defer bridge.Close()
	logger.Info("connected to looms", zap.String("addr", *grpcAddr))

	// Create MCP server with bridge as provider
	mcpServer := server.NewMCPServer(serverName, version.Get(), logger,
		server.WithToolProvider(bridge),
		server.WithResourceProvider(bridge),
		server.WithExtensions(protocol.ServerAppsExtension()),
	)

	// Wire MCP server to bridge so app mutations trigger resource list change notifications.
	bridge.SetMCPServer(mcpServer)

	// Create stdio transport (reads from stdin, writes to stdout)
	stdioTransport := transport.NewStdioServerTransport(os.Stdin, os.Stdout)

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

	// Run the MCP server
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
