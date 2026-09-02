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

package task

import (
	"context"

	"github.com/teradata-labs/loom/pkg/taskctx"
)

// The ambient task attribution lives in pkg/taskctx, a leaf package that
// imports nothing but "context".
//
// It has to be readable from pkg/shuttle (the human-request store) as well as
// from here, and pkg/shuttle cannot import pkg/task: the chain
// pkg/task -> pkg/communication -> pkg/types -> pkg/shuttle is a cycle. The
// aliases below let callers that already import pkg/task use the familiar names
// without taking a second import.

// Attribution identifies the task that in-flight work belongs to.
// See taskctx.Attribution for the full contract.
type Attribution = taskctx.Attribution

// ContextWithAttribution returns a context carrying the ambient task
// attribution.
func ContextWithAttribution(ctx context.Context, a Attribution) context.Context {
	return taskctx.ContextWithAttribution(ctx, a)
}

// AttributionFromContext returns the ambient task attribution. The boolean is
// false when no task is claimed, which is a normal condition.
func AttributionFromContext(ctx context.Context) (Attribution, bool) {
	return taskctx.AttributionFromContext(ctx)
}

// TaskIDFromContext returns just the claimed task's ID, or "" when none is
// claimed. Callers persist "" as NULL.
func TaskIDFromContext(ctx context.Context) string {
	return taskctx.TaskIDFromContext(ctx)
}

// Creation sources recorded in tasks.created_via.
const (
	CreatedViaUser          = taskctx.CreatedViaUser
	CreatedViaAgent         = taskctx.CreatedViaAgent
	CreatedViaDecompose     = taskctx.CreatedViaDecompose
	CreatedViaSkillTemplate = taskctx.CreatedViaSkillTemplate
	CreatedViaWorkflow      = taskctx.CreatedViaWorkflow
)
