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
package metaagent

import (
	"context"

	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/observability"
)

// minimalContext is a minimal implementation of agent.Context for meta-agent operations
type minimalContext struct {
	context.Context
	tracer observability.Tracer
}

func (m *minimalContext) Session() *agent.Session {
	return nil
}

func (m *minimalContext) Tracer() observability.Tracer {
	return m.tracer
}

func (m *minimalContext) ProgressCallback() agent.ProgressCallback {
	return nil
}

// newMinimalContextWithTracer creates a minimal agent context with a tracer
func newMinimalContextWithTracer(ctx context.Context, tracer observability.Tracer) agent.Context {
	return &minimalContext{
		Context: ctx,
		tracer:  tracer,
	}
}
