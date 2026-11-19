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

package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

func TestFormatCompactResponse(t *testing.T) {
	assert.Equal(t, "Nothing to compact: session s1 has only its newest 2 turns in context.\n",
		formatCompactResponse("s1", &loomv1.CompactSessionResponse{TurnsKept: 2}))

	got := formatCompactResponse("s1", &loomv1.CompactSessionResponse{
		Compacted: true, TurnsFolded: true, TurnsKept: 2, TokensBefore: 90000, TokensAfter: 20000,
	})
	assert.Contains(t, got, "older turns folded into the summary; newest 2 turns kept")
	assert.Contains(t, got, "about 90000 tokens before, 20000 after")
}

// The help text describes a compaction that folds only: it never says that a
// tool result becomes a stub.
func TestSessionsCompactHelpSaysItFoldsOnly(t *testing.T) {
	assert.NotContains(t, strings.ToLower(sessionsCompactCmd.Long), "stub", "the help text says that a compaction stubs tool results")
	assert.NotContains(t, strings.ToLower(sessionsCompactCmd.Short), "stub", "the short help says that a compaction stubs tool results")
	assert.Contains(t, sessionsCompactCmd.Long, "folded into the session's running summary", "the help text does not say what a compaction does")
}
