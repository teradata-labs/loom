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
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Every LLMCost the server builds from an agent response must carry the
// estimate flag, or clients lose the asterisk on an estimated cost. The cost
// messages are built inline in the Weave/StreamWeave handlers, so this is a
// source check: each file's LLMCost literals and CostEstimated mappings must
// pair one for one.
func TestLLMCost_CarriesCostEstimated(t *testing.T) {
	for _, f := range []string{"server.go", "multi_agent.go"} {
		src, err := os.ReadFile(f)
		require.NoError(t, err)
		text := string(src)
		costs := strings.Count(text, "LlmCost: &loomv1.LLMCost{")
		require.Positive(t, costs, f)
		require.Equal(t, costs, len(regexp.MustCompile(`CostEstimated:\s+resp\.Usage\.CostEstimated,`).FindAllString(text, -1)),
			"%s: every LLMCost built from a response must set CostEstimated", f)
	}
}
