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
package bedrock

import (
	"encoding/json"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// toolUseInputToMap must extract the arguments a Converse toolUse block
// carries. The document's concrete types keep their value in an unexported
// field and do NOT implement json.Marshaler, so the pre-fix approach —
// json.Marshal(doc) — produced "{}" and silently dropped every argument.
// Live symptom: DeepSeek/Qwen tool calls arrived with empty input, each
// call failed required-parameter validation, and the model looped
// re-issuing it (TER-770).
func TestToolUseInputToMap_ExtractsArguments(t *testing.T) {
	doc := document.NewLazyDocument(map[string]interface{}{
		"action": "load",
		"name":   "get-started",
		"count":  float64(3),
		"nested": map[string]interface{}{"a": true},
	})

	got := toolUseInputToMap(doc)

	require.NotNil(t, got)
	assert.Equal(t, "load", got["action"])
	assert.Equal(t, "get-started", got["name"])
	assert.Equal(t, float64(3), got["count"])
	assert.Equal(t, map[string]interface{}{"a": true}, got["nested"])
}

// Pin the failure mode the fix removes: plain json.Marshal on a smithy
// document yields "{}" (unexported fields, no json.Marshaler). If a future
// SDK bump makes json.Marshal work, this test documents why the helper
// exists; if someone reverts the helper to json.Marshal, the test above
// fails.
func TestToolUseInputToMap_PlainJSONMarshalDropsArguments(t *testing.T) {
	doc := document.NewLazyDocument(map[string]interface{}{"action": "load"})

	raw, err := json.Marshal(doc)
	require.NoError(t, err)
	assert.JSONEq(t, "{}", string(raw),
		"json.Marshal on a smithy document no longer drops the value — revisit whether toolUseInputToMap is still needed")
}

func TestToolUseInputToMap_NilDocument(t *testing.T) {
	got := toolUseInputToMap(nil)
	require.NotNil(t, got)
	assert.Empty(t, got)
}

// A model may emit a non-object input (string, array). The helper degrades
// to an empty map and leaves reporting to the tool's own validation.
func TestToolUseInputToMap_NonObjectInputDegradesToEmpty(t *testing.T) {
	doc := document.NewLazyDocument("not-an-object")
	got := toolUseInputToMap(doc)
	require.NotNil(t, got)
	assert.Empty(t, got)
}
