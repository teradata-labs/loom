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

package failurerecovery

// Test-only shims that expose the unexported write/read path to the external
// integration test (package failurerecovery_test), which must live in a separate
// package to break the import cycle it would otherwise create by importing the
// Postgres store (postgres -> agent -> failurerecovery). This file carries no
// build tag, so it compiles in every test build; it adds no production surface.

import (
	"context"

	"github.com/teradata-labs/loom/pkg/memory"
)

// LearnForTest drives the unexported learn path with a crafted (problem,
// solution) pair — the only way to force the evolved-solution supersede branch,
// which the LLM/rule abstractors cannot be made to emit deterministically. The
// sink is any value exposing AddEvent (structurally an eventSink); pass nil to
// skip event capture.
func (e *Engine) LearnForTest(ctx context.Context, agentID, class, tool, problem, solution string, sink interface {
	AddEvent(name string, attrs map[string]interface{})
}) {
	var s eventSink
	if sink != nil {
		s = sink
	}
	e.learn(ctx, agentID, failureRecord{
		class:    class,
		tool:     tool,
		problem:  problem,
		solution: solution,
	}, s)
}

// RecallForTest exposes the unexported reactive recall so the integration test
// can verify which lesson the real store returns after a supersede.
func (e *Engine) RecallForTest(ctx context.Context, agentID, queryText, class, tool string, threshold float32) []*memory.Memory {
	return e.recall(ctx, agentID, queryText, class, tool, threshold)
}

// ReuseCountOf exposes the unexported reuse_count reader for assertions.
func ReuseCountOf(m *memory.Memory) int { return reuseCountOf(m) }
