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

// Package hygiene enforces end-of-turn task-board hygiene for skill-emitted
// tasks. When a skill run leaves the board in an incoherent state — orphan
// IN_PROGRESS tasks, BLOCKED tasks with no HITL surfaced, or OPEN tasks
// that were never started — the auditor catches it before control returns
// to the user.
//
// Three violation kinds are detected:
//
//   - IN_PROGRESS_ORPHAN: agent claimed the task but never moved it to
//     DONE or BLOCKED before the turn ended. Misleading: the board says
//     work is in flight but no agent is on it.
//   - BLOCKED_NO_HITL: agent transitioned the task to BLOCKED but did not
//     surface a question to the user. The user only learns by reading the
//     board, which is the bug we're fixing.
//   - OPEN_UNSTARTED: a task was created but never claimed. The agent
//     either silently abandoned it or chose not to work it. Either way the
//     user is owed an explanation or a Deferred transition.
//
// Three policies govern enforcement (see loomv1.HygienePolicy):
//
//   - REQUIRE_FIX (default): inject a synthetic user message describing the
//     violations and re-run the LLM turn so the agent resolves them itself.
//     Capped by max_retries; on exhaustion the auditor falls through to
//     AUTO_FIX so the loop terminates.
//   - AUTO_FIX: machine-transition tasks with reason notes and spawn HITL
//     for BLOCKED. Cheaper but masks agent bugs.
//   - WARN_ONLY: emit observability events and a summary; do not change
//     task state and do not retry.
//
// Scope: only tasks emitted by currently-active skills (matched via
// SkillIdempotencyKey prefix "skill:<name>|sess:<sessionID>|") are
// audited. Tasks the agent created directly via TaskBoardTool with no
// idempotency key are out of scope — that is general agent hygiene, a
// different concern.
package hygiene
