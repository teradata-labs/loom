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

package failurerecovery

import "sync"

// failureRecord is an abstracted, PII-free failure held in RAM only. It is never
// persisted directly — it becomes a durable lesson only if the same tool later
// succeeds within the turn (write-once on recovery, spec §7).
//
// sig carries the DETERMINISTIC call fingerprint (canonical tool+args and the
// offending tokens parsed from the error). It is PII-bearing and is persisted on
// the lesson only when the feature is user-scoped (buildLesson enforces this),
// so a planned call can be matched against it deterministically at dispatch time
// without any LLM. The problem/solution text remains the abstract, shareable
// part of the lesson.
type failureRecord struct {
	class    string
	tool     string
	problem  string
	solution string
	sig      callSignature
}

// tracker holds the most recent unresolved failure per tool, for one session.
// It is the RAM-only staging area: a failure is recorded here and only written
// to the store when a subsequent success of the same tool confirms a recovery.
//
// Keyed on tool name (not tool+params): a fix typically changes the arguments,
// so requiring a param match would miss almost every real recovery.
type tracker struct {
	mu          sync.Mutex
	lastFailure map[string]failureRecord // tool -> most recent failure
	advised     map[string]bool          // canonical planned call -> already advised
}

func newTracker() *tracker {
	return &tracker{
		lastFailure: make(map[string]failureRecord),
		advised:     make(map[string]bool),
	}
}

// recordFailure stages an abstracted failure for a tool, replacing any prior
// unresolved failure for the same tool.
func (t *tracker) recordFailure(rec failureRecord) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lastFailure[rec.tool] = rec
}

// recordSuccess reports whether the given tool had a staged failure (a recovery)
// and, if so, returns it and clears the staging slot so the lesson is learned at
// most once per failure episode.
func (t *tracker) recordSuccess(tool string) (failureRecord, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	rec, ok := t.lastFailure[tool]
	if ok {
		delete(t.lastFailure, tool)
	}
	return rec, ok
}

// peekFailure returns the most recent staged failure for a tool WITHOUT clearing
// it. Used by reactive recall so its query text comes from the same abstractor
// that produced the durable lesson, keeping query and stored text aligned.
func (t *tracker) peekFailure(tool string) (failureRecord, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	rec, ok := t.lastFailure[tool]
	return rec, ok
}

// markAdvisedIfNew records that a semantic pre-exec advisory has been injected
// for a given canonical planned call, returning true only the FIRST time. This
// bounds the advisory to once per distinct planned call per session: the model
// is warned once and then left to decide, so a semantic (non-certain) match can
// never trap the loop by re-blocking a call the model deliberately re-issues.
func (t *tracker) markAdvisedIfNew(canonical string) bool {
	if canonical == "" {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.advised[canonical] {
		return false
	}
	t.advised[canonical] = true
	return true
}
