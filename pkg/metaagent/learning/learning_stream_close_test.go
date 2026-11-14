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
package learning

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	_ "github.com/teradata-labs/loom/internal/sqlitedriver"
	"github.com/teradata-labs/loom/pkg/communication"
	"github.com/teradata-labs/loom/pkg/observability"
)

// patternMetricsStream is a server stream that records what is sent.
type patternMetricsStream struct {
	grpc.ServerStream
	ctx  context.Context
	sent chan *loomv1.PatternMetricEvent
}

func (s *patternMetricsStream) Context() context.Context { return s.ctx }

func (s *patternMetricsStream) Send(e *loomv1.PatternMetricEvent) error {
	s.sent <- e
	return nil
}

// TestStreamPatternMetrics_EndsWhenBusClosesSubscription checks that the
// stream ends with Unavailable when the bus closes its subscription (as on
// server shutdown, where the bus is closed before the gRPC server stops),
// instead of reading the closed channel's nil message and panicking.
func TestStreamPatternMetrics_EndsWhenBusClosesSubscription(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, InitSelfImprovementSchema(context.Background(), db, observability.NewNoOpTracer()))

	collector, err := NewMetricsCollector(":memory:", observability.NewNoOpTracer())
	require.NoError(t, err)
	t.Cleanup(func() { _ = collector.Close() })

	bus := communication.NewMessageBus(nil, nil, nil, zaptest.NewLogger(t))
	tracker := NewPatternEffectivenessTracker(db, observability.NewNoOpTracer(), bus, time.Hour, 5*time.Minute)
	agent, err := NewLearningAgent(db, observability.NewNoOpTracer(),
		NewLearningEngine(collector, observability.NewNoOpTracer()), tracker, AutonomyManual, time.Hour)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := &patternMetricsStream{ctx: ctx, sent: make(chan *loomv1.PatternMetricEvent, 10)}

	done := make(chan error, 1)
	go func() { done <- agent.StreamPatternMetrics(&loomv1.StreamPatternMetricsRequest{}, stream) }()

	require.Eventually(t, func() bool {
		return len(bus.GetSubscriptionsByAgent("learning-agent")) == 1
	}, 5*time.Second, 10*time.Millisecond, "stream did not subscribe")

	require.NoError(t, bus.Close())

	select {
	case err := <-done:
		require.Error(t, err)
		assert.Equal(t, codes.Unavailable, status.Code(err), "got %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("StreamPatternMetrics did not return after the bus closed its subscription")
	}
	assert.Empty(t, stream.sent, "no event is sent for the closed channel")
}
