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

package memory

import "time"

// EvictionOptions configures a stale-memory eviction pass over graph_memories.
//
// The primary predicate is conjunctive: a memory is evicted only when ALL of
// these hold — its salience is below MinSalience, it has not been accessed
// within MaxIdle, it is older than MinAge, and it has been reused no more than
// MaxAccessCount times. This targets "learned once, never used again, decayed"
// rows while protecting freshly written lessons via MinAge.
//
// MaxRows, when > 0, additionally caps the number of live memories per agent.
// After the predicate pass, rows beyond the cap (ranked by salience, then
// recency) are evicted as an overflow guard.
//
// Eviction is a soft operation: it sets expires_at to now so the row is
// immediately excluded from recall but remains on disk (reversible until a
// later purge). AgentID == "" sweeps every agent in one pass.
type EvictionOptions struct {
	// AgentID scopes the pass to a single agent. Empty sweeps all agents.
	AgentID string
	// MinSalience is the salience floor; rows at or above it are kept.
	MinSalience float64
	// MaxIdle is the minimum time since last access for a row to be eligible.
	// Rows never accessed are treated as idle.
	MaxIdle time.Duration
	// MinAge protects freshly created rows: only rows older than this are
	// eligible for predicate eviction.
	MinAge time.Duration
	// MaxAccessCount is the upper bound on reuse for a row to be eligible.
	// A row with more accesses than this is considered useful and kept.
	MaxAccessCount int
	// MaxRows caps live memories per agent. 0 disables the cap (predicate-only).
	MaxRows int
}
