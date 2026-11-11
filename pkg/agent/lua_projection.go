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

// parkProjectionContextKey is the HumanRequest.Context key under which a park
// records the names of the tools the model was shown for the provider call
// whose batch parked. ResumeChat restores that projection for the batch's
// calls, so a script resumed after approval sees the set its requesting call
// saw, never a wider one derived at resume time.
const parkProjectionContextKey = "advertised_tools"

// advertisedProjectionKey carries, on a tool call's context, the names of the
// tools the model was shown for the provider call that requested it.
type advertisedProjectionKey struct{}

// withAdvertisedProjection records the provider call's tool projection on the
// context of one of its tool calls. The projection is the final list sent to
// the model: advertisedTools(session) with circuit-breaker-disabled tools
// already removed by recovery.activeTools. A script engine reads it to decide
// what a script may call, so it never re-derives (and so widens) the set.
func withAdvertisedProjection(ctx Context, tools []shuttle.Tool) Context {
	return &contextWithValue{Context: ctx, key: advertisedProjectionKey{}, val: toolNames(tools)}
}

// toolNames returns the names of tools in order, skipping nil entries.
func toolNames(tools []shuttle.Tool) []string {
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		if t != nil {
			names = append(names, t.Name())
		}
	}
	return names
}

// parkedProjection returns the projection a park recorded on hr, and false
// when there is none: a row parked before the projection was recorded, or one
// whose value is not a list of names. The in-memory store hands back the
// []string the park wrote; the SQL stores decode their JSON to
// []interface{}.
func parkedProjection(hr *shuttle.HumanRequest) ([]string, bool) {
	if hr == nil {
		return nil, false
	}
	switch v := hr.Context[parkProjectionContextKey].(type) {
	case []string:
		return append([]string{}, v...), true
	case []interface{}:
		names := make([]string, 0, len(v))
		for _, e := range v {
			s, ok := e.(string)
			if !ok {
				return nil, false
			}
			names = append(names, s)
		}
		return names, true
	}
	return nil, false
}

// restrictToProjection returns the tools of current whose names are in names,
// in current's order: the resume-time set narrowed to what the requesting
// provider call showed the model. A tool advertised since the park, or one
// recovery had removed from the requesting call, is left out.
func restrictToProjection(current []shuttle.Tool, names []string) []shuttle.Tool {
	keep := make(map[string]bool, len(names))
	for _, n := range names {
		keep[n] = true
	}
	out := make([]shuttle.Tool, 0, len(current))
	for _, t := range current {
		if t != nil && keep[t.Name()] {
			out = append(out, t)
		}
	}
	return out
}

// advertisedProjectionFromContext returns the projection recorded by
// withAdvertisedProjection, and false when there is none (a call that did not
// come from the conversation loop's dispatch).
func advertisedProjectionFromContext(ctx context.Context) ([]string, bool) {
	names, ok := ctx.Value(advertisedProjectionKey{}).([]string)
	return names, ok
}
