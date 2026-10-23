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
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNoteToolCall_HintsOnlyAtThresholds(t *testing.T) {
	r := newRecoveryOrchestrator(nil, nil)
	for i := 1; i <= 9; i++ {
		count, hint := r.noteToolCall("k")
		assert.Equal(t, i, count)
		assert.Equal(t, i == 3 || i == 5 || i == 8, hint, "call %d", i)
	}
}

func TestNoteToolCall_ResetsOnDifferentCall(t *testing.T) {
	r := newRecoveryOrchestrator(nil, nil)
	for _, k := range []string{"a", "b", "a", "b", "a", "b"} {
		count, hint := r.noteToolCall(k)
		assert.Equal(t, 1, count)
		assert.False(t, hint)
	}
	r.noteToolCall("a")
	r.noteToolCall("a")
	count, hint := r.noteToolCall("a")
	assert.Equal(t, 3, count)
	assert.True(t, hint)
}

func TestNoteToolCall_NilReceiver(t *testing.T) {
	count, hint := (*recoveryOrchestrator)(nil).noteToolCall("k")
	assert.Equal(t, 0, count)
	assert.False(t, hint)
}

func TestNoteToolCall_EmptyThresholdsDisable(t *testing.T) {
	r := newRecoveryOrchestrator(&RecoveryConfig{}, nil)
	for i := 1; i <= 10; i++ {
		_, hint := r.noteToolCall("k")
		assert.False(t, hint, "call %d", i)
	}
}

func TestDefaultRecoveryConfig_RepeatHintThresholds(t *testing.T) {
	assert.Equal(t, []int{3, 5, 8}, DefaultRecoveryConfig().RepeatHintThresholds)
}
