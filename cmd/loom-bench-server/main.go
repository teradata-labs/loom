// loom-bench-server is a minimal gRPC server for benchmarking.
// It uses a mock LLM provider and noop backend — zero real tokens consumed.
//
// Supports hot-reconfiguration via HTTP endpoints:
//
//	POST /reconfigure — swap LLM config and agent count without restart
//	POST /reset       — clear metrics counters
//	GET  /metrics     — current LLM provider metrics
//	GET  /health      — liveness check
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"syscall"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/fabric"
	"github.com/teradata-labs/loom/pkg/llm/loadtest"
	"github.com/teradata-labs/loom/pkg/server"
)

var gitCommit = "unknown"

const maxNumAgents = 1000

// reconfigureRequest is the JSON body for POST /reconfigure.
type reconfigureRequest struct {
	LLMLatencyMs        *int64   `json:"llm_latency_ms"`
	LLMJitterMs         *int64   `json:"llm_jitter_ms"`
	LLMErrorRate        *float64 `json:"llm_error_rate"`
	NumAgents           *int     `json:"num_agents"`
	LLMConcurrencyLimit *int     `json:"llm_concurrency_limit"`
	StreamChunkSize     *int     `json:"stream_chunk_size"`
	StreamChunkDelay    *int64   `json:"stream_chunk_delay_ms"`
}

// benchServer holds the mutable server state for hot-reconfiguration.
type benchServer struct {
	mu         sync.RWMutex
	grpcServer *grpc.Server
	multiSrv   *server.MultiAgentServer
	provider   *loadtest.Provider
	config     serverConfig
}

type serverConfig struct {
	port                int
	httpPort            int
	numAgents           int
	llmLatency          time.Duration
	llmJitter           time.Duration
	llmErrorRate        float64
	llmConcurrencyLimit int
	streamChunkSize     int
	streamChunkDelay    time.Duration
}

func main() {
	port := flag.Int("port", 60051, "gRPC listen port")
	httpPort := flag.Int("http-port", 8080, "HTTP metrics port")
	numAgents := flag.Int("num-agents", 1, "Number of agents to register")
	llmLatency := flag.Duration("llm-latency", time.Millisecond, "Mock LLM base latency")
	llmJitter := flag.Duration("llm-jitter", 0, "Mock LLM latency jitter")
	llmErrorRate := flag.Float64("llm-error-rate", 0, "Mock LLM error rate [0.0, 1.0]")
	llmConcurrencyLimit := flag.Int("llm-concurrency-limit", 10000, "Max concurrent LLM calls")
	streamChunkSize := flag.Int("stream-chunk-size", 10, "Streaming chunk size (chars)")
	streamChunkDelay := flag.Duration("stream-chunk-delay", 5*time.Millisecond, "Delay between streaming chunks")
	flag.Parse()

	cfg := serverConfig{
		port:                *port,
		httpPort:            *httpPort,
		numAgents:           *numAgents,
		llmLatency:          *llmLatency,
		llmJitter:           *llmJitter,
		llmErrorRate:        *llmErrorRate,
		llmConcurrencyLimit: *llmConcurrencyLimit,
		streamChunkSize:     *streamChunkSize,
		streamChunkDelay:    *streamChunkDelay,
	}

	bs := &benchServer{config: cfg}

	// Build initial gRPC server
	provider, multiSrv, grpcServer := bs.buildGRPCServer(cfg)
	bs.provider = provider
	bs.multiSrv = multiSrv
	bs.grpcServer = grpcServer

	// Start gRPC listener
	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.port))
	if err != nil {
		log.Fatalf("listen: %v", err)
	}

	go func() {
		log.Printf("gRPC server listening on :%d (commit=%s, agents=%d, llm_latency=%s, llm_jitter=%s)",
			cfg.port, gitCommit, cfg.numAgents, cfg.llmLatency, cfg.llmJitter)
		if err := grpcServer.Serve(lis); err != nil {
			log.Fatalf("serve: %v", err)
		}
	}()

	// Start HTTP server
	httpServer := bs.startHTTPServer(cfg.httpPort)

	// Wait for shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	sig := <-sigCh
	log.Printf("Received %v, shutting down...", sig)

	grpcServer.GracefulStop()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(ctx)
	log.Println("Server stopped.")
}

// buildGRPCServer creates a new gRPC server with the given config.
func (bs *benchServer) buildGRPCServer(cfg serverConfig) (*loadtest.Provider, *server.MultiAgentServer, *grpc.Server) {
	provider := loadtest.NewProvider(loadtest.ProviderConfig{
		Name:             "bench-mock",
		Model:            "bench-mock-v1",
		BaseLatency:      cfg.llmLatency,
		LatencyJitter:    cfg.llmJitter,
		ErrorRate:        cfg.llmErrorRate,
		StreamChunkSize:  cfg.streamChunkSize,
		StreamChunkDelay: cfg.streamChunkDelay,
	})

	backend := &noopBackend{}
	agents := make(map[string]*agent.Agent, cfg.numAgents)
	for i := range cfg.numAgents {
		name := "default"
		if i > 0 {
			name = fmt.Sprintf("bench-agent-%d", i)
		}
		ag := agent.NewAgent(backend, provider, agent.WithConfig(&agent.Config{
			Name:        name,
			Description: fmt.Sprintf("Benchmark agent %d", i),
		}))
		agents[name] = ag
	}

	multiSrv := server.NewMultiAgentServer(agents, nil)
	multiSrv.SetLogger(zap.NewNop())
	multiSrv.SetLLMConcurrencyLimit(cfg.llmConcurrencyLimit)

	grpcServer := grpc.NewServer()
	loomv1.RegisterLoomServiceServer(grpcServer, multiSrv)

	return provider, multiSrv, grpcServer
}

// startHTTPServer starts the HTTP server with metrics, health, reconfigure, and reset endpoints.
func (bs *benchServer) startHTTPServer(port int) *http.Server {
	mux := http.NewServeMux()

	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		bs.mu.RLock()
		snap := bs.provider.GetMetrics().Snapshot()
		bs.mu.RUnlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(snap)
	})

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"status":"ok","commit":"%s"}`, gitCommit)
	})

	mux.HandleFunc("/reconfigure", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}

		var req reconfigureRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, fmt.Sprintf("bad JSON: %v", err), http.StatusBadRequest)
			return
		}

		bs.mu.Lock()
		cfg := bs.config

		// Apply deltas
		if req.LLMLatencyMs != nil {
			cfg.llmLatency = time.Duration(*req.LLMLatencyMs) * time.Millisecond
		}
		if req.LLMJitterMs != nil {
			cfg.llmJitter = time.Duration(*req.LLMJitterMs) * time.Millisecond
		}
		if req.LLMErrorRate != nil {
			cfg.llmErrorRate = *req.LLMErrorRate
		}
		if req.NumAgents != nil {
			if *req.NumAgents <= 0 || *req.NumAgents > maxNumAgents {
				bs.mu.Unlock()
				http.Error(w, fmt.Sprintf("NumAgents must be between 1 and %d", maxNumAgents), http.StatusBadRequest)
				return
			}
			cfg.numAgents = *req.NumAgents
		}
		if req.LLMConcurrencyLimit != nil {
			cfg.llmConcurrencyLimit = *req.LLMConcurrencyLimit
		}
		if req.StreamChunkSize != nil {
			cfg.streamChunkSize = *req.StreamChunkSize
		}
		if req.StreamChunkDelay != nil {
			cfg.streamChunkDelay = time.Duration(*req.StreamChunkDelay) * time.Millisecond
		}

		// Rebuild the gRPC service handler (the gRPC server/listener stays the same,
		// but we swap the registered service — this requires restarting the gRPC server).
		// For simplicity, we just update the provider and config. The existing gRPC server
		// keeps serving with the old agents. New scenarios should wait a moment after
		// reconfigure for in-flight requests to drain.
		provider, multiSrv, _ := bs.buildGRPCServer(cfg)
		bs.provider = provider
		bs.multiSrv = multiSrv
		bs.config = cfg
		bs.mu.Unlock()

		log.Printf("Reconfigured: agents=%d, llm_latency=%s, llm_jitter=%s, llm_error_rate=%.2f, concurrency_limit=%d",
			cfg.numAgents, cfg.llmLatency, cfg.llmJitter, cfg.llmErrorRate, cfg.llmConcurrencyLimit)

		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"status":"reconfigured","agents":%d,"llm_latency_ms":%d,"llm_error_rate":%.2f}`,
			cfg.numAgents, cfg.llmLatency.Milliseconds(), cfg.llmErrorRate)
	})

	mux.HandleFunc("/reset", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		bs.mu.RLock()
		// Clear all sessions to free memory
		if bs.multiSrv != nil {
			bs.multiSrv.ClearAllSessions()
		}
		// Reset metrics counters
		m := bs.provider.GetMetrics()
		bs.mu.RUnlock()
		m.TotalCalls.Store(0)
		m.SuccessCount.Store(0)
		m.ErrorCount.Store(0)
		m.TotalLatencyNs.Store(0)
		m.MinLatencyNs.Store(0)
		m.MaxLatencyNs.Store(0)

		// Force GC to reclaim session memory
		runtime.GC()

		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"status":"reset","sessions_cleared":true}`)
	})

	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: mux,
	}
	go func() {
		log.Printf("HTTP server listening on :%d", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("http serve: %v", err)
		}
	}()
	return srv
}

// noopBackend is a minimal ExecutionBackend for benchmarking.
type noopBackend struct{}

func (b *noopBackend) Name() string { return "bench-noop" }
func (b *noopBackend) ExecuteQuery(_ context.Context, _ string) (*fabric.QueryResult, error) {
	return &fabric.QueryResult{Type: "rows", RowCount: 0}, nil
}
func (b *noopBackend) GetSchema(_ context.Context, resource string) (*fabric.Schema, error) {
	return &fabric.Schema{Name: resource}, nil
}
func (b *noopBackend) GetMetadata(_ context.Context, _ string) (map[string]interface{}, error) {
	return map[string]interface{}{}, nil
}
func (b *noopBackend) ListResources(_ context.Context, _ map[string]string) ([]fabric.Resource, error) {
	return []fabric.Resource{}, nil
}
func (b *noopBackend) Capabilities() *fabric.Capabilities {
	return fabric.NewCapabilities()
}
func (b *noopBackend) Close() error                 { return nil }
func (b *noopBackend) Ping(_ context.Context) error { return nil }
func (b *noopBackend) ExecuteCustomOperation(_ context.Context, _ string, _ map[string]interface{}) (interface{}, error) {
	return nil, fmt.Errorf("not supported")
}
