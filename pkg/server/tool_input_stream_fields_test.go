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
package server

import (
	"testing"

	"github.com/stretchr/testify/assert"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
)

func TestApplyToolInputStreamFields_CarriesCallIdentityAndSize(t *testing.T) {
	progress := &loomv1.WeaveProgress{ToolName: "files"}

	applyToolInputStreamFields(progress, agent.ProgressEvent{
		IsToolInputStream: true,
		ToolName:          "files",
		ToolCallID:        "call_1",
		ToolInputBytes:    2048,
	})

	assert.True(t, progress.IsToolInputStream)
	assert.Equal(t, "call_1", progress.ToolCallId)
	assert.Equal(t, int64(2048), progress.ToolInputBytes)
	assert.Equal(t, "files", progress.ToolName)
}

func TestApplyToolInputStreamFields_IgnoresOtherEvents(t *testing.T) {
	progress := &loomv1.WeaveProgress{}

	applyToolInputStreamFields(progress, agent.ProgressEvent{
		IsTokenStream: true,
		ToolCallID:    "call_1",
	})

	assert.False(t, progress.IsToolInputStream)
	assert.Empty(t, progress.ToolCallId)
	assert.Zero(t, progress.ToolInputBytes)
}
