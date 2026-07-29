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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/teradata-labs/loom/pkg/session"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"go.uber.org/zap"
)

// Context dumping captures the exact (messages, tools) about to be dispatched
// to the LLM provider, one record per provider call, for offline inspection.
// It is a debug-only diagnostic: the record is deliberately un-redacted, so the
// only safeguards are that it is off by default and that it is written solely
// to a local per-run file — never to zap/Hawk or any shared log.

// contextDumpRecord is the on-disk shape of a single provider-call dump:
// the session id, the per-session turn number, and the compiled (messages,
// tools) captured verbatim before dispatch.
type contextDumpRecord struct {
	SessionID string            `json:"session_id"`
	Turn      int               `json:"turn"`
	Messages  []Message         `json:"messages"`
	Tools     []contextDumpTool `json:"tools"`
}

// contextDumpTool is the serializable projection of a shuttle.Tool. Tools reach
// chatWithRetry as interface values whose concrete fields carry no LLM-visible
// meaning. The provider payload carries only name, description, and input
// schema; backend is captured alongside them as routing context, not as
// something dispatched to the LLM.
type contextDumpTool struct {
	Name        string              `json:"name"`
	Description string              `json:"description"`
	InputSchema *shuttle.JSONSchema `json:"input_schema,omitempty"`
	Backend     string              `json:"backend,omitempty"`
}

// contextDumper owns the local per-run sink file and the per-session turn
// counters. One instance backs an agent for the life of the process.
type contextDumper struct {
	path string

	mu    sync.Mutex
	file  *os.File
	turns map[string]int // sessionID -> last emitted turn number
}

// envContextDumpVar is the environment switch that enables context dumping
// independently of Config.Debug.ContextDump.
const envContextDumpVar = "LOOM_DEBUG_CONTEXT_DUMP"

// envDebugDirVar overrides the parent directory for the per-run sink.
const envDebugDirVar = "LOOM_DEBUG_DIR"

var (
	envDumpOnce    sync.Once
	envDumpEnabled bool
)

// contextDumpEnvEnabled reports whether the environment switch requests
// dumping. The variable is read exactly once per process.
func contextDumpEnvEnabled() bool {
	envDumpOnce.Do(func() {
		v := strings.TrimSpace(os.Getenv(envContextDumpVar))
		envDumpEnabled = v == "1" || strings.EqualFold(v, "true")
	})
	return envDumpEnabled
}

// dumpContext is the tap at the entry of chatWithRetry. When dumping is off it
// early-returns before any serialization, so the production path pays only a
// bool check. When on, it captures the compiled (messages, tools) verbatim,
// tags them with the session id and the next per-session turn number, and
// writes one record to the local sink. A sink failure is logged locally and
// never propagates — capturing a dump must not disturb the provider call.
func (a *Agent) dumpContext(ctx Context, messages []Message, tools []shuttle.Tool) {
	if a.config == nil || (!a.config.Debug.ContextDump && !contextDumpEnvEnabled()) {
		return
	}

	d := a.contextDumperInstance()
	if d == nil {
		return
	}

	sessionID := session.SessionIDFromContext(ctx)
	rec := contextDumpRecord{
		SessionID: sessionID,
		Turn:      d.nextTurn(sessionID),
		Messages:  messages,
		Tools:     dumpTools(tools),
	}

	if err := d.write(rec); err != nil {
		// Log only the failure, never the record — the record is un-redacted
		// and must stay out of zap/Hawk.
		zap.L().Warn("context dump write failed", zap.Error(err))
	}
}

// contextDumperInstance lazily constructs the agent's sink on first use. A
// construction failure is logged once and leaves the agent with a nil dumper,
// so dumping is silently disabled for the run rather than crashing it.
func (a *Agent) contextDumperInstance() *contextDumper {
	a.contextDumpOnce.Do(func() {
		d, err := newContextDumper(a.id)
		if err != nil {
			zap.L().Warn("context dump sink init failed; dumps disabled for this run", zap.Error(err))
			return
		}
		a.contextDump = d
	})
	return a.contextDump
}

// newContextDumper opens the run-scoped sink file, creating the debug directory
// as needed. The path is keyed by process id and agent id so concurrent runs do
// not share a file. The file is mode 0600 because dumps may carry user data.
func newContextDumper(agentID string) (*contextDumper, error) {
	dir := contextDumpDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create context dump dir %q: %w", dir, err)
	}

	name := fmt.Sprintf("context-dump-%d-%s.jsonl", os.Getpid(), sanitizeForFilename(agentID))
	path := filepath.Join(dir, name)

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open context dump sink %q: %w", path, err)
	}

	return &contextDumper{
		path:  path,
		file:  f,
		turns: make(map[string]int),
	}, nil
}

// contextDumpDir resolves the parent directory for per-run sinks. It honors
// LOOM_DEBUG_DIR when set and otherwise falls back to a subdirectory of the OS
// temp dir.
func contextDumpDir() string {
	if dir := strings.TrimSpace(os.Getenv(envDebugDirVar)); dir != "" {
		return filepath.Join(dir, "context-dumps")
	}
	return filepath.Join(os.TempDir(), "loom-debug", "context-dumps")
}

// nextTurn returns the next monotonic turn number for a session, starting at 1
// on the session's first provider call.
func (d *contextDumper) nextTurn(sessionID string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.turns[sessionID]++
	return d.turns[sessionID]
}

// currentTurn returns the turn number last assigned to a session without
// advancing it — the in-flight turn during a session's tool execution — or 0
// before the session's first provider call.
func (d *contextDumper) currentTurn(sessionID string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.turns[sessionID]
}

// write serializes one record as a JSON line and appends it to the sink.
func (d *contextDumper) write(rec contextDumpRecord) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("marshal context dump record: %w", err)
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	if _, err := d.file.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("write context dump record: %w", err)
	}
	return nil
}

// dumpTools projects tools into their serializable form, skipping nil entries.
func dumpTools(tools []shuttle.Tool) []contextDumpTool {
	out := make([]contextDumpTool, 0, len(tools))
	for _, t := range tools {
		if t == nil {
			continue
		}
		out = append(out, contextDumpTool{
			Name:        t.Name(),
			Description: t.Description(),
			InputSchema: t.InputSchema(),
			Backend:     t.Backend(),
		})
	}
	return out
}

// sanitizeForFilename maps a value to a filesystem-safe token, collapsing any
// character outside [A-Za-z0-9_-] to an underscore.
func sanitizeForFilename(s string) string {
	if s == "" {
		return "agent"
	}
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}, s)
}
