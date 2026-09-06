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

	"github.com/teradata-labs/loom/pkg/shuttle"
)

// Session-handle auto-release (issue #345). A 3×64-agent live study showed
// that MCP session-scoped resources leak until server TTL when their release
// is left to agent discretion: across 192 agent runs, zero agents released a
// handle, budget slots never churned, and waiting mechanisms starved. The
// lifecycle therefore belongs to the runtime: the agent's conversation loop
// plants a collector in context, the adapter deposits any session_handle it
// sees in a successful tool result, and when the loop ends every collected
// handle is released best-effort by calling the tool that minted it with the
// release property that tool's own InputSchema declares.
//
// The convention is schema-gated on both ends: a handle is only tracked when
// the minting tool's InputSchema declares a release property ("releaseHandle"
// or "release_handle"), and a release is only attempted through a tool that
// declares one. A server that never declared the convention is never called
// back — an auto-release against a permissive server that treats unknown
// arguments as a fresh mint request would otherwise leak harder than doing
// nothing.

// handleCollectorKey is the context key for the per-conversation collector.
type handleCollectorKey struct{}

// mintedHandle is one tracked session handle and the adapter that minted it.
// The release call goes back through the same tool on the same server, with
// the property name and required-fields check derived from that adapter's
// own tool schema at release time.
type mintedHandle struct {
	adapter *MCPToolAdapter
	handle  string
}

// HandleCollector accumulates session handles minted during one agent
// conversation. Safe for concurrent use (parallel tool execution).
//
// Scope tradeoff: the collector is planted per chat() call, so handles live
// for exactly one agent message exchange. A handle the agent reuses from
// conversation history in a LATER message will already have been released at
// the end of the message that minted it; the follow-up call fails and the
// agent must mint a fresh handle. This is deliberate — releasing per chat is
// the only boundary the runtime currently observes. Callers wanting
// session-long handle lifetimes need a session-end hook, which does not
// exist yet.
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

// ContextWithHandleCollector installs an EXISTING collector on ctx, so a turn
// that spans more than one call — a HITL park and the resume that finishes it
// — keeps one collector for its whole life. Its handles then stay live across
// the gap and are released once, by whichever call actually ends the turn.
// The per-call scope in the type comment above is the default, not a limit:
// the owner is whoever planted the collector.
func ContextWithHandleCollector(ctx context.Context, c *HandleCollector) context.Context {
	if c == nil {
		return ctx
	}
	return context.WithValue(ctx, handleCollectorKey{}, c)
}

// collectorFrom returns the conversation's collector, or nil when the caller
// did not opt in (workflows, direct executor use).
func collectorFrom(ctx context.Context) *HandleCollector {
	c, _ := ctx.Value(handleCollectorKey{}).(*HandleCollector)
	return c
}

// sessionHandleLeaseKind is the Kind the adapter stamps on the lease events
// it emits for MCP session handles (pkg/shuttle/lease.go). Opaque to loom —
// the ledger and the LLM slot scheduler match it purely by identity.
const sessionHandleLeaseKind = "mcp-session-handle"

// collectorKey scopes dedup to the minting server: two servers can mint
// identical handle strings, and a release on one must never forget the
// other's handle. The same key doubles as the lease event ID, so a mint's
// LeaseAcquired, an explicit release's LeaseReleased, and ReleaseAll's
// end-of-conversation LeaseReleased all name the identical (Kind, ID) pair.
func collectorKey(a *MCPToolAdapter, handle string) string {
	return a.serverName + "\x00" + handle
}

// add records a minted handle once per server. Duplicates (the same handle
// re-reported by the same server) are ignored; releases are deduplicated at
// collection time.
func (c *HandleCollector) add(a *MCPToolAdapter, handle string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := collectorKey(a, handle)
	if c.present[key] {
		return
	}
	c.present[key] = true
	c.minted = append(c.minted, mintedHandle{adapter: a, handle: handle})
}

// forget drops a handle the agent released explicitly through the same
// server (seen as a release argument on a successful call), so ReleaseAll
// doesn't double-release it.
func (c *HandleCollector) forget(a *MCPToolAdapter, handle string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := collectorKey(a, handle)
	if !c.present[key] {
		return
	}
	delete(c.present, key)
	for i, m := range c.minted {
		if m.handle == handle && m.adapter.serverName == a.serverName {
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

// releaseAllTimeout bounds the WHOLE end-of-conversation release pass — it is
// a total budget shared by every release, not a per-handle allowance.
// ReleaseAll runs synchronously on the chat return path, so a hung server
// adds at most ~this much latency to the conversation's return.
const releaseAllTimeout = 3 * time.Second

// releaseAllConcurrency bounds how many releases run in flight at once.
// Releases run concurrently so one slow server cannot serialize the rest
// into the shared budget, but bounded so a conversation that minted many
// handles doesn't burst-open work against every server simultaneously.
const releaseAllConcurrency = 4

// ReleaseAll releases every tracked handle, best-effort: it runs on a fresh
// background context (the conversation's ctx is typically already cancelled),
// each failure or skip is logged, and the collector empties regardless.
//
// The pass is synchronous — the caller's defer must not race agent teardown —
// but internally concurrent (bounded by releaseAllConcurrency) under one
// shared releaseAllTimeout budget, so the worst case adds ~3s, not 3s per
// handle.
//
// A release is only attempted when the minting tool's own InputSchema
// declares a release property AND that property alone satisfies the schema's
// required fields; otherwise the handle is skipped with a warning and left
// to the server's TTL (the pre-#345 behavior), which beats a call the
// client-side schema validation is guaranteed to reject.
//
// The returned LeaseReleased events name every handle the collector stopped
// tracking, including ones whose wire release was skipped or failed: the
// conversation is over, so the runtime no longer holds them either way and
// the ledger must not keep seeding RESOURCE_HOLDER for a dead conversation.
// The caller applies them to the agent's per-session lease ledger.
func (c *HandleCollector) ReleaseAll(logger *zap.Logger) []shuttle.LeaseEvent {
	c.mu.Lock()
	minted := c.minted
	c.minted = nil
	c.present = map[string]bool{}
	c.mu.Unlock()
	if len(minted) == 0 {
		return nil
	}
	released := make([]shuttle.LeaseEvent, 0, len(minted))
	for _, m := range minted {
		released = append(released, shuttle.LeaseEvent{
			Action: shuttle.LeaseReleased,
			Kind:   sessionHandleLeaseKind,
			ID:     collectorKey(m.adapter, m.handle),
		})
	}
	if logger == nil {
		logger = zap.NewNop()
	}

	ctx, cancel := context.WithTimeout(context.Background(), releaseAllTimeout)
	defer cancel()

	sem := make(chan struct{}, releaseAllConcurrency)
	var wg sync.WaitGroup
	for _, m := range minted {
		wg.Add(1)
		sem <- struct{}{}
		go func(m mintedHandle) {
			defer wg.Done()
			defer func() { <-sem }()
			releaseOne(ctx, m, logger)
		}(m)
	}
	wg.Wait()
	return released
}

// releaseOne attempts a single best-effort release, deriving the wire
// spelling of the release property from the minting tool's own InputSchema.
func releaseOne(ctx context.Context, m mintedHandle, logger *zap.Logger) {
	fields := []zap.Field{
		zap.String("server", m.adapter.serverName),
		zap.String("tool", m.adapter.tool.Name),
		zap.String("handle", m.handle),
	}

	prop, ok := releaseHandleProperty(m.adapter.tool.InputSchema)
	if !ok {
		// Tracking is schema-gated, so this only happens when the tool
		// definition changed between mint and release. Leak to server TTL.
		logger.Warn("session-handle auto-release skipped: tool schema declares no release property", fields...)
		return
	}
	if !releaseSatisfiesRequired(m.adapter.tool.InputSchema, prop) {
		// The schema requires more than the release property; a one-argument
		// release call would fail client-side validation before reaching the
		// server. Leaking to server TTL beats a guaranteed-failing call.
		logger.Warn("session-handle auto-release skipped: schema requires fields beyond the release property",
			append(fields, zap.String("release_property", prop))...)
		return
	}

	_, err := m.adapter.client.CallTool(ctx, m.adapter.tool.Name, map[string]interface{}{
		prop: m.handle,
	})
	if err != nil {
		logger.Warn("session-handle auto-release failed (best-effort)",
			append(fields, zap.Error(err))...)
		return
	}
	logger.Info("session handle auto-released at conversation end", fields...)
}

// releaseHandleProperty resolves the release property name the tool's own
// InputSchema declares — the server's actual wire spelling. It is derived
// directly from the raw MCP schema (never from the adapter's LLM-facing
// case conversion), so the release call always matches what the server
// published regardless of how agent-issued parameters are normalized.
func releaseHandleProperty(schema map[string]interface{}) (string, bool) {
	props, _ := schema["properties"].(map[string]interface{})
	if props == nil {
		return "", false
	}
	for _, name := range []string{"releaseHandle", "release_handle"} {
		if _, declared := props[name]; declared {
			return name, true
		}
	}
	return "", false
}

// releaseSatisfiesRequired reports whether a call carrying only the release
// property can satisfy the schema's required fields: required ⊆ {prop}.
// The required list appears as []interface{} after a JSON round-trip and as
// []string when constructed in Go; both are handled.
func releaseSatisfiesRequired(schema map[string]interface{}, prop string) bool {
	switch req := schema["required"].(type) {
	case nil:
		return true
	case []string:
		for _, r := range req {
			if r != prop {
				return false
			}
		}
		return true
	case []interface{}:
		for _, r := range req {
			name, isString := r.(string)
			if !isString || name != prop {
				return false
			}
		}
		return true
	default:
		// Malformed required clause: don't guess — skip the release.
		return false
	}
}

// trackSessionHandles inspects a successful tool outcome. A top-level
// "session_handle" string in the result payload is collected for
// end-of-conversation release — but only when the tool's own InputSchema
// declares a release property, i.e. the server opted into the convention.
// A successful call that carried a release argument (either spelling)
// removes that handle from tracking (the agent cleaned up itself).
//
// The returned lease events mirror the collector mutations onto the generic
// shuttle contract (pkg/shuttle/lease.go): a mint yields LeaseAcquired, an
// explicit release yields LeaseReleased, in observation order. The caller
// (Execute) appends them to the outgoing shuttle.Result, so the agent's
// per-session ledger and the LLM slot scheduler track MCP session handles
// with zero MCP-specific code. Without a collector in ctx (workflow paths,
// direct executor use) nothing is tracked and no events are emitted —
// unchanged behavior.
func trackSessionHandles(ctx context.Context, a *MCPToolAdapter, params map[string]interface{}, data interface{}) []shuttle.LeaseEvent {
	c := collectorFrom(ctx)
	if c == nil {
		return nil
	}
	var events []shuttle.LeaseEvent
	for _, key := range []string{"release_handle", "releaseHandle"} {
		if released, ok := params[key].(string); ok && released != "" {
			c.forget(a, released)
			// The release event is emitted even for a handle the collector
			// never tracked: the server processed the release on a successful
			// call, and the backend's declaration is authoritative (the
			// ledger tolerates release-of-unknown as a no-op).
			events = append(events, shuttle.LeaseEvent{
				Action: shuttle.LeaseReleased,
				Kind:   sessionHandleLeaseKind,
				ID:     collectorKey(a, released),
			})
		}
	}
	if _, declared := releaseHandleProperty(a.tool.InputSchema); !declared {
		return events // server never declared the convention: do not track
	}
	if h := sessionHandleFromData(data); h != "" {
		c.add(a, h)
		events = append(events, shuttle.LeaseEvent{
			Action: shuttle.LeaseAcquired,
			Kind:   sessionHandleLeaseKind,
			ID:     collectorKey(a, h),
		})
	}
	return events
}

// sessionHandleFromData extracts a top-level session_handle string from a
// tool result payload in any of the shapes convertMCPContent produces: a
// string containing a JSON object (single text content), a JSON object
// already, or a multi-item content slice whose text items may carry the
// JSON payload. Anything else yields "".
func sessionHandleFromData(data interface{}) string {
	switch v := data.(type) {
	case string:
		return sessionHandleFromJSON(v)
	case map[string]interface{}:
		return sessionHandleFromMap(v)
	case []map[string]interface{}:
		for _, item := range v {
			if h := sessionHandleFromMap(item); h != "" {
				return h
			}
		}
	case []interface{}:
		// The same slice shape after a JSON round-trip (workflow persistence).
		for _, item := range v {
			if m, ok := item.(map[string]interface{}); ok {
				if h := sessionHandleFromMap(m); h != "" {
					return h
				}
			}
		}
	}
	return ""
}

// sessionHandleFromMap reads a session_handle from a result object: directly
// as a top-level field, or inside a content item's "text" payload (the shape
// convertMCPContent produces for multi-item results).
func sessionHandleFromMap(m map[string]interface{}) string {
	if h := stringField(m, "session_handle"); h != "" {
		return h
	}
	if text, ok := m["text"].(string); ok {
		return sessionHandleFromJSON(text)
	}
	return ""
}

// sessionHandleFromJSON parses a string as a JSON object and extracts its
// top-level session_handle, or "".
func sessionHandleFromJSON(s string) string {
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return ""
	}
	return stringField(m, "session_handle")
}

func stringField(m map[string]interface{}, key string) string {
	s, _ := m[key].(string)
	if len(s) == 0 || len(s) > 256 {
		return ""
	}
	return s
}
