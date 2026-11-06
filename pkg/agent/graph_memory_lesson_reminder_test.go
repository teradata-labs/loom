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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/memory"
	"github.com/teradata-labs/loom/pkg/types"
)

// F2: the error-time lesson reaches the model as a user-role
// <system-reminder>, never at system role. A system message mid-conversation
// is hoisted ahead of every message by the Anthropic client and breaks every
// message-level cache breakpoint (see withReminder in runConversationLoop).
func TestInjectErrorLessonsIsUserRoleReminder(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	ctx := context.Background()
	_, err := store.Remember(ctx, &memory.Memory{
		AgentID:    fleetLessonAgentID,
		Content:    "numeric overflow inserting CC_Number into an INTEGER column — declare the column BIGINT",
		MemoryType: memory.MemoryTypeLesson,
		Source:     "lesson_mined",
		Salience:   0.9,
	})
	require.NoError(t, err)
	a := &Agent{
		graphMemoryStore:  store,
		config:            &Config{Name: "runner-reminder"},
		graphMemoryConfig: &loomv1.GraphMemoryConfig{Enabled: true, FleetLessonSharing: true},
	}
	session := &types.Session{ID: "s-reminder"}
	session.AddMessage(ctx, types.Message{Role: "user", Content: "load the table"})
	session.AddMessage(ctx, callMsg("1", "execute_statement", "INSERT INTO vt SELECT 1"))
	session.AddMessage(ctx, resultMsg("1", false, "numeric overflow"))

	a.injectErrorLessons(ctx, session, []string{"Error 2616: numeric overflow occurred during computation"})

	msgs := session.GetMessages()
	require.Len(t, msgs, 4)
	got := msgs[3]
	assert.NotEqual(t, "system", got.Role, "never system role")
	assert.Equal(t, roleLessonReminder, got.Role,
		"persisted under its own role, so nothing mistakes it for the human's turn")
	assert.True(t, IsSyntheticWireUserRole(got.Role))
	assert.Equal(t, "user", normalizeWireRoles(msgs)[3].Role, "the provider sees a user-role message")
	assert.True(t, strings.HasPrefix(got.Content, "<system-reminder>\n"), got.Content)
	assert.True(t, strings.HasSuffix(got.Content, "\n</system-reminder>"), got.Content)
	assert.Contains(t, got.Content, "BIGINT")
	assert.Contains(t, got.Content, "[Verified Lessons — matched to the error just hit]")
}
