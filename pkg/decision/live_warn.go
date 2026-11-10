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
	"sync"
	"time"

	"go.uber.org/zap"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// LiveErrorWarnInterval is the least time between two live-path decider
// error warnings for one site. A degraded decider fails every live visit;
// one line per site per interval connects slower turns to the decider
// without flooding the log.
const LiveErrorWarnInterval = time.Minute

// liveErrorWarner logs ERROR outcomes on the live path, at most once per
// interval per site, and counts the ones it suppressed in between. It is
// safe for concurrent use.
type liveErrorWarner struct {
	interval time.Duration
	now      func() time.Time
	logger   func() *zap.Logger

	mu    sync.Mutex
	sites map[string]*liveErrorState
}

type liveErrorState struct {
	last       time.Time
	suppressed int64
}

func newLiveErrorWarner(interval time.Duration, now func() time.Time, logger func() *zap.Logger) *liveErrorWarner {
	return &liveErrorWarner{interval: interval, now: now, logger: logger, sites: make(map[string]*liveErrorState)}
}

// defaultLiveErrorWarner is shared by every Router in the process, so the
// rate limit holds per site however many agents share a decider. The logger
// is read at log time, so a zap.ReplaceGlobals after startup is honoured.
var defaultLiveErrorWarner = newLiveErrorWarner(LiveErrorWarnInterval, time.Now, zap.L)

// observe logs out when it is an ERROR outcome and the site has not logged
// within the interval. Other paths (DECIDER, FALLBACK, BUDGET, DISABLED) are
// not errors and are never logged.
func (w *liveErrorWarner) observe(site string, out Outcome) {
	if out.Path != loomv1.DecisionPath_DECISION_PATH_ERROR {
		return
	}
	w.mu.Lock()
	st, ok := w.sites[site]
	if !ok {
		st = &liveErrorState{}
		w.sites[site] = st
	}
	now := w.now()
	if !st.last.IsZero() && now.Sub(st.last) < w.interval {
		st.suppressed++
		w.mu.Unlock()
		return
	}
	suppressed := st.suppressed
	st.suppressed = 0
	st.last = now
	w.mu.Unlock()

	w.logger().Warn("decision: live decider call failed; the site's existing mechanism decides",
		zap.String("site", site),
		zap.Error(out.Err),
		zap.Duration("latency", out.Latency),
		zap.Int64("suppressed_since_last", suppressed),
		zap.Duration("warn_interval", w.interval))
}

// DecideLive is Decide for a live call site: a site whose band can act and
// whose caller waits for the answer. An ERROR outcome is also logged as a
// zap Warn naming the site and the error, rate limited per site
// (LiveErrorWarnInterval), because on the live path a failing decider costs
// the turn its latency budget and the reason is otherwise only on the shadow
// row. Shadow evaluations call Decide, which never logs.
func (r *Router) DecideLive(ctx context.Context, req *loomv1.DecisionRequest) Outcome {
	out := r.Decide(ctx, req)
	site := ""
	if req != nil {
		site = req.Site
	}
	w := r.liveWarn
	if w == nil {
		w = defaultLiveErrorWarner
	}
	w.observe(site, out)
	return out
}
