// Copyright 2026 Teradata
//
// harness — the small helpers the routes share: payload sizing, a scripted
// compressor (so a fold's summary is deterministic with no network), and the
// session/store readers that let a route inspect the compiled context and the
// durable rows directly.

package contextoptimiser

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/types"
)

// heavyConversation is ~1.2 KB of user text — conversation that fold must
// summarize (it is never a tool result, so eviction can never touch it).
func heavyConversation(i int) string {
	// VARIED, non-repetitive text: a real tokenizer (cl100k) tokenizes a single
	// repeated sentence far too cheaply (~8 bytes/token), so repetitive filler
	// never accumulates enough tokens to force a fold under the tiktoken estimate.
	// Varied words + numbers land near ~4 bytes/token, like real prose. Seeded by
	// i via a deterministic LCG (no rand — reproducible).
	words := strings.Fields("consumer group spool ceiling ctas deferred chargeback tier failure " +
		"rate department statement nightly batch window boundary lock io gigabytes climbed cpu " +
		"seconds inversion anomaly database owning user decision recorded escalate dba threshold " +
		"surcharge blended exempt registered shift log deferral saturation headroom estate ceiling")
	var b strings.Builder
	fmt.Fprintf(&b, "turn %d: ", i)
	n := int64(i*131 + 7)
	for b.Len() < 3600 {
		n = (n*1103515245 + 12345) & 0x7fffffff
		fmt.Fprintf(&b, "%s-%d ", words[int(n)%len(words)], n%9973)
	}
	return b.String()
}

// countingCompressor is the scripted summarizer: it returns a fixed summary so a
// fold's output is deterministic, and counts how often it was called.
type countingCompressor struct {
	mu      sync.Mutex
	calls   int
	summary string
}

func (c *countingCompressor) CompressMessages(_ context.Context, msgs []types.Message) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.summary != "" {
		return c.summary, nil
	}
	return fmt.Sprintf("[compressed %d messages]", len(msgs)), nil
}
func (c *countingCompressor) IsEnabled() bool { return true }
func (c *countingCompressor) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// setCompressor installs a compressor on the live agent's memory.
func (r *rig) setCompressor(c agent.MemoryCompressor) { r.mem.SetCompressor(c) }

// liveSession returns the session the route has been driving.
func (r *rig) liveSession(t *testing.T, sessionID string) *types.Session {
	t.Helper()
	s, ok := r.agent.GetSession(sessionID)
	if !ok {
		t.Fatalf("live session %s not found", sessionID)
	}
	return s
}

// restoredSession builds a twin over the same durable store and replays the
// session — the reload-is-a-read check (HLD §8).
func (r *rig) restoredSession(t *testing.T, sessionID string, maxContext, reservedOutput, offloadThreshold int) *types.Session {
	t.Helper()
	_, _, mem := buildAgentWithMemory(t, &scriptedLLM{}, r.store, r.skillsDir, r.patternsDir,
		maxContext, reservedOutput, offloadThreshold, false)
	s := mem.GetOrCreateSession(context.Background(), sessionID)
	if s == nil {
		t.Fatalf("restored session %s is nil", sessionID)
	}
	return s
}

// compiledContext returns the context a session would dispatch — read straight
// from its memory, no provider call.
func compiledContext(t *testing.T, s *types.Session) []types.Message {
	t.Helper()
	if s == nil || s.SegmentedMem == nil {
		return nil
	}
	sm, ok := s.SegmentedMem.(*agent.SegmentedMemory)
	if !ok {
		t.Fatalf("session %s carries no *agent.SegmentedMemory", s.ID)
	}
	return sm.GetMessagesForLLM()
}

// durableMessages reads the persisted rows from the store.
func durableMessages(t *testing.T, r *rig, sessionID string) []types.Message {
	t.Helper()
	msgs, err := r.store.LoadMessages(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("load messages: %v", err)
	}
	return msgs
}

// trim shortens content for readable failure messages.
func trim(s string) string {
	s = strings.ReplaceAll(s, "\n", "\\n")
	if len(s) > 160 {
		return s[:160] + fmt.Sprintf(" … [%d chars]", len(s))
	}
	return s
}
