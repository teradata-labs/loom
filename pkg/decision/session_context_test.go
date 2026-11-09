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
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/teradata-labs/loom/pkg/session"
)

// The conversation loop puts the agent session on the context with
// pkg/session, not with WithSessionID. Shadow rows and budgets must see it,
// or every row from a live turn is keyed to "".
func TestSessionIDFromContextFallsBackToAgentSession(t *testing.T) {
	assert.Equal(t, "", SessionIDFromContext(context.Background()))
	assert.Equal(t, "", SessionIDFromContext(nil)) //nolint:staticcheck // nil is part of the contract

	agentCtx := session.WithSessionID(context.Background(), "sess-agent")
	assert.Equal(t, "sess-agent", SessionIDFromContext(agentCtx), "agent session is used when no decision session is set")

	both := WithSessionID(agentCtx, "sess-decision")
	assert.Equal(t, "sess-decision", SessionIDFromContext(both), "an explicit decision session wins")

	emptyOverride := WithSessionID(agentCtx, "")
	assert.Equal(t, "sess-agent", SessionIDFromContext(emptyOverride), "an empty explicit value does not hide the agent session")
}
