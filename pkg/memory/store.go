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

package memory

import "context"

// TokenCounter counts tokens. Satisfied by *agent.TokenCounter.
type TokenCounter interface {
	CountTokens(text string) int
}

// GraphMemoryStore defines the storage interface for graph-backed episodic memory.
// Implementations exist for SQLite and PostgreSQL.
type GraphMemoryStore interface {
	// Entity CRUD (mutable)
	CreateEntity(ctx context.Context, entity *Entity) (*Entity, error)
	GetEntity(ctx context.Context, agentID, name string) (*Entity, error)
	UpdateEntity(ctx context.Context, entity *Entity) (*Entity, error)
	ListEntities(ctx context.Context, agentID, entityType string, limit, offset int) ([]*Entity, int, error)
	SearchEntities(ctx context.Context, agentID, query string, limit int) ([]*Entity, error)
	DeleteEntity(ctx context.Context, agentID, name string) error

	// Edge CRUD (mutable, UNIQUE on source+target+relation)
	Relate(ctx context.Context, edge *Edge) (*Edge, error) // upsert
	Unrelate(ctx context.Context, sourceID, targetID, relation string) error
	Neighbors(ctx context.Context, entityID string, relation string, direction string, depth int) ([]*Edge, error)
	ListEdgesFrom(ctx context.Context, entityID string) ([]*Edge, error)
	ListEdgesTo(ctx context.Context, entityID string) ([]*Edge, error)

	// Memory (append-only, immutable content)
	Remember(ctx context.Context, mem *Memory) (*Memory, error)
	GetMemory(ctx context.Context, agentID, memoryID string) (*Memory, error)
	Recall(ctx context.Context, opts RecallOpts) ([]*Memory, error)
	Forget(ctx context.Context, memoryID string) error                                  // soft-delete: set expires_at
	Supersede(ctx context.Context, oldMemoryID string, newMem *Memory) (*Memory, error) // creates SUPERSEDES lineage
	Consolidate(ctx context.Context, memoryIDs []string, consolidated *Memory) (*Memory, error)
	GetLineage(ctx context.Context, memoryID string) ([]*MemoryLineage, error)

	// Access tracking (the only allowed mutation on memories)
	TouchMemories(ctx context.Context, memoryIDs []string) error

	// Salience management
	DecayAll(ctx context.Context, agentID string, decayFactor float64) error

	// Composite query
	ContextFor(ctx context.Context, opts ContextForOpts) (*EntityRecall, error)

	// Stats
	GetStats(ctx context.Context, agentID string) (*GraphStats, error)

	Close() error
}

// RecallOpts configures a memory recall query.
type RecallOpts struct {
	AgentID     string
	Query       string   // FTS search query
	MemoryType  string   // filter (empty = all)
	EntityIDs   []string // scope to entities (empty = all)
	Tags        []string // filter by tags
	MinSalience float64
	MaxTokens   int // 0 = no limit
	Limit       int // max results
}

// ContextForOpts configures a composite context query.
type ContextForOpts struct {
	AgentID    string
	EntityName string
	Topic      string // FTS query for relevant memories
	MaxTokens  int    // total budget
}
