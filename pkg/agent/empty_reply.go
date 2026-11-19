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
	"fmt"
	"strings"
)

// stopReasonRefusal is the stop reason the Anthropic Messages API (and the
// Vercel AI Gateway on that surface) sets when the model declines a request.
const stopReasonRefusal = "refusal"

// isRefusal reports whether the provider stopped the reply as a refusal.
func isRefusal(stopReason string) bool {
	return stopReason == stopReasonRefusal
}

// emptyReplyText is the reply loom returns when the model produced no text
// after the one-shot retry. It says what happened and never presents an
// empty reply as a completed task, which is what the earlier
// "Completed N tool executions" placeholder did.
//
// A refusal gets its own sentence, because it is a deliberate answer from
// the model rather than a failure to produce one.
func emptyReplyText(stopReason string, toolExecutions, turns int) string {
	if isRefusal(stopReason) {
		return "The model declined to answer this request. No text was returned (stop reason: refusal)."
	}
	reason := stopReason
	if strings.TrimSpace(reason) == "" {
		reason = "unknown"
	}
	return fmt.Sprintf("The model returned no text (stop reason: %s) after %d tool executions across %d turns.",
		reason, toolExecutions, turns)
}
