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

package jev

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/teradata-labs/loom/pkg/decision"
)

// limiter is a token bucket sized to a per-minute request budget with a
// burst of one second's worth. It is deliberately separate from the LLM slot
// scheduler: that meters generative tokens against a provider quota; this
// meters requests against Jev's published 1,200 rpm. A Client owns one, so a
// process that builds several Clients (one per agent) should share a Client.
type limiter struct {
	mu       sync.Mutex
	rate     float64 // tokens per second
	burst    float64
	tokens   float64
	last     time.Time
	now      func() time.Time
	sleepFor func(context.Context, time.Duration) error
}

func newLimiter(requestsPerMinute float64) *limiter {
	rate := requestsPerMinute / 60
	// One second's worth of tokens is too small once a single logical
	// decision fans out: decision.Chunked sends up to DefaultChunkConcurrency
	// chunks at once, so a burst below that makes one Decide queue against
	// itself and blow the deadline its own caller set. Admit at least one
	// whole fan-out. The average rate is unchanged; only the smoothing is.
	burst := rate
	if burst < decision.DefaultChunkConcurrency {
		burst = decision.DefaultChunkConcurrency
	}
	return &limiter{rate: rate, burst: burst, tokens: burst, last: time.Now(), now: time.Now, sleepFor: sleepCtx}
}

// wait blocks until a token is available or ctx ends.
func (l *limiter) wait(ctx context.Context) error {
	for {
		l.mu.Lock()
		now := l.now()
		elapsed := now.Sub(l.last).Seconds()
		if elapsed < 0 {
			elapsed = 0 // clock moved backwards; never refund tokens for it
		}
		l.last = now
		l.tokens += elapsed * l.rate
		if l.tokens > l.burst {
			l.tokens = l.burst
		}
		if l.tokens >= 1 {
			l.tokens--
			l.mu.Unlock()
			return nil
		}
		need := (1 - l.tokens) / l.rate
		l.mu.Unlock()
		wait := time.Duration(need * float64(time.Second))
		// A live caller gave us a deadline it means to act on. Queueing past
		// it wastes the whole budget and then reports a deadline error the
		// caller cannot tell from a hung provider; returning now lets it fall
		// back with time to spare. A patient caller still waits.
		if !fitsBeforeDeadline(ctx, wait) {
			return fmt.Errorf("%w: throttled, a token is %s away", decision.ErrRateLimited, wait.Round(time.Millisecond))
		}
		if err := l.sleepFor(ctx, wait); err != nil {
			return err
		}
	}
}
