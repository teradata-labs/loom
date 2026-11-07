// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package shuttle

import "context"

// ParkableQuestion is a tool whose result IS a human's answer. With HITL park
// enabled the agent never executes such a call: the pre-scan parks the turn on
// it as a "question" item (exactly like contact_human), and on resume the
// agent builds the call's result from the human's answer via ParkAnswerResult.
// Execute still runs when the call is not parked: park disabled, ParkQuestion
// reported ok=false, the call ID is empty or duplicated within the batch (the
// card cannot bind such a call), or an in-turn resolver is used instead of
// park. Execute should then explain why the tool needs a human.
//
// Today each method is called at most once per call: ParkQuestion when the
// turn parks, ParkAnswerResult when it resumes. Implementations must still be
// pure with respect to the input and tolerate repeated calls, because
// ParkAnswerResult can run again when a resume is retried after it ran but
// before its result row was saved. The agent decides whether a call parks
// when the turn parks and records that decision on the park row; on resume it
// trusts the recorded decision and does not call ParkQuestion again. After a
// restart the input has been through JSON, so numbers arrive as float64.
//
// A policy Ask on a ParkableQuestion tool does not produce a separate
// approval: the question card is the only human step, as for contact_human.
// A policy Deny is never lifted.
type ParkableQuestion interface {
	Tool
	// ParkQuestion reports whether this call should park and the human-facing
	// question text. ok=false leaves the call to normal dispatch (Execute),
	// e.g. so invalid input reaches the model as a repairable tool error.
	// ctx carries the active trace span so tool-author code is traced.
	ParkQuestion(ctx context.Context, input map[string]interface{}) (question string, ok bool)
	// ParkAnswerResult builds the tool result from the human's non-empty
	// answer. It must not return nil; a nil result becomes an internal_error.
	ParkAnswerResult(ctx context.Context, input map[string]interface{}, answer string) *Result
}
