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
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

func TestBuildShadowRecordsCarriesDeciderError(t *testing.T) {
	t.Parallel()
	req, err := NewRequest("s", map[string]any{"x": 1}, map[string]*loomv1.DecisionQuestion{"q": Noul("Is it so?")})
	require.NoError(t, err)

	long := errors.New("jev: HTTP 503: " + strings.Repeat("x", 2*maxErrorTextRunes))
	rows := BuildShadowRecords(req, Outcome{Path: loomv1.DecisionPath_DECISION_PATH_ERROR, Err: long}, "jev", "sess", nil)
	require.Len(t, rows, 1)
	assert.Equal(t, loomv1.DecisionPath_DECISION_PATH_ERROR, rows[0].Path)
	assert.True(t, strings.HasPrefix(rows[0].Error, "jev: HTTP 503: "))
	assert.Equal(t, maxErrorTextRunes+1, len([]rune(rows[0].Error)), "bounded plus the ellipsis")
	assert.Equal(t, "", rows[0].CandidateAnswer)

	ok := BuildShadowRecords(req, Outcome{Path: loomv1.DecisionPath_DECISION_PATH_FALLBACK}, "jev", "sess", nil)
	require.Len(t, ok, 1)
	assert.Equal(t, "", ok[0].Error, "no error, empty field")
}

func TestBuildShadowRecordsCarriesSubject(t *testing.T) {
	t.Parallel()
	req, err := NewRequest("s", map[string]any{"x": 1}, map[string]*loomv1.DecisionQuestion{"q": Noul("Is it so?")})
	require.NoError(t, err)
	rows := BuildShadowRecords(req, Outcome{Path: loomv1.DecisionPath_DECISION_PATH_DECIDER}, "jev", "sess",
		map[string]Reference{"q": {Subject: "session:abc"}})
	require.Len(t, rows, 1)
	assert.Equal(t, "session:abc", rows[0].Subject)
	assert.Equal(t, "", rows[0].ReferenceAnswer, "a subject-only reference is not a reference answer")
}
