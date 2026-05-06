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

package index

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"sync"
	"time"

	"github.com/teradata-labs/loom/pkg/skills"
)

// CacheKey identifies a router decision. Keyed on session, message hash,
// and a hash of the eligible binding set. Different bindings on the same
// message must produce a different key so an agent reconfiguration
// invalidates cached decisions.
type CacheKey struct {
	SessionID    string
	MessageHash  string
	BindingsHash string
}

// CacheEntry is the cached router output for one (session, message,
// bindings) triple plus its expiry.
type CacheEntry struct {
	Candidates []*skills.Skill
	ExpiresAt  time.Time
}

// Cache is a per-router LRU keyed on CacheKey. Concurrent-safe; a single
// shared instance is fine across an agent's sessions because the key
// includes SessionID.
//
// Eviction policy: time-based expiry plus a soft size cap. When the cap
// is exceeded the oldest-by-insertion entry is dropped (FIFO rather than
// strict LRU; routing decisions are short-lived enough that LRU doesn't
// pay for itself).
type Cache struct {
	mu       sync.Mutex
	entries  map[CacheKey]*entryNode
	head     *entryNode
	tail     *entryNode
	maxSize  int
	defaultT time.Duration
	now      func() time.Time
}

type entryNode struct {
	key  CacheKey
	val  CacheEntry
	prev *entryNode
	next *entryNode
}

// CacheOption configures Cache during construction.
type CacheOption func(*Cache)

// WithMaxSize caps the cache at n entries. Default 256.
func WithMaxSize(n int) CacheOption {
	return func(c *Cache) {
		if n > 0 {
			c.maxSize = n
		}
	}
}

// WithDefaultTTL sets the default per-entry TTL when callers don't supply one.
func WithDefaultTTL(d time.Duration) CacheOption {
	return func(c *Cache) {
		if d > 0 {
			c.defaultT = d
		}
	}
}

// withNowFunc is test-only; the default uses time.Now.
func withNowFunc(now func() time.Time) CacheOption {
	return func(c *Cache) {
		if now != nil {
			c.now = now
		}
	}
}

// NewCache builds a router decision cache with the given options.
func NewCache(opts ...CacheOption) *Cache {
	c := &Cache{
		entries:  make(map[CacheKey]*entryNode),
		maxSize:  256,
		defaultT: 5 * time.Minute,
		now:      time.Now,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Get returns the cached candidates for a key, or nil when absent or expired.
// Expired entries are evicted on read.
func (c *Cache) Get(key CacheKey) []*skills.Skill {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	node, ok := c.entries[key]
	if !ok {
		return nil
	}
	if c.now().After(node.val.ExpiresAt) {
		c.removeLocked(node)
		return nil
	}
	return node.val.Candidates
}

// Put stores a routing decision. Pass ttl=0 to use the cache's default TTL.
// Re-puts replace the existing entry without altering insertion order.
func (c *Cache) Put(key CacheKey, candidates []*skills.Skill, ttl time.Duration) {
	if c == nil {
		return
	}
	if ttl <= 0 {
		ttl = c.defaultT
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if existing, ok := c.entries[key]; ok {
		existing.val = CacheEntry{
			Candidates: candidates,
			ExpiresAt:  c.now().Add(ttl),
		}
		return
	}

	node := &entryNode{
		key: key,
		val: CacheEntry{
			Candidates: candidates,
			ExpiresAt:  c.now().Add(ttl),
		},
	}
	c.entries[key] = node
	c.appendLocked(node)

	for len(c.entries) > c.maxSize && c.head != nil {
		c.removeLocked(c.head)
	}
}

// Invalidate removes a single key. Used when bindings change mid-session.
func (c *Cache) Invalidate(key CacheKey) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if node, ok := c.entries[key]; ok {
		c.removeLocked(node)
	}
}

// InvalidateSession drops every entry for the given session. Called when
// bindings change for an agent session.
func (c *Cache) InvalidateSession(sessionID string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// Walk the FIFO queue rather than the map so the linked list stays in
	// sync. The cache is small (max 256 entries), so a linear walk is cheap.
	cur := c.head
	for cur != nil {
		next := cur.next
		if cur.key.SessionID == sessionID {
			c.removeLocked(cur)
		}
		cur = next
	}
}

// Size returns the current number of cached entries (including any expired
// entries that have not yet been read past).
func (c *Cache) Size() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// HashMessage produces a stable, opaque key for a user message. The hash
// covers only the message text; the session id and bindings hash are
// supplied separately to CacheKey to keep keys orthogonal.
func HashMessage(msg string) string {
	h := sha256.Sum256([]byte(msg))
	return hex.EncodeToString(h[:16])
}

// HashBindings produces a stable hash of the resolved binding set used
// during a router walk. Order-independent on Name + Mode + Priority +
// MinVersion + LabelMatch keys/values.
func HashBindings(bindings []skills.SkillBinding) string {
	if len(bindings) == 0 {
		return ""
	}
	type rec struct {
		Name       string
		Mode       string
		Priority   int32
		MinVersion string
		LabelKeys  []string
		LabelVals  []string
	}
	records := make([]rec, 0, len(bindings))
	for _, b := range bindings {
		keys := make([]string, 0, len(b.LabelMatch))
		for k := range b.LabelMatch {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		vals := make([]string, 0, len(keys))
		for _, k := range keys {
			vals = append(vals, b.LabelMatch[k])
		}
		records = append(records, rec{
			Name:       b.Name,
			Mode:       string(b.Mode),
			Priority:   b.Priority,
			MinVersion: b.MinVersion,
			LabelKeys:  keys,
			LabelVals:  vals,
		})
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].Name != records[j].Name {
			return records[i].Name < records[j].Name
		}
		return records[i].Mode < records[j].Mode
	})
	h := sha256.New()
	for _, r := range records {
		h.Write([]byte(r.Name))
		h.Write([]byte{0})
		h.Write([]byte(r.Mode))
		h.Write([]byte{0})
		h.Write([]byte{byte(r.Priority), byte(r.Priority >> 8), byte(r.Priority >> 16), byte(r.Priority >> 24)})
		h.Write([]byte(r.MinVersion))
		for i, k := range r.LabelKeys {
			h.Write([]byte(k))
			h.Write([]byte{0})
			h.Write([]byte(r.LabelVals[i]))
			h.Write([]byte{0})
		}
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// ----------------------------------------------------------------------------
// Internal: doubly-linked list operations (FIFO eviction order).
// All callers must hold c.mu.
// ----------------------------------------------------------------------------

func (c *Cache) appendLocked(node *entryNode) {
	node.prev = c.tail
	node.next = nil
	if c.tail != nil {
		c.tail.next = node
	}
	c.tail = node
	if c.head == nil {
		c.head = node
	}
}

func (c *Cache) removeLocked(node *entryNode) {
	if node.prev != nil {
		node.prev.next = node.next
	} else {
		c.head = node.next
	}
	if node.next != nil {
		node.next.prev = node.prev
	} else {
		c.tail = node.prev
	}
	node.prev = nil
	node.next = nil
	delete(c.entries, node.key)
}
