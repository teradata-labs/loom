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

package agent

import (
	"context"

	"github.com/teradata-labs/loom/pkg/shuttle"
)

// advertisedProjectionKey carries, on a tool call's context, the names of the
// tools the model was shown for the provider call that requested it.
type advertisedProjectionKey struct{}

// withAdvertisedProjection records the provider call's tool projection on the
// context of one of its tool calls. The projection is the final list sent to
// the model: advertisedTools(session) with circuit-breaker-disabled tools
// already removed by recovery.activeTools. A script engine reads it to decide
// what a script may call, so it never re-derives (and so widens) the set.
func withAdvertisedProjection(ctx Context, tools []shuttle.Tool) Context {
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		if t != nil {
			names = append(names, t.Name())
		}
	}
	return &contextWithValue{Context: ctx, key: advertisedProjectionKey{}, val: names}
}

// advertisedProjectionFromContext returns the projection recorded by
// withAdvertisedProjection, and false when there is none (a call that did not
// come from the conversation loop's dispatch).
func advertisedProjectionFromContext(ctx context.Context) ([]string, bool) {
	names, ok := ctx.Value(advertisedProjectionKey{}).([]string)
	return names, ok
}
