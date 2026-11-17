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
	"testing"

	"github.com/stretchr/testify/assert"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

func TestFormatCompactResponse(t *testing.T) {
	assert.Equal(t, "Nothing to compact: session s1 has only its newest 2 turns in context.\n",
		formatCompactResponse("s1", &loomv1.CompactSessionResponse{TurnsKept: 2}))

	got := formatCompactResponse("s1", &loomv1.CompactSessionResponse{
		Compacted: true, ToolResultsStubbed: true, TurnsFolded: true, TurnsKept: 2, TokensBefore: 90000, TokensAfter: 20000,
	})
	assert.Contains(t, got, "older tool results stubbed and older turns folded into the summary; newest 2 turns kept")
	assert.Contains(t, got, "about 90000 tokens before, 20000 after")

	assert.Contains(t, formatCompactResponse("s1", &loomv1.CompactSessionResponse{Compacted: true, ToolResultsStubbed: true}),
		": older tool results stubbed;")
}
