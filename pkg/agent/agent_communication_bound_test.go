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
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBoundedPayloadValue_UnderBoundRidesExactly proves a payload within the
// bound is the marshaled bytes untouched.
func TestBoundedPayloadValue_UnderBoundRidesExactly(t *testing.T) {
	data := map[string]interface{}{"question": "which database?", "rows": []interface{}{1.0, 2.0, 3.0}}
	got, trueSize, err := boundedPayloadValue(data, 16384)
	require.NoError(t, err)

	want, err := json.Marshal(data)
	require.NoError(t, err)
	assert.Equal(t, want, got, "within the bound the payload is byte-identical")
	assert.Equal(t, len(want), trueSize, "the reported size is the marshaled size")

	var round map[string]interface{}
	require.NoError(t, json.Unmarshal(got, &round), "receiver unmarshals it")
	assert.Equal(t, data["question"], round["question"])
}

// TestBoundedPayloadValue_OverBoundStaysValidJSON is the regression this fix
// exists for: the old path truncated the MARSHALED JSON and appended prose, so
// the receiver's json.Unmarshal failed on every over-bound message. The
// replacement must always be a well-formed document that names what was elided.
func TestBoundedPayloadValue_OverBoundStaysValidJSON(t *testing.T) {
	const threshold = 1024
	data := map[string]interface{}{"rows": strings.Repeat("payload-", 5000)}

	got, trueSize, err := boundedPayloadValue(data, threshold)
	require.NoError(t, err)
	require.LessOrEqual(t, len(got), threshold, "the bound holds")

	// The decisive assertion: the receiver can still read it.
	var round map[string]interface{}
	require.NoError(t, json.Unmarshal(got, &round),
		"an over-bound payload must remain valid JSON — the receiver unmarshals it")

	assert.Equal(t, true, round["truncated"], "the payload says it was bounded")
	original, _ := json.Marshal(data)
	assert.Equal(t, float64(len(original)), round["original_bytes"], "and names the true size")
	assert.Equal(t, len(original), trueSize, "metadata records the true size, not the notice's")
	assert.NotEmpty(t, round["preview"], "a preview of the elided content rides along")
	assert.Contains(t, round["note"], "narrower slice", "the note names the door")
}

// TestBoundedPayloadValue_StringPayload covers the common shape: a plain string
// over the bound. It must not come back as cut-off JSON either.
func TestBoundedPayloadValue_StringPayload(t *testing.T) {
	got, trueSize, err := boundedPayloadValue(strings.Repeat("x", 40000), 16384)
	require.NoError(t, err)

	var round map[string]interface{}
	require.NoError(t, json.Unmarshal(got, &round), "still valid JSON")
	assert.Equal(t, true, round["truncated"])
	assert.Greater(t, trueSize, 40000, "the true size is the payload's, not the notice's")
}
