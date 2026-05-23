package loadtest

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/types"
)

func TestProvider_ImplementsInterfaces(t *testing.T) {
	p := NewProvider(DefaultConfig())
	var _ types.LLMProvider = p
	var _ types.StreamingLLMProvider = p
}

func TestProvider_NameAndModel(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Name = "test-provider"
	cfg.Model = "test-model-v2"
	p := NewProvider(cfg)

	assert.Equal(t, "test-provider", p.Name())
	assert.Equal(t, "test-model-v2", p.Model())
}

func TestProvider_Chat_DefaultResponse(t *testing.T) {
	cfg := DefaultConfig()
	cfg.BaseLatency = 0
	cfg.LatencyJitter = 0
	p := NewProvider(cfg)

	resp, err := p.Chat(context.Background(), []types.Message{
		{Role: "user", Content: "hello world"},
	}, nil)

	require.NoError(t, err)
	assert.Contains(t, resp.Content, "hello world")
	assert.Equal(t, "end_turn", resp.StopReason)
	assert.Equal(t, cfg.InputTokensPerMessage, resp.Usage.InputTokens)
	assert.Equal(t, cfg.OutputTokens, resp.Usage.OutputTokens)
}

func TestProvider_Chat_CannedResponse(t *testing.T) {
	cfg := DefaultConfig()
	cfg.BaseLatency = 0
	cfg.LatencyJitter = 0
	cfg.Response = &types.LLMResponse{
		Content:    "canned answer",
		StopReason: "stop",
		Usage:      types.Usage{InputTokens: 10, OutputTokens: 20},
	}
	p := NewProvider(cfg)

	resp, err := p.Chat(context.Background(), nil, nil)
	require.NoError(t, err)
	assert.Equal(t, "canned answer", resp.Content)
}

func TestProvider_Chat_ResponseFunc(t *testing.T) {
	cfg := DefaultConfig()
	cfg.BaseLatency = 0
	cfg.LatencyJitter = 0
	cfg.ResponseFunc = func(messages []types.Message, tools []shuttle.Tool) *types.LLMResponse {
		return &types.LLMResponse{
			Content: fmt.Sprintf("dynamic: %d messages, %d tools", len(messages), len(tools)),
		}
	}
	// Also set Response to prove ResponseFunc takes precedence
	cfg.Response = &types.LLMResponse{Content: "should not appear"}
	p := NewProvider(cfg)

	resp, err := p.Chat(context.Background(), []types.Message{
		{Role: "user", Content: "a"},
		{Role: "assistant", Content: "b"},
	}, []shuttle.Tool{&shuttle.MockTool{MockName: "tool1"}})

	require.NoError(t, err)
	assert.Equal(t, "dynamic: 2 messages, 1 tools", resp.Content)
}

func TestProvider_Chat_Latency(t *testing.T) {
	cfg := DefaultConfig()
	cfg.BaseLatency = 50 * time.Millisecond
	cfg.LatencyJitter = 0
	cfg.ErrorRate = 0
	p := NewProvider(cfg)

	start := time.Now()
	_, err := p.Chat(context.Background(), []types.Message{
		{Role: "user", Content: "test"},
	}, nil)
	elapsed := time.Since(start)

	require.NoError(t, err)
	assert.GreaterOrEqual(t, elapsed, 50*time.Millisecond)
}

func TestProvider_Chat_LatencyJitter(t *testing.T) {
	cfg := DefaultConfig()
	cfg.BaseLatency = 10 * time.Millisecond
	cfg.LatencyJitter = 50 * time.Millisecond
	cfg.ErrorRate = 0
	p := NewProvider(cfg)

	// Run multiple calls — they should vary in latency
	var durations []time.Duration
	for range 20 {
		start := time.Now()
		_, err := p.Chat(context.Background(), []types.Message{
			{Role: "user", Content: "test"},
		}, nil)
		require.NoError(t, err)
		durations = append(durations, time.Since(start))
	}

	// At least some variation should exist
	min, max := durations[0], durations[0]
	for _, d := range durations[1:] {
		if d < min {
			min = d
		}
		if d > max {
			max = d
		}
	}
	// With 50ms jitter over 20 calls, expect at least 10ms spread
	assert.Greater(t, max-min, 5*time.Millisecond, "expected jitter to produce latency variation")
}

func TestProvider_Chat_ErrorInjection(t *testing.T) {
	tests := []struct {
		name      string
		errorRate float64
		wantErr   bool
	}{
		{"zero error rate", 0.0, false},
		{"100% error rate", 1.0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.BaseLatency = 0
			cfg.LatencyJitter = 0
			cfg.ErrorRate = tt.errorRate
			p := NewProvider(cfg)

			_, err := p.Chat(context.Background(), []types.Message{
				{Role: "user", Content: "test"},
			}, nil)

			if tt.wantErr {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), "simulated provider error")
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestProvider_Chat_CustomErrorMessage(t *testing.T) {
	cfg := DefaultConfig()
	cfg.BaseLatency = 0
	cfg.LatencyJitter = 0
	cfg.ErrorRate = 1.0
	cfg.ErrorMessage = "429 Too Many Requests"
	p := NewProvider(cfg)

	_, err := p.Chat(context.Background(), nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "429 Too Many Requests")
}

func TestProvider_Chat_ContextCancellation(t *testing.T) {
	cfg := DefaultConfig()
	cfg.BaseLatency = 5 * time.Second // Long enough to be cancelled
	cfg.LatencyJitter = 0
	p := NewProvider(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := p.Chat(ctx, []types.Message{
		{Role: "user", Content: "test"},
	}, nil)
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, elapsed, 1*time.Second, "should have cancelled quickly")
}

func TestProvider_Chat_ErrorRate_Probabilistic(t *testing.T) {
	cfg := DefaultConfig()
	cfg.BaseLatency = 0
	cfg.LatencyJitter = 0
	cfg.ErrorRate = 0.5
	p := NewProvider(cfg)

	successes := 0
	failures := 0
	n := 1000

	for range n {
		_, err := p.Chat(context.Background(), []types.Message{
			{Role: "user", Content: "test"},
		}, nil)
		if err != nil {
			failures++
		} else {
			successes++
		}
	}

	// With 50% error rate over 1000 calls, expect roughly 500 each
	// Allow wide margin (30%-70%) to avoid flaky tests
	assert.Greater(t, successes, 300, "expected some successes with 50%% error rate")
	assert.Greater(t, failures, 300, "expected some failures with 50%% error rate")
}

func TestProvider_ChatStream(t *testing.T) {
	cfg := DefaultConfig()
	cfg.BaseLatency = 0
	cfg.LatencyJitter = 0
	cfg.StreamChunkSize = 5
	cfg.StreamChunkDelay = 1 * time.Millisecond
	cfg.Response = &types.LLMResponse{
		Content:    "hello world from stream",
		StopReason: "end_turn",
	}
	p := NewProvider(cfg)

	var chunks []string
	var mu sync.Mutex
	callback := func(token string) {
		mu.Lock()
		chunks = append(chunks, token)
		mu.Unlock()
	}

	resp, err := p.ChatStream(context.Background(), []types.Message{
		{Role: "user", Content: "test"},
	}, nil, callback)

	require.NoError(t, err)
	assert.Equal(t, "hello world from stream", resp.Content)

	// Verify chunks were delivered
	mu.Lock()
	defer mu.Unlock()
	assert.Greater(t, len(chunks), 1, "should have received multiple chunks")

	// Reconstruct and verify
	var sb strings.Builder
	for _, c := range chunks {
		sb.WriteString(c)
	}
	reconstructed := sb.String()
	assert.Equal(t, "hello world from stream", reconstructed)
}

func TestProvider_ChatStream_ContextCancellation(t *testing.T) {
	cfg := DefaultConfig()
	cfg.BaseLatency = 0
	cfg.LatencyJitter = 0
	cfg.StreamChunkSize = 1 // 1 char per chunk = many chunks
	cfg.StreamChunkDelay = 50 * time.Millisecond
	cfg.Response = &types.LLMResponse{
		Content: "this is a long string that should get cancelled during streaming",
	}
	p := NewProvider(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	chunkCount := 0
	_, err := p.ChatStream(ctx, nil, nil, func(token string) {
		chunkCount++
	})

	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Greater(t, chunkCount, 0, "should have received some chunks before cancellation")
}

func TestProvider_ChatStream_NilCallback(t *testing.T) {
	cfg := DefaultConfig()
	cfg.BaseLatency = 0
	cfg.LatencyJitter = 0
	p := NewProvider(cfg)

	resp, err := p.ChatStream(context.Background(), []types.Message{
		{Role: "user", Content: "test"},
	}, nil, nil)

	require.NoError(t, err)
	assert.NotEmpty(t, resp.Content)
}

func TestProvider_Metrics(t *testing.T) {
	cfg := DefaultConfig()
	cfg.BaseLatency = 0
	cfg.LatencyJitter = 0
	cfg.ErrorRate = 0
	p := NewProvider(cfg)

	// Make some calls
	for range 10 {
		_, _ = p.Chat(context.Background(), []types.Message{
			{Role: "user", Content: "test"},
		}, nil)
	}

	snap := p.GetMetrics().Snapshot()
	assert.Equal(t, int64(10), snap.TotalCalls)
	assert.Equal(t, int64(10), snap.SuccessCount)
	assert.Equal(t, int64(0), snap.ErrorCount)
	assert.GreaterOrEqual(t, snap.MaxLatency, time.Duration(0))
}

func TestProvider_Metrics_WithErrors(t *testing.T) {
	cfg := DefaultConfig()
	cfg.BaseLatency = 0
	cfg.LatencyJitter = 0
	cfg.ErrorRate = 1.0
	p := NewProvider(cfg)

	for range 5 {
		_, _ = p.Chat(context.Background(), nil, nil)
	}

	snap := p.GetMetrics().Snapshot()
	assert.Equal(t, int64(5), snap.TotalCalls)
	assert.Equal(t, int64(0), snap.SuccessCount)
	assert.Equal(t, int64(5), snap.ErrorCount)
}

// TestProvider_ConcurrentSafety verifies thread safety under -race.
func TestProvider_ConcurrentSafety(t *testing.T) {
	cfg := DefaultConfig()
	cfg.BaseLatency = 1 * time.Millisecond
	cfg.LatencyJitter = 2 * time.Millisecond
	cfg.ErrorRate = 0.1
	p := NewProvider(cfg)

	const goroutines = 50
	const callsPerGoroutine = 20

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for range goroutines {
		go func() {
			defer wg.Done()
			for range callsPerGoroutine {
				_, _ = p.Chat(context.Background(), []types.Message{
					{Role: "user", Content: "concurrent test"},
				}, nil)
			}
		}()
	}

	wg.Wait()

	snap := p.GetMetrics().Snapshot()
	assert.Equal(t, int64(goroutines*callsPerGoroutine), snap.TotalCalls)
	assert.Equal(t, snap.SuccessCount+snap.ErrorCount, snap.TotalCalls)
}

// TestProvider_ConcurrentStream verifies streaming thread safety under -race.
func TestProvider_ConcurrentStream(t *testing.T) {
	cfg := DefaultConfig()
	cfg.BaseLatency = 0
	cfg.LatencyJitter = 1 * time.Millisecond
	cfg.StreamChunkSize = 5
	cfg.StreamChunkDelay = 0
	cfg.ErrorRate = 0
	cfg.Response = &types.LLMResponse{Content: "stream test content"}
	p := NewProvider(cfg)

	const goroutines = 30

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for range goroutines {
		go func() {
			defer wg.Done()
			_, err := p.ChatStream(context.Background(), []types.Message{
				{Role: "user", Content: "test"},
			}, nil, func(token string) {
				// Just consume tokens
				_ = token
			})
			assert.NoError(t, err)
		}()
	}

	wg.Wait()

	snap := p.GetMetrics().Snapshot()
	assert.Equal(t, int64(goroutines), snap.TotalCalls)
}

func TestProvider_Metrics_Snapshot_Empty(t *testing.T) {
	p := NewProvider(DefaultConfig())
	snap := p.GetMetrics().Snapshot()

	assert.Equal(t, int64(0), snap.TotalCalls)
	assert.Equal(t, time.Duration(0), snap.AvgLatency)
	assert.Equal(t, time.Duration(0), snap.MinLatency) // sentinel cleared
}

func BenchmarkProvider_Chat(b *testing.B) {
	cfg := DefaultConfig()
	cfg.BaseLatency = 0
	cfg.LatencyJitter = 0
	p := NewProvider(cfg)

	msgs := []types.Message{{Role: "user", Content: "bench"}}
	ctx := context.Background()

	b.ResetTimer()
	for b.Loop() {
		_, _ = p.Chat(ctx, msgs, nil)
	}
}

func BenchmarkProvider_Chat_Concurrent(b *testing.B) {
	cfg := DefaultConfig()
	cfg.BaseLatency = 0
	cfg.LatencyJitter = 0
	p := NewProvider(cfg)

	msgs := []types.Message{{Role: "user", Content: "bench"}}
	ctx := context.Background()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = p.Chat(ctx, msgs, nil)
		}
	})
}
