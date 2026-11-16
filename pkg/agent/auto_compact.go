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

// AutoCompactLedger once recorded, per session, the turn of the last
// end-of-turn auto-compaction attempt. It now records nothing. The type, its
// constructor and Forget are kept so that an embedder that built one and
// passed it to WithAutoCompactLedger still compiles.
//
// Deprecated: end-of-turn auto-compaction (behavior.auto_compact_percent) is
// retired. The in-loop fold and the step check (fitStepToWindow) keep every
// send inside the provider's window. A config file that still sets the key
// loads with a warning.
type AutoCompactLedger struct{}

// NewAutoCompactLedger returns a ledger that records nothing.
//
// Deprecated: see AutoCompactLedger.
func NewAutoCompactLedger() *AutoCompactLedger {
	return &AutoCompactLedger{}
}

// Forget does nothing: the ledger holds no per-session state. It is safe on a
// nil ledger.
//
// Deprecated: see AutoCompactLedger.
func (l *AutoCompactLedger) Forget(sessionID string) {}
