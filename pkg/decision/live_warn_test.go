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

package decision

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// scriptedDecider answers every request with resp or fails with err.
type scriptedDecider struct {
	resp *loomv1.DecisionResponse
	err  error
}

func (s scriptedDecider) Decide(context.Context, *loomv1.DecisionRequest) (*loomv1.DecisionResponse, error) {
	return s.resp, s.err
}
func (scriptedDecider) Name() string  { return "scripted" }
func (scriptedDecider) Model() string { return "" }

// fakeClock is a settable clock for the warner's rate limit.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// liveWarnRouter returns a Router over d whose live warnings go to an
// observer, with the rate limit on a fake clock.
func liveWarnRouter(d Decider) (*Router, *observer.ObservedLogs, *fakeClock) {
	core, logs := observer.New(zapcore.WarnLevel)
	logger := zap.New(core)
	clock := &fakeClock{t: time.Unix(1_800_000_000, 0)}
	r := NewRouter(d)
	r.liveWarn = newLiveErrorWarner(LiveErrorWarnInterval, clock.now, func() *zap.Logger { return logger })
	return r, logs, clock
}

func liveWarnRequest(t *testing.T, site string) *loomv1.DecisionRequest {
	t.Helper()
	req, err := NewRequest(site, "state", map[string]*loomv1.DecisionQuestion{"q": Noul("true?")})
	require.NoError(t, err)
	return req
}

func TestDecideLiveWarnsOnceOnError(t *testing.T) {
	t.Parallel()
	down := errors.New("decider down")
	r, logs, _ := liveWarnRouter(scriptedDecider{err: down})

	out := r.DecideLive(context.Background(), liveWarnRequest(t, "workflow.branch"))
	assert.Equal(t, loomv1.DecisionPath_DECISION_PATH_ERROR, out.Path)
	assert.ErrorIs(t, out.Err, down)

	entries := logs.All()
	require.Len(t, entries, 1)
	e := entries[0]
	assert.Equal(t, zapcore.WarnLevel, e.Level)
	fields := e.ContextMap()
	assert.Equal(t, "workflow.branch", fields["site"])
	assert.Equal(t, "decider down", fields["error"])
	assert.Equal(t, int64(0), fields["suppressed_since_last"])
}

func TestDecideLiveRateLimitsABurstPerSite(t *testing.T) {
	t.Parallel()
	r, logs, clock := liveWarnRouter(scriptedDecider{err: errors.New("decider down")})
	ctx := context.Background()

	// A concurrent burst at one site within the interval logs once.
	const burst = 50
	var wg sync.WaitGroup
	for range burst {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.DecideLive(ctx, liveWarnRequest(t, "stage.validation"))
		}()
	}
	wg.Wait()
	require.Len(t, logs.All(), 1, "a burst within the interval logs once")

	// Another site has its own limit.
	r.DecideLive(ctx, liveWarnRequest(t, "skill.route"))
	require.Len(t, logs.All(), 2, "each site is limited on its own")

	// Still inside the interval: suppressed.
	clock.advance(LiveErrorWarnInterval - time.Second)
	r.DecideLive(ctx, liveWarnRequest(t, "stage.validation"))
	require.Len(t, logs.All(), 2)

	// After the interval the next error logs, with the suppressed count.
	clock.advance(2 * time.Second)
	r.DecideLive(ctx, liveWarnRequest(t, "stage.validation"))
	entries := logs.FilterField(zap.String("site", "stage.validation")).All()
	require.Len(t, entries, 2)
	assert.Equal(t, int64(burst), entries[1].ContextMap()["suppressed_since_last"],
		"the burst's other %d errors plus the one inside the interval", burst-1)
}

func TestDecideLiveLogsOnlyErrors(t *testing.T) {
	t.Parallel()
	answered := &loomv1.DecisionResponse{Answers: map[string]*loomv1.DecisionAnswer{
		"q": {Kind: &loomv1.DecisionAnswer_Noul{Noul: &loomv1.NoulAnswer{Probability: 0.99}}},
	}}
	tests := []struct {
		name string
		d    Decider
		opts []RouterOption
		want loomv1.DecisionPath
	}{
		{name: "disabled", d: nil, want: loomv1.DecisionPath_DECISION_PATH_DISABLED},
		{name: "off", d: Off{}, want: loomv1.DecisionPath_DECISION_PATH_DISABLED},
		{name: "fallback below band", d: scriptedDecider{resp: answered}, want: loomv1.DecisionPath_DECISION_PATH_FALLBACK},
		{name: "decider", d: scriptedDecider{resp: answered},
			opts: []RouterOption{WithBand("s", Band{ActMin: 0.5})}, want: loomv1.DecisionPath_DECISION_PATH_DECIDER},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r, logs, _ := liveWarnRouter(tt.d)
			for _, o := range tt.opts {
				o(r)
			}
			out := r.DecideLive(context.Background(), liveWarnRequest(t, "s"))
			require.Equal(t, tt.want, out.Path, "err: %v", out.Err)
			assert.Empty(t, logs.All())
		})
	}
}

func TestDecideDoesNotWarn(t *testing.T) {
	t.Parallel()
	r, logs, _ := liveWarnRouter(scriptedDecider{err: errors.New("decider down")})
	out := r.Decide(context.Background(), liveWarnRequest(t, "s"))
	require.Equal(t, loomv1.DecisionPath_DECISION_PATH_ERROR, out.Path)
	assert.Empty(t, logs.All(), "shadow evaluations go through Decide, which never logs")
}
