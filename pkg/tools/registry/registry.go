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
// Package registry provides tool indexing and search capabilities.
// It maintains an FTS5 index of all available tools (builtin, MCP, custom)
// and supports LLM-assisted search for high accuracy tool discovery.
package registry

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	_ "github.com/teradata-labs/loom/internal/sqlitedriver"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/types"
	"go.uber.org/zap"
)

// Registry manages the tool index and provides search capabilities.
type Registry struct {
	db             *sql.DB
	llm            types.LLMProvider
	tracer         observability.Tracer
	logger         *zap.Logger
	mu             sync.RWMutex
	indexers       []Indexer
	liveMCPServers func() []string
}

// Indexer is an interface for tool source indexers.
type Indexer interface {
	// Name returns the indexer name for logging.
	Name() string

	// Source returns the tool source type.
	Source() loomv1.ToolSource

	// Index indexes all tools from this source.
	Index(ctx context.Context) ([]*loomv1.IndexedTool, error)
}

// IndexOutcome is the result of an index run that can vouch for the
// completeness of what it reports, so IndexAll can prune rows the run did
// not re-report instead of accumulating them forever (issue #334).
//
// A scope is the value of the tools table's mcp_server column: the server
// name for MCP tools, "" for sources that don't partition by server.
type IndexOutcome struct {
	// Tools is the set of tools reported by this run.
	Tools []*loomv1.IndexedTool

	// CompleteScopes lists the scopes for which Tools is the complete
	// current set. Indexed rows in these scopes that are absent from Tools
	// are stale and are deleted. A scope that failed to enumerate this run
	// (e.g. an unreachable MCP server) must NOT be listed here, so a
	// transient failure never wipes a server's tools.
	CompleteScopes []string

	// KnownScopes lists every scope that still exists for this source
	// (e.g. every configured MCP server, reachable or not). Only consulted
	// when PruneOrphanScopes is true.
	KnownScopes []string

	// PruneOrphanScopes, when true, deletes rows of this source whose scope
	// is not in KnownScopes — the "server was removed from configuration"
	// case. With an empty KnownScopes it deletes every row of the source.
	PruneOrphanScopes bool
}

// ReconcilingIndexer is an optional extension of Indexer. Indexers that
// implement it get stale rows pruned after each run; plain Indexers keep
// the historical upsert-only behavior (required for sources like custom
// tools, where rows are registered out-of-band via RegisterTool).
type ReconcilingIndexer interface {
	Indexer

	// IndexWithOutcome indexes all tools and reports the reconciliation
	// boundaries of the run.
	IndexWithOutcome(ctx context.Context) (*IndexOutcome, error)
}

// Config holds registry configuration.
type Config struct {
	DBPath   string            // Path to SQLite database
	LLM      types.LLMProvider // LLM provider for search assistance
	Tracer   observability.Tracer
	Logger   *zap.Logger // Structured logger for prune/evict audit trails; nil uses a no-op logger
	Indexers []Indexer   // Tool source indexers

	// LiveMCPServers, when set, returns the names of the MCP servers that
	// currently exist; search results are then restricted to MCP tools from
	// these servers, so stale index rows are never surfaced to agents even
	// between reconciliation runs. nil disables the filter. An empty result
	// filters out all MCP tools.
	LiveMCPServers func() []string
}

// New creates a new tool registry.
func New(cfg Config) (*Registry, error) {
	if cfg.Tracer == nil {
		cfg.Tracer = observability.NewNoOpTracer()
	}
	if cfg.Logger == nil {
		cfg.Logger = zap.NewNop()
	}

	// Open SQLite database with FTS5 support
	db, err := sql.Open("sqlite3", cfg.DBPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	r := &Registry{
		db:             db,
		llm:            cfg.LLM,
		tracer:         cfg.Tracer,
		logger:         cfg.Logger,
		indexers:       cfg.Indexers,
		liveMCPServers: cfg.LiveMCPServers,
	}

	// Initialize schema
	if err := r.initSchema(); err != nil {
		_ = db.Close() // Best-effort cleanup; initSchema error takes priority
		return nil, fmt.Errorf("failed to initialize schema: %w", err)
	}

	// Rebuild the FTS index from the content table, once per open. Databases
	// written by earlier builds were populated via INSERT OR REPLACE, whose
	// implicit DELETE does not fire the tools_ad trigger (recursive_triggers
	// is off by default), leaving ghost FTS postings behind; a later DELETE
	// could free the max rowid, which SQLite then reuses for a new tool,
	// attaching the ghost postings to the wrong tool (wrong search results and
	// a failing FTS5 integrity-check). The rebuild is idempotent and cheap at
	// open time, and repairs any such database in place.
	if _, err := db.Exec(`INSERT INTO tools_fts(tools_fts) VALUES('rebuild')`); err != nil {
		_ = db.Close() // Best-effort cleanup; rebuild error takes priority
		return nil, fmt.Errorf("failed to rebuild FTS index: %w", err)
	}

	return r, nil
}

// initSchema creates the database schema including FTS5 tables.
func (r *Registry) initSchema() error {
	schema := `
	-- Main tools table
	CREATE TABLE IF NOT EXISTS tools (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		description TEXT NOT NULL,
		source INTEGER NOT NULL,
		mcp_server TEXT,
		input_schema TEXT,
		output_schema TEXT,
		capabilities TEXT,  -- JSON array
		keywords TEXT,      -- JSON array
		examples TEXT,      -- JSON array
		indexed_at TEXT NOT NULL,
		version TEXT,
		requires_approval INTEGER DEFAULT 0,
		rate_limit TEXT     -- JSON object
	);

	-- FTS5 virtual table for full-text search
	-- Uses BM25 ranking with boosted weights for name and capabilities
	CREATE VIRTUAL TABLE IF NOT EXISTS tools_fts USING fts5(
		name,
		description,
		capabilities,
		keywords,
		content='tools',
		content_rowid='rowid',
		tokenize='porter unicode61'
	);

	-- Triggers to keep FTS in sync with main table
	CREATE TRIGGER IF NOT EXISTS tools_ai AFTER INSERT ON tools BEGIN
		INSERT INTO tools_fts(rowid, name, description, capabilities, keywords)
		VALUES (new.rowid, new.name, new.description, new.capabilities, new.keywords);
	END;

	CREATE TRIGGER IF NOT EXISTS tools_ad AFTER DELETE ON tools BEGIN
		INSERT INTO tools_fts(tools_fts, rowid, name, description, capabilities, keywords)
		VALUES ('delete', old.rowid, old.name, old.description, old.capabilities, old.keywords);
	END;

	CREATE TRIGGER IF NOT EXISTS tools_au AFTER UPDATE ON tools BEGIN
		INSERT INTO tools_fts(tools_fts, rowid, name, description, capabilities, keywords)
		VALUES ('delete', old.rowid, old.name, old.description, old.capabilities, old.keywords);
		INSERT INTO tools_fts(rowid, name, description, capabilities, keywords)
		VALUES (new.rowid, new.name, new.description, new.capabilities, new.keywords);
	END;

	-- Indexes for common queries
	CREATE INDEX IF NOT EXISTS idx_tools_source ON tools(source);
	CREATE INDEX IF NOT EXISTS idx_tools_mcp_server ON tools(mcp_server);

	-- Tool sources tracking table
	CREATE TABLE IF NOT EXISTS tool_sources (
		name TEXT PRIMARY KEY,
		type INTEGER NOT NULL,
		description TEXT,
		tool_count INTEGER DEFAULT 0,
		last_indexed TEXT,
		available INTEGER DEFAULT 1,
		status_message TEXT
	);
	`

	_, err := r.db.Exec(schema)
	return err
}

// Close closes the registry and its database connection.
func (r *Registry) Close() error {
	return r.db.Close()
}

// IndexAll indexes tools from all registered indexers.
func (r *Registry) IndexAll(ctx context.Context) (*loomv1.IndexToolsResponse, error) {
	ctx, span := r.tracer.StartSpan(ctx, "tools.registry.index_all")
	defer r.tracer.EndSpan(span)

	start := time.Now()
	resp := &loomv1.IndexToolsResponse{}
	var errors []*loomv1.IndexError

	r.mu.Lock()
	defer r.mu.Unlock()

	for _, indexer := range r.indexers {
		var tools []*loomv1.IndexedTool
		var outcome *IndexOutcome
		var err error

		if reconciler, ok := indexer.(ReconcilingIndexer); ok {
			outcome, err = reconciler.IndexWithOutcome(ctx)
			if outcome != nil {
				tools = outcome.Tools
			}
		} else {
			tools, err = indexer.Index(ctx)
		}
		if err != nil {
			errors = append(errors, &loomv1.IndexError{
				Source:       indexer.Source(),
				ErrorMessage: err.Error(),
			})
			continue
		}

		// Insert tools into database
		for _, tool := range tools {
			if err := r.upsertTool(ctx, tool); err != nil {
				errors = append(errors, &loomv1.IndexError{
					Source:       indexer.Source(),
					ServerName:   tool.McpServer,
					ErrorMessage: fmt.Sprintf("failed to index tool %s: %v", tool.Name, err),
				})
				continue
			}
		}

		// Prune rows this run vouches are stale (removed servers, removed tools).
		if outcome != nil {
			pruned, pruneErrs := r.pruneStaleRows(ctx, indexer.Source(), outcome)
			resp.PrunedCount += types.SafeInt32(pruned)
			errors = append(errors, pruneErrs...)
		}

		// Update counts
		switch indexer.Source() {
		case loomv1.ToolSource_TOOL_SOURCE_BUILTIN:
			resp.BuiltinCount = types.SafeInt32(len(tools))
		case loomv1.ToolSource_TOOL_SOURCE_MCP:
			resp.McpCount += types.SafeInt32(len(tools))
		case loomv1.ToolSource_TOOL_SOURCE_CUSTOM:
			resp.CustomCount = types.SafeInt32(len(tools))
		}

		// Update source tracking
		r.updateSourceInfo(ctx, indexer.Name(), indexer.Source(), len(tools), true, "indexed successfully")
	}

	resp.TotalCount = resp.BuiltinCount + resp.McpCount + resp.CustomCount
	resp.Errors = errors
	resp.DurationMs = time.Since(start).Milliseconds()

	span.Status = observability.Status{
		Code:    observability.StatusOK,
		Message: fmt.Sprintf("Indexed %d tools, pruned %d stale", resp.TotalCount, resp.PrunedCount),
	}

	return resp, nil
}

// pruneStaleRows deletes rows the outcome vouches are stale: rows in scopes
// that no longer exist (PruneOrphanScopes/KnownScopes) and rows in
// CompleteScopes that this run did not re-report. Returns the number of rows
// deleted; failures are reported as IndexErrors, never as a hard failure —
// a prune problem must not abort indexing. Caller holds r.mu.
func (r *Registry) pruneStaleRows(ctx context.Context, source loomv1.ToolSource, outcome *IndexOutcome) (int, []*loomv1.IndexError) {
	var errs []*loomv1.IndexError
	pruned := 0

	// Group the reported tool IDs by scope for the complete-scope diffs.
	keepByScope := make(map[string]map[string]bool)
	for _, tool := range outcome.Tools {
		scope := tool.McpServer
		if keepByScope[scope] == nil {
			keepByScope[scope] = make(map[string]bool)
		}
		keepByScope[scope][tool.Id] = true
	}

	// 1. Orphaned scopes: the scope (MCP server) is gone from configuration.
	if outcome.PruneOrphanScopes {
		known := make(map[string]bool, len(outcome.KnownScopes))
		for _, s := range outcome.KnownScopes {
			known[s] = true
		}
		deleted, err := r.deleteRowsWhere(ctx, source, func(scope, id string) bool {
			return !known[scope]
		})
		pruned += len(deleted)
		r.logPruned("server removed from configuration", source, deleted)
		if err != nil {
			errs = append(errs, &loomv1.IndexError{
				Source:       source,
				ErrorMessage: fmt.Sprintf("failed to prune orphaned scopes: %v", err),
			})
		}
	}

	// 2. Complete scopes: the scope still exists and this run enumerated it
	// fully, so any row not re-reported is a removed tool.
	if len(outcome.CompleteScopes) > 0 {
		complete := make(map[string]bool, len(outcome.CompleteScopes))
		for _, s := range outcome.CompleteScopes {
			complete[s] = true
		}
		deleted, err := r.deleteRowsWhere(ctx, source, func(scope, id string) bool {
			return complete[scope] && !keepByScope[scope][id]
		})
		pruned += len(deleted)
		r.logPruned("tool no longer reported by its server", source, deleted)
		if err != nil {
			errs = append(errs, &loomv1.IndexError{
				Source:       source,
				ErrorMessage: fmt.Sprintf("failed to prune removed tools: %v", err),
			})
		}
	}

	return pruned, errs
}

// logPruned records exactly which index rows a prune removed, so a tool that
// disappears from search is traceable to the reconciliation decision that
// deleted it. No-op when nothing was deleted.
func (r *Registry) logPruned(reason string, source loomv1.ToolSource, deleted []prunedRow) {
	if len(deleted) == 0 {
		return
	}
	ids := make([]string, len(deleted))
	scopeSet := make(map[string]bool, len(deleted))
	scopes := make([]string, 0, len(deleted))
	for i, row := range deleted {
		ids[i] = row.id
		if !scopeSet[row.scope] {
			scopeSet[row.scope] = true
			scopes = append(scopes, row.scope)
		}
	}
	r.logger.Info("Pruned stale tool index entries",
		zap.String("reason", reason),
		zap.String("source", source.String()),
		zap.Strings("tool_ids", ids),
		zap.Strings("mcp_servers", scopes))
}

// prunedRow identifies one deleted index row: its tool ID and its scope (the
// tools.mcp_server value, "" for sources that don't partition by server).
type prunedRow struct {
	id    string
	scope string
}

// deleteRowsWhere deletes every row of the given source for which stale
// returns true, evaluated over (scope, id), and returns the rows it deleted.
// The candidate set is read first and deleted by ID, so the work stays clear
// of SQLite bind-variable limits regardless of tool count. Caller holds r.mu.
func (r *Registry) deleteRowsWhere(ctx context.Context, source loomv1.ToolSource, stale func(scope, id string) bool) ([]prunedRow, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, COALESCE(mcp_server, '') FROM tools WHERE source = ?`, int(source))
	if err != nil {
		return nil, err
	}

	var staleRows []prunedRow
	for rows.Next() {
		var id, scope string
		if err := rows.Scan(&id, &scope); err != nil {
			continue
		}
		if stale(scope, id) {
			staleRows = append(staleRows, prunedRow{id: id, scope: scope})
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()

	var deleted []prunedRow
	for _, row := range staleRows {
		if _, err := r.db.ExecContext(ctx, `DELETE FROM tools WHERE id = ?`, row.id); err != nil {
			return deleted, err
		}
		deleted = append(deleted, row)
	}
	return deleted, nil
}

// EvictTool removes a single tool from the index by ID. Used when dynamic
// registration proves an entry stale (its MCP server no longer exists), so
// the same dead tool is never served twice.
func (r *Registry) EvictTool(ctx context.Context, toolID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	res, err := r.db.ExecContext(ctx, `DELETE FROM tools WHERE id = ?`, toolID)
	if err != nil {
		return err
	}
	if n, raErr := res.RowsAffected(); raErr == nil && n > 0 {
		r.logger.Info("Evicted stale tool index entry", zap.String("tool_id", toolID))
	}
	return nil
}

// RegisterTool registers or updates a single tool in the database.
// This is the exported entry point for registering custom tools via the gRPC API.
// Thread-safe: acquires write lock before database access.
func (r *Registry) RegisterTool(ctx context.Context, tool *loomv1.IndexedTool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.upsertTool(ctx, tool)
}

// upsertTool inserts or updates a tool in the database.
//
// The upsert MUST be INSERT ... ON CONFLICT DO UPDATE, never INSERT OR
// REPLACE: OR REPLACE resolves the conflict by deleting the existing row and
// inserting a new one with a fresh rowid, and that implicit DELETE does not
// fire the tools_ad trigger (recursive_triggers is off by default), leaving
// ghost postings in the external-content tools_fts index. ON CONFLICT DO
// UPDATE keeps the rowid stable, so the tools_au UPDATE trigger keeps the FTS
// index in sync. The conflict target is the table's primary key, tools(id).
func (r *Registry) upsertTool(ctx context.Context, tool *loomv1.IndexedTool) error {
	capabilities, _ := json.Marshal(tool.Capabilities)
	keywords, _ := json.Marshal(tool.Keywords)
	examples, _ := json.Marshal(tool.Examples)
	rateLimit, _ := json.Marshal(tool.RateLimit)

	_, err := r.db.ExecContext(ctx, `
		INSERT INTO tools (
			id, name, description, source, mcp_server, input_schema, output_schema,
			capabilities, keywords, examples, indexed_at, version, requires_approval, rate_limit
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			description = excluded.description,
			source = excluded.source,
			mcp_server = excluded.mcp_server,
			input_schema = excluded.input_schema,
			output_schema = excluded.output_schema,
			capabilities = excluded.capabilities,
			keywords = excluded.keywords,
			examples = excluded.examples,
			indexed_at = excluded.indexed_at,
			version = excluded.version,
			requires_approval = excluded.requires_approval,
			rate_limit = excluded.rate_limit
	`,
		tool.Id, tool.Name, tool.Description, tool.Source, tool.McpServer,
		tool.InputSchema, tool.OutputSchema, string(capabilities), string(keywords),
		string(examples), tool.IndexedAt, tool.Version, tool.RequiresApproval, string(rateLimit),
	)

	return err
}

// updateSourceInfo updates the tool_sources table.
func (r *Registry) updateSourceInfo(ctx context.Context, name string, source loomv1.ToolSource, count int, available bool, message string) {
	_, _ = r.db.ExecContext(ctx, `
		INSERT OR REPLACE INTO tool_sources (name, type, tool_count, last_indexed, available, status_message)
		VALUES (?, ?, ?, ?, ?, ?)
	`, name, source, count, time.Now().Format(time.RFC3339), available, message)
}

// Search performs LLM-assisted tool search.
func (r *Registry) Search(ctx context.Context, req *loomv1.SearchToolsRequest) (*loomv1.SearchToolsResponse, error) {
	ctx, span := r.tracer.StartSpan(ctx, "tools.registry.search")
	defer r.tracer.EndSpan(span)

	start := time.Now()
	metadata := &loomv1.SearchMetadata{}

	// Default mode to BALANCED
	mode := req.Mode
	if mode == loomv1.SearchMode_SEARCH_MODE_UNSPECIFIED {
		mode = loomv1.SearchMode_SEARCH_MODE_BALANCED
	}
	metadata.ModeUsed = mode

	// Get total indexed count
	var totalIndexed int32
	_ = r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM tools").Scan(&totalIndexed)
	metadata.TotalIndexed = totalIndexed

	// Stage 1: Query understanding (for ACCURATE mode)
	var expandedTerms []string
	if mode == loomv1.SearchMode_SEARCH_MODE_ACCURATE && r.llm != nil {
		queryStart := time.Now()
		expandedTerms = r.expandQuery(ctx, req.Query, req.TaskContext)
		metadata.QueryUnderstandingMs = time.Since(queryStart).Milliseconds()
		metadata.ExpandedTerms = expandedTerms
	}

	// Stage 2: FTS5 retrieval
	ftsStart := time.Now()
	candidates, err := r.ftsSearch(ctx, req.Query, expandedTerms, req.CapabilityFilters, req.SourceFilters, 20)
	if err != nil {
		return nil, fmt.Errorf("FTS search failed: %w", err)
	}
	metadata.FtsRetrievalMs = time.Since(ftsStart).Milliseconds()
	metadata.CandidatesRetrieved = types.SafeInt32(len(candidates))

	// Stage 3: LLM re-ranking (for BALANCED and ACCURATE modes)
	var results []*loomv1.ToolSearchResult
	if (mode == loomv1.SearchMode_SEARCH_MODE_BALANCED || mode == loomv1.SearchMode_SEARCH_MODE_ACCURATE) && r.llm != nil && len(candidates) > 0 {
		rerankStart := time.Now()
		results = r.rerankWithLLM(ctx, req.Query, req.TaskContext, candidates)
		metadata.LlmRerankingMs = time.Since(rerankStart).Milliseconds()
	} else {
		// FAST mode or no LLM - use FTS scores directly
		results = candidates
	}

	// Limit results
	maxResults := int(req.MaxResults)
	if maxResults <= 0 {
		maxResults = 5
	}
	if maxResults > 20 {
		maxResults = 20
	}
	if len(results) > maxResults {
		results = results[:maxResults]
	}

	// Optionally strip schema if not requested
	if !req.IncludeSchema {
		for _, result := range results {
			result.Tool.InputSchema = ""
			result.Tool.OutputSchema = ""
		}
	}

	metadata.TotalMs = time.Since(start).Milliseconds()

	span.Status = observability.Status{
		Code:    observability.StatusOK,
		Message: fmt.Sprintf("Found %d results in %dms", len(results), metadata.TotalMs),
	}

	return &loomv1.SearchToolsResponse{
		Results:  results,
		Metadata: metadata,
	}, nil
}

// ftsSearch performs FTS5 full-text search.
func (r *Registry) ftsSearch(ctx context.Context, query string, expandedTerms []string, capFilters []string, sourceFilters []loomv1.ToolSource, limit int) ([]*loomv1.ToolSearchResult, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// Build search terms - split query into individual words plus expanded terms
	searchTerms := strings.Fields(query)
	searchTerms = append(searchTerms, expandedTerms...)

	// Escape and prepare terms for FTS5 - use individual words with OR
	seen := make(map[string]bool)
	var ftsQuery strings.Builder
	first := true
	for _, term := range searchTerms {
		// Skip empty terms
		term = strings.TrimSpace(term)
		if term == "" {
			continue
		}
		// Skip duplicates
		if seen[strings.ToLower(term)] {
			continue
		}
		seen[strings.ToLower(term)] = true

		if !first {
			ftsQuery.WriteString(" OR ")
		}
		first = false

		// Simple escaping - replace quotes and wrap in quotes for exact word match
		escaped := strings.ReplaceAll(term, "\"", "\"\"")
		ftsQuery.WriteString("\"")
		ftsQuery.WriteString(escaped)
		ftsQuery.WriteString("\"")
	}

	// Build SQL with optional filters
	sql := `
		SELECT t.id, t.name, t.description, t.source, t.mcp_server,
			   t.input_schema, t.output_schema, t.capabilities, t.keywords,
			   t.examples, t.indexed_at, t.version, t.requires_approval, t.rate_limit,
			   bm25(tools_fts, 10.0, 5.0, 3.0, 2.0) as score
		FROM tools t
		JOIN tools_fts ON t.rowid = tools_fts.rowid
		WHERE tools_fts MATCH ?
	`

	args := []interface{}{ftsQuery.String()}

	// Add source filters
	if len(sourceFilters) > 0 {
		placeholders := make([]string, len(sourceFilters))
		for i, s := range sourceFilters {
			placeholders[i] = "?"
			args = append(args, int(s))
		}
		sql += " AND t.source IN (" + strings.Join(placeholders, ",") + ")" // #nosec G202 -- placeholders are literal "?" strings, values are parameterized
	}

	// Add capability filters (check if any capability matches)
	if len(capFilters) > 0 {
		for _, cap := range capFilters {
			sql += " AND t.capabilities LIKE ?"
			args = append(args, "%"+cap+"%")
		}
	}

	// Restrict MCP tools to live servers so stale index rows are never
	// surfaced to agents (issue #334).
	livenessSQL, livenessArgs := r.mcpLivenessClause("t")
	sql += livenessSQL // #nosec G202 -- clause contains only literal "?" placeholders, values are parameterized
	args = append(args, livenessArgs...)

	sql += " ORDER BY score LIMIT ?"
	args = append(args, limit)

	rows, err := r.db.QueryContext(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var results []*loomv1.ToolSearchResult
	for rows.Next() {
		tool := &loomv1.IndexedTool{}
		var capabilities, keywords, examples, rateLimit string
		var score float64

		err := rows.Scan(
			&tool.Id, &tool.Name, &tool.Description, &tool.Source, &tool.McpServer,
			&tool.InputSchema, &tool.OutputSchema, &capabilities, &keywords,
			&examples, &tool.IndexedAt, &tool.Version, &tool.RequiresApproval, &rateLimit,
			&score,
		)
		if err != nil {
			continue
		}

		// Parse JSON arrays
		_ = json.Unmarshal([]byte(capabilities), &tool.Capabilities)
		_ = json.Unmarshal([]byte(keywords), &tool.Keywords)

		var toolExamples []*loomv1.ToolExample
		_ = json.Unmarshal([]byte(examples), &toolExamples)
		tool.Examples = toolExamples

		var rl loomv1.RateLimitInfo
		_ = json.Unmarshal([]byte(rateLimit), &rl)
		tool.RateLimit = &rl

		// Convert BM25 score to confidence (BM25 is negative, lower is better)
		// Normalize to 0-1 range
		confidence := 1.0 / (1.0 + (-score / 10.0))

		results = append(results, &loomv1.ToolSearchResult{
			Tool:       tool,
			Confidence: confidence,
			Signals: []*loomv1.RelevanceSignal{
				{SignalType: "bm25_score", Description: "FTS5 BM25 ranking", Weight: score},
			},
		})
	}

	return results, nil
}

// mcpLivenessClause returns a SQL fragment (leading " AND ...") restricting
// MCP-sourced rows to currently-live servers, with its bind arguments. It is
// a no-op ("" and nil) when no liveness callback is configured. With a
// callback returning no servers, every MCP row is excluded: a tool whose
// server does not exist cannot be executed, so surfacing it only misleads
// the agent (issue #334).
func (r *Registry) mcpLivenessClause(alias string) (string, []interface{}) {
	if r.liveMCPServers == nil {
		return "", nil
	}
	col := "mcp_server"
	srcCol := "source"
	if alias != "" {
		col = alias + "." + col
		srcCol = alias + "." + srcCol
	}

	live := r.liveMCPServers()
	if len(live) == 0 {
		return fmt.Sprintf(" AND %s != ?", srcCol),
			[]interface{}{int(loomv1.ToolSource_TOOL_SOURCE_MCP)}
	}

	placeholders := make([]string, len(live))
	args := make([]interface{}, 0, len(live)+1)
	args = append(args, int(loomv1.ToolSource_TOOL_SOURCE_MCP))
	for i, server := range live {
		placeholders[i] = "?"
		args = append(args, server)
	}
	clause := fmt.Sprintf(" AND (%s != ? OR COALESCE(%s, '') IN (%s))",
		srcCol, col, strings.Join(placeholders, ",")) // #nosec G201 -- placeholders are literal "?" strings, values are parameterized
	return clause, args
}

// expandQuery uses LLM to expand the search query with synonyms and related terms.
func (r *Registry) expandQuery(ctx context.Context, query, taskContext string) []string {
	if r.llm == nil {
		return nil
	}

	prompt := fmt.Sprintf(`Given this tool search query: "%s"
%s
Generate 5-10 relevant search terms (synonyms, related concepts, technical terms) that would help find matching tools.
Return ONLY a JSON array of strings, no explanation.

Example output: ["send", "message", "notification", "alert", "webhook", "post"]`, query, func() string {
		if taskContext != "" {
			return fmt.Sprintf("Task context: %s", taskContext)
		}
		return ""
	}())

	messages := []types.Message{
		{Role: "user", Content: prompt},
	}

	response, err := r.llm.Chat(ctx, messages, nil)
	if err != nil || response == nil {
		return nil
	}

	var terms []string
	_ = json.Unmarshal([]byte(response.Content), &terms)
	return terms
}

// rerankWithLLM uses LLM to re-rank search candidates for better accuracy.
func (r *Registry) rerankWithLLM(ctx context.Context, query, taskContext string, candidates []*loomv1.ToolSearchResult) []*loomv1.ToolSearchResult {
	if r.llm == nil || len(candidates) == 0 {
		return candidates
	}

	// Build tool descriptions for LLM
	var toolDescs strings.Builder
	for i, c := range candidates {
		toolDescs.WriteString(fmt.Sprintf("%d. %s: %s\n", i+1, c.Tool.Name, c.Tool.Description))
	}

	prompt := fmt.Sprintf(`Rank these tools by relevance to the query: "%s"
%s
Tools:
%s
Return a JSON array of objects with "index" (1-based) and "score" (0.0-1.0) and "reason" (brief explanation).
Only include tools with score > 0.3. Order by score descending.

Example output: [{"index": 2, "score": 0.95, "reason": "Exact match for slack notification"}, {"index": 1, "score": 0.7, "reason": "Can send webhooks but not slack-specific"}]`,
		query,
		func() string {
			if taskContext != "" {
				return fmt.Sprintf("Task context: %s", taskContext)
			}
			return ""
		}(),
		toolDescs.String())

	messages := []types.Message{
		{Role: "user", Content: prompt},
	}

	response, err := r.llm.Chat(ctx, messages, nil)
	if err != nil || response == nil {
		return candidates
	}

	// Parse LLM response
	var rankings []struct {
		Index  int     `json:"index"`
		Score  float64 `json:"score"`
		Reason string  `json:"reason"`
	}
	if err := json.Unmarshal([]byte(response.Content), &rankings); err != nil {
		return candidates
	}

	// Build re-ranked results
	var reranked []*loomv1.ToolSearchResult
	for _, rank := range rankings {
		if rank.Index < 1 || rank.Index > len(candidates) {
			continue
		}
		result := candidates[rank.Index-1]
		result.Confidence = rank.Score
		result.MatchReason = rank.Reason
		result.Signals = append(result.Signals, &loomv1.RelevanceSignal{
			SignalType:  "llm_rerank",
			Description: rank.Reason,
			Weight:      rank.Score,
		})
		reranked = append(reranked, result)
	}

	if len(reranked) == 0 {
		return candidates
	}

	return reranked
}

// GetTool retrieves a specific tool by ID.
func (r *Registry) GetTool(ctx context.Context, toolID string) (*loomv1.IndexedTool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	row := r.db.QueryRowContext(ctx, `
		SELECT id, name, description, source, mcp_server, input_schema, output_schema,
			   capabilities, keywords, examples, indexed_at, version, requires_approval, rate_limit
		FROM tools WHERE id = ?
	`, toolID)

	tool := &loomv1.IndexedTool{}
	var capabilities, keywords, examples, rateLimit string

	err := row.Scan(
		&tool.Id, &tool.Name, &tool.Description, &tool.Source, &tool.McpServer,
		&tool.InputSchema, &tool.OutputSchema, &capabilities, &keywords,
		&examples, &tool.IndexedAt, &tool.Version, &tool.RequiresApproval, &rateLimit,
	)
	if err != nil {
		return nil, fmt.Errorf("tool not found: %s", toolID)
	}

	// Parse JSON arrays
	_ = json.Unmarshal([]byte(capabilities), &tool.Capabilities)
	_ = json.Unmarshal([]byte(keywords), &tool.Keywords)

	var toolExamples []*loomv1.ToolExample
	_ = json.Unmarshal([]byte(examples), &toolExamples)
	tool.Examples = toolExamples

	var rl loomv1.RateLimitInfo
	_ = json.Unmarshal([]byte(rateLimit), &rl)
	tool.RateLimit = &rl

	return tool, nil
}

// ListSources returns all registered tool sources.
func (r *Registry) ListSources(ctx context.Context) ([]*loomv1.ToolSourceInfo, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	rows, err := r.db.QueryContext(ctx, `
		SELECT name, type, description, tool_count, last_indexed, available, status_message
		FROM tool_sources
	`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var sources []*loomv1.ToolSourceInfo
	for rows.Next() {
		source := &loomv1.ToolSourceInfo{}
		var description sql.NullString
		var statusMessage sql.NullString

		err := rows.Scan(
			&source.Name, &source.Type, &description, &source.ToolCount,
			&source.LastIndexed, &source.Available, &statusMessage,
		)
		if err != nil {
			continue
		}

		if description.Valid {
			source.Description = description.String
		}
		if statusMessage.Valid {
			source.StatusMessage = statusMessage.String
		}

		sources = append(sources, source)
	}

	return sources, nil
}

// GetToolsByCapability returns tools with a specific capability tag.
func (r *Registry) GetToolsByCapability(ctx context.Context, capability string, sourceFilters []loomv1.ToolSource, maxResults int) ([]*loomv1.IndexedTool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if maxResults <= 0 {
		maxResults = 10
	}

	sql := `SELECT id, name, description, source, mcp_server, input_schema, output_schema,
			       capabilities, keywords, examples, indexed_at, version, requires_approval, rate_limit
			FROM tools WHERE capabilities LIKE ?`

	args := []interface{}{"%" + capability + "%"}

	if len(sourceFilters) > 0 {
		placeholders := make([]string, len(sourceFilters))
		for i, s := range sourceFilters {
			placeholders[i] = "?"
			args = append(args, int(s))
		}
		sql += " AND source IN (" + strings.Join(placeholders, ",") + ")" // #nosec G202 -- placeholders are literal "?" strings, values are parameterized
	}

	// Restrict MCP tools to live servers (issue #334).
	livenessSQL, livenessArgs := r.mcpLivenessClause("")
	sql += livenessSQL // #nosec G202 -- clause contains only literal "?" placeholders, values are parameterized
	args = append(args, livenessArgs...)

	sql += " LIMIT ?"
	args = append(args, maxResults)

	rows, err := r.db.QueryContext(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var tools []*loomv1.IndexedTool
	for rows.Next() {
		tool := &loomv1.IndexedTool{}
		var capabilities, keywords, examples, rateLimit string

		err := rows.Scan(
			&tool.Id, &tool.Name, &tool.Description, &tool.Source, &tool.McpServer,
			&tool.InputSchema, &tool.OutputSchema, &capabilities, &keywords,
			&examples, &tool.IndexedAt, &tool.Version, &tool.RequiresApproval, &rateLimit,
		)
		if err != nil {
			continue
		}

		_ = json.Unmarshal([]byte(capabilities), &tool.Capabilities)
		_ = json.Unmarshal([]byte(keywords), &tool.Keywords)

		var toolExamples []*loomv1.ToolExample
		_ = json.Unmarshal([]byte(examples), &toolExamples)
		tool.Examples = toolExamples

		var rl loomv1.RateLimitInfo
		_ = json.Unmarshal([]byte(rateLimit), &rl)
		tool.RateLimit = &rl

		tools = append(tools, tool)
	}

	return tools, nil
}
