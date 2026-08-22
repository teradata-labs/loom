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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/teradata-labs/loom/pkg/types"
)

// The extraction prompt must carry the lesson lane: assistant-side
// error→fix discoveries are the method knowledge fleets need to transfer
// (a 30-wave study measured recall of task-echo memories doing nothing for
// the dominant failure mode, because the old prompt discarded error→fix
// material outright).
func TestExtractionPromptHasLessonLane(t *testing.T) {
	msgs := []types.Message{{Role: "user", Content: "create the volatile table"}}
	p := buildGraphMemoryExtractionPrompt(msgs, 10, nil, nil)

	assert.Contains(t, p, "LESSONS:", "lesson lane must exist")
	assert.Contains(t, p, `memory_type "lesson"`, "lessons must be typed")
	assert.Contains(t, p, "never chatter", "error+resolution must be exempt from the chatter exclusion")
	assert.NotContains(t, p, "IGNORE the assistant's process notes",
		"the blanket assistant-content exclusion must be gone")
	assert.Contains(t, p, "fact|preference|decision|experience|failure|observation|lesson",
		"schema enum must include lesson")
	// User-fact lane preserved.
	assert.True(t, strings.Contains(p, "USER FACTS"))
	// Verification gate: unverified beliefs must never become lessons (a
	// ladder pass measured misdiagnoses outnumbering correct lessons 7:1
	// without it).
	assert.Contains(t, p, "VERIFICATION GATE")
	assert.Contains(t, p, "NOT a lesson")
}

// The lesson type must survive ingestion instead of coercing to fact.
func TestLessonTypeIsValid(t *testing.T) {
	assert.True(t, isValidMemoryType("lesson"))
	assert.False(t, isValidMemoryType("hunch"))
}
