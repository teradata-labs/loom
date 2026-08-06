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

// Per-mutation debug logging traces every context-mutation point — skill load,
// compaction, large-result offload, per-session tool assembly, and restore
// re-fire — to zap at Debug level, tagged with the session id and the in-flight
// turn. It shares the context-dump switch: the same config flag or environment
// switch gates both, so with the switch off no mutation-debug log is emitted.

// contextDebug carries the mutation-debug switch and the per-session turn source
// into the components that host mutation points but do not hold an *Agent
// (SegmentedMemory, Memory, ManageSkillsTool). The switch is read on every check
// so it reflects the agent's final configuration regardless of construction
// order. A nil *contextDebug is a no-op, so a component may hold one
// unconditionally and the off path costs only a nil check.
type contextDebug struct {
	enabledFn func() bool
	turnOf    func(sessionID string) int
}

// on reports whether mutation-debug logging is active.
func (c *contextDebug) on() bool {
	return c != nil && c.enabledFn != nil && c.enabledFn()
}

// turn returns the in-flight turn number for a session, or 0 when unavailable.
func (c *contextDebug) turn(sessionID string) int {
	if c == nil || c.turnOf == nil {
		return 0
	}
	return c.turnOf(sessionID)
}

// contextDebugEnabled reports whether per-mutation debug logging is on. It reads
// the context-dump switch: the config flag or the environment switch.
func (a *Agent) contextDebugEnabled() bool {
	return (a.config != nil && a.config.Debug.ContextDump) || contextDumpEnvEnabled()
}

// contextDebugTurn returns the in-flight turn number for a session, read from
// the per-session counter the context dumper advances on each provider call.
func (a *Agent) contextDebugTurn(sessionID string) int {
	d := a.contextDumperInstance()
	if d == nil {
		return 0
	}
	return d.currentTurn(sessionID)
}

// newContextDebug builds the mutation-debug carrier injected into the memory and
// skill components. Its closures read this agent's switch and turn source at log
// time, so the carrier can be wired before configuration is finalized.
func (a *Agent) newContextDebug() *contextDebug {
	return &contextDebug{
		enabledFn: a.contextDebugEnabled,
		turnOf:    a.contextDebugTurn,
	}
}
