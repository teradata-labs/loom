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
package adapter

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"go.uber.org/zap"
)

// Session-handle auto-release (issue #345). A 3×64-agent live study showed
// that MCP session-scoped resources leak until server TTL when their release
// is left to agent discretion: across 192 agent runs, zero agents released a
// handle, budget slots never churned, and waiting mechanisms starved. The
// lifecycle therefore belongs to the runtime: the agent's conversation loop
// plants a collector in context, the adapter deposits any session_handle it
// sees in a successful tool result (the convention: a top-level
// "session_handle" string field in the result payload), and when the loop
// ends every collected handle is released best-effort by calling the tool
// that minted it with {"release_handle": <handle>}.

// handleCollectorKey is the context key for the per-conversation collector.
type handleCollectorKey struct{}

// mintedHandle is one tracked session handle and the adapter that minted it
// (the release call goes back through the same tool on the same server).
type mintedHandle struct {
	adapter *MCPToolAdapter
	handle  string
}

// HandleCollector accumulates session handles minted during one agent
// conversation. Safe for concurrent use (parallel tool execution).
type HandleCollector struct {
	mu      sync.Mutex
	minted  []mintedHandle
	present map[string]bool
}

// WithHandleCollector returns a ctx carrying a fresh collector and the
// collector itself. The caller (the agent conversation loop) must invoke
// ReleaseAll when the conversation ends.
func WithHandleCollector(ctx context.Context) (context.Context, *HandleCollector) {
	c := &HandleCollector{present: map[string]bool{}}
	return context.WithValue(ctx, handleCollectorKey{}, c), c
}

// collectorFrom returns the conversation's collector, or nil when the caller
// did not opt in (workflows, direct executor use).
func collectorFrom(ctx context.Context) *HandleCollector {
	c, _ := ctx.Value(handleCollectorKey{}).(*HandleCollector)
	return c
}

// add records a minted handle once. Duplicates (the same handle re-reported)
// are ignored; releases are deduplicated at collection time.
func (c *HandleCollector) add(a *MCPToolAdapter, handle string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.present[handle] {
		return
	}
	c.present[handle] = true
	c.minted = append(c.minted, mintedHandle{adapter: a, handle: handle})
}

// forget drops a handle the agent released explicitly (seen as a
// release_handle argument on a successful call), so ReleaseAll doesn't
// double-release it.
func (c *HandleCollector) forget(handle string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.present[handle] {
		return
	}
	delete(c.present, handle)
	for i, m := range c.minted {
		if m.handle == handle {
			c.minted = append(c.minted[:i:i], c.minted[i+1:]...)
			break
		}
	}
}

// Count reports tracked handles (tests, diagnostics).
func (c *HandleCollector) Count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.minted)
}

// releaseAllTimeout bounds the whole end-of-conversation release pass; the
// conversation is already over, so this must never hold the caller long.
const releaseAllTimeout = 10 * time.Second

// ReleaseAll releases every tracked handle, best-effort: it runs on a fresh
// background context (the conversation's ctx is typically already cancelled),
// each failure is logged and skipped, and the collector empties regardless.
func (c *HandleCollector) ReleaseAll(logger *zap.Logger) {
	c.mu.Lock()
	minted := c.minted
	c.minted = nil
	c.present = map[string]bool{}
	c.mu.Unlock()
	if len(minted) == 0 {
		return
	}
	if logger == nil {
		logger = zap.NewNop()
	}

	ctx, cancel := context.WithTimeout(context.Background(), releaseAllTimeout)
	defer cancel()
	for _, m := range minted {
		_, err := m.adapter.client.CallTool(ctx, m.adapter.tool.Name, map[string]interface{}{
			"release_handle": m.handle,
		})
		if err != nil {
			logger.Debug("session-handle auto-release failed (best-effort)",
				zap.String("tool", m.adapter.tool.Name),
				zap.String("server", m.adapter.serverName),
				zap.Error(err))
			continue
		}
		logger.Info("session handle auto-released at conversation end",
			zap.String("server", m.adapter.serverName),
			zap.String("tool", m.adapter.tool.Name))
	}
}

// trackSessionHandles inspects a successful tool outcome: a top-level
// "session_handle" string in the result payload is collected for
// end-of-conversation release, and a successful call that carried a
// release_handle argument removes that handle from tracking (the agent
// cleaned up itself).
func trackSessionHandles(ctx context.Context, a *MCPToolAdapter, params map[string]interface{}, data interface{}) {
	c := collectorFrom(ctx)
	if c == nil {
		return
	}
	if released, ok := params["release_handle"].(string); ok && released != "" {
		c.forget(released)
	}
	if h := sessionHandleFromData(data); h != "" {
		c.add(a, h)
	}
}

// sessionHandleFromData extracts a top-level session_handle string from a
// tool result payload: either a JSON object already, or a string containing
// one. Anything else yields "".
func sessionHandleFromData(data interface{}) string {
	switch v := data.(type) {
	case string:
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(v), &m); err != nil {
			return ""
		}
		return stringField(m, "session_handle")
	case map[string]interface{}:
		return stringField(v, "session_handle")
	}
	return ""
}

func stringField(m map[string]interface{}, key string) string {
	s, _ := m[key].(string)
	if len(s) == 0 || len(s) > 256 {
		return ""
	}
	return s
}
