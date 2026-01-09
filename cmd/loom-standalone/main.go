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
package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/fabric"
	"github.com/teradata-labs/loom/pkg/llm/anthropic"
	"github.com/teradata-labs/loom/pkg/llm/bedrock"
	"github.com/teradata-labs/loom/pkg/llm/ollama"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/prompts"
	"github.com/teradata-labs/loom/pkg/server"
	"github.com/teradata-labs/loom/pkg/tui/client"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

// Import the server implementation
// Note: We'd typically import this from cmd/looms but for simplicity
// we'll keep it self-contained

var (
	port        int
	provider    string
	apiKey      string
	model       string
	temperature float64
	maxTokens   int
)

var rootCmd = &cobra.Command{
	Use:   "loom-standalone",
	Short: "Loom Standalone - All-in-one server and TUI",
	Long: `Loom Standalone bundles the gRPC server and TUI client into a single binary.

This is perfect for:
- Quick testing and demos
- Single-user development
- Offline usage
- Easy distribution (just one binary!)

The embedded server starts automatically on a random available port
and the TUI connects to it. No separate server process needed.`,
	Version: "0.1.0",
	Run:     runStandalone,
}

func init() {
	rootCmd.Flags().IntVar(&port, "port", 0, "Server port (0 = random available port)")
	rootCmd.Flags().StringVar(&provider, "provider", "anthropic", "LLM provider (anthropic, bedrock, ollama)")
	rootCmd.Flags().StringVar(&apiKey, "api-key", "", "LLM API key (for anthropic)")
	rootCmd.Flags().StringVar(&model, "model", "", "LLM model (provider-specific)")
	rootCmd.Flags().Float64Var(&temperature, "temperature", 1.0, "LLM temperature")
	rootCmd.Flags().IntVar(&maxTokens, "max-tokens", 4096, "Max tokens")
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func runStandalone(cmd *cobra.Command, args []string) {
	// Find available port if not specified
	if port == 0 {
		var err error
		port, err = findAvailablePort()
		if err != nil {
			log.Fatalf("Failed to find available port: %v", err)
		}
	}

	// Start embedded server
	serverAddr := fmt.Sprintf("localhost:%d", port)
	log.Printf("Starting embedded Loom server on %s", serverAddr)

	grpcServer, err := startEmbeddedServer(port, provider, apiKey, model, temperature, maxTokens)
	if err != nil {
		log.Fatalf("Failed to start embedded server: %v", err)
	}

	// Ensure server shuts down on exit
	defer grpcServer.GracefulStop()

	// Wait for server to be ready
	time.Sleep(500 * time.Millisecond)

	// Connect TUI client to embedded server
	c, err := client.NewClient(client.Config{
		ServerAddr: serverAddr,
	})
	if err != nil {
		log.Fatalf("Failed to connect to embedded server: %v", err)
	}
	defer c.Close()

	// Simple CLI mode for now (TUI integration coming soon)
	log.Printf("Loom standalone server running on %s", serverAddr)
	log.Printf("Connect with: loom --server=%s", serverAddr)
	log.Println("Press Ctrl+C to stop")

	// Wait for interrupt
	sigch := make(chan os.Signal, 1)
	signal.Notify(sigch, os.Interrupt, syscall.SIGTERM)
	<-sigch
	log.Println("Shutting down...")
}

// startEmbeddedServer starts an embedded gRPC server.
func startEmbeddedServer(port int, llmProvider, llmAPIKey, llmModel string, temp float64, maxTok int) (*grpc.Server, error) {
	// Create tracer
	tracer := observability.NewNoOpTracer()

	// Create temporary session store
	dbPath := fmt.Sprintf("/tmp/loom-standalone-%d.db", time.Now().Unix())
	store, err := agent.NewSessionStore(dbPath, tracer)
	if err != nil {
		return nil, fmt.Errorf("failed to create session store: %w", err)
	}

	memory := agent.NewMemoryWithStore(store)

	// Create LLM provider
	var llmProv agent.LLMProvider

	switch llmProvider {
	case "anthropic":
		if llmAPIKey == "" {
			llmAPIKey = os.Getenv("ANTHROPIC_API_KEY")
		}
		if llmAPIKey == "" {
			return nil, fmt.Errorf("anthropic API key required (--api-key or ANTHROPIC_API_KEY env var)")
		}
		if llmModel == "" {
			llmModel = "claude-sonnet-4-5-20250929"
		}
		llmProv = anthropic.NewClient(anthropic.Config{
			APIKey:      llmAPIKey,
			Model:       llmModel,
			MaxTokens:   maxTok,
			Temperature: temp,
		})

	case "bedrock":
		if llmModel == "" {
			llmModel = "anthropic.claude-sonnet-4-5-20250929-v1:0"
		}
		llmProv, err = bedrock.NewClient(bedrock.Config{
			Region:      "us-west-2",
			ModelID:     llmModel,
			MaxTokens:   maxTok,
			Temperature: temp,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to create bedrock client: %w", err)
		}

	case "ollama":
		if llmModel == "" {
			llmModel = "llama3.1"
		}
		llmProv = ollama.NewClient(ollama.Config{
			Endpoint:    "http://localhost:11434",
			Model:       llmModel,
			MaxTokens:   maxTok,
			Temperature: temp,
		})

	default:
		return nil, fmt.Errorf("unsupported provider: %s", llmProvider)
	}

	// Create mock backend
	backend := &mockBackend{}

	// Create promptio registry
	promptRegistry := prompts.NewPromptioRegistry("./prompts")

	// Create agent
	ag := agent.NewAgent(
		backend,
		llmProv,
		agent.WithTracer(tracer),
		agent.WithMemory(memory),
		agent.WithPrompts(promptRegistry),
		agent.WithConfig(&agent.Config{
			MaxTurns:          10,
			MaxToolExecutions: 20,
			EnableTracing:     false,
		}),
	)

	// Create gRPC server
	grpcServer := grpc.NewServer()
	loomService := server.NewServer(ag, store)
	loomv1.RegisterLoomServiceServer(grpcServer, loomService)
	reflection.Register(grpcServer)

	// Start server in background
	addr := fmt.Sprintf(":%d", port)
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("failed to listen: %w", err)
	}

	go func() {
		if err := grpcServer.Serve(lis); err != nil {
			log.Printf("Server error: %v", err)
		}
	}()

	return grpcServer, nil
}

// findAvailablePort finds an available TCP port.
// Uses :0 to let OS allocate an ephemeral port - this is for port discovery, not server binding.
func findAvailablePort() (int, error) {
	// #nosec G102 -- Intentional: :0 lets OS pick ephemeral port for standalone mode
	lis, err := net.Listen("tcp", ":0")
	if err != nil {
		return 0, err
	}
	defer lis.Close()

	return lis.Addr().(*net.TCPAddr).Port, nil
}

// mockBackend is a temporary backend for testing.
type mockBackend struct{}

func (m *mockBackend) Name() string {
	return "mock"
}

func (m *mockBackend) ExecuteQuery(ctx context.Context, query string) (*fabric.QueryResult, error) {
	return &fabric.QueryResult{
		Type: "rows",
		Columns: []fabric.Column{
			{Name: "result", Type: "string"},
		},
		Rows: []map[string]interface{}{
			{"result": "Mock backend - not implemented yet"},
		},
		RowCount: 1,
	}, nil
}

func (m *mockBackend) GetSchema(ctx context.Context, resource string) (*fabric.Schema, error) {
	return &fabric.Schema{
		Name:   resource,
		Type:   "table",
		Fields: []fabric.Field{},
	}, nil
}

func (m *mockBackend) GetMetadata(ctx context.Context, resource string) (map[string]interface{}, error) {
	return map[string]interface{}{}, nil
}

func (m *mockBackend) ListResources(ctx context.Context, filters map[string]string) ([]fabric.Resource, error) {
	return []fabric.Resource{}, nil
}

func (m *mockBackend) Capabilities() *fabric.Capabilities {
	return fabric.NewCapabilities()
}

func (m *mockBackend) Close() error {
	return nil
}

func (m *mockBackend) Ping(ctx context.Context) error {
	return nil
}

func (m *mockBackend) ExecuteCustomOperation(ctx context.Context, op string, params map[string]interface{}) (interface{}, error) {
	return nil, fmt.Errorf("operation %s not supported by mock backend", op)
}
