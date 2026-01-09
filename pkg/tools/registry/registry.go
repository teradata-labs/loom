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

	_ "github.com/mutecomm/go-sqlcipher/v4"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/types"
)

// Registry manages the tool index and provides search capabilities.
type Registry struct {
	db       *sql.DB
	llm      types.LLMProvider
	tracer   observability.Tracer
	mu       sync.RWMutex
	indexers []Indexer
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

// Config holds registry configuration.
type Config struct {
	DBPath   string            // Path to SQLite database
	LLM      types.LLMProvider // LLM provider for search assistance
	Tracer   observability.Tracer
	Indexers []Indexer // Tool source indexers
}

// New creates a new tool registry.
func New(cfg Config) (*Registry, error) {
	if cfg.Tracer == nil {
		cfg.Tracer = observability.NewNoOpTracer()
	}

	// Open SQLite database with FTS5 support
	db, err := sql.Open("sqlite3", cfg.DBPath+"?_fts5=1")
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	r := &Registry{
		db:       db,
		llm:      cfg.LLM,
		tracer:   cfg.Tracer,
		indexers: cfg.Indexers,
	}

	// Initialize schema
	if err := r.initSchema(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to initialize schema: %w", err)
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
		tools, err := indexer.Index(ctx)
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

		// Update counts
		switch indexer.Source() {
		case loomv1.ToolSource_TOOL_SOURCE_BUILTIN:
			resp.BuiltinCount = int32(len(tools))
		case loomv1.ToolSource_TOOL_SOURCE_MCP:
			resp.McpCount += int32(len(tools))
		case loomv1.ToolSource_TOOL_SOURCE_CUSTOM:
			resp.CustomCount = int32(len(tools))
		}

		// Update source tracking
		r.updateSourceInfo(ctx, indexer.Name(), indexer.Source(), len(tools), true, "indexed successfully")
	}

	resp.TotalCount = resp.BuiltinCount + resp.McpCount + resp.CustomCount
	resp.Errors = errors
	resp.DurationMs = time.Since(start).Milliseconds()

	span.Status = observability.Status{
		Code:    observability.StatusOK,
		Message: fmt.Sprintf("Indexed %d tools", resp.TotalCount),
	}

	return resp, nil
}

// upsertTool inserts or updates a tool in the database.
func (r *Registry) upsertTool(ctx context.Context, tool *loomv1.IndexedTool) error {
	capabilities, _ := json.Marshal(tool.Capabilities)
	keywords, _ := json.Marshal(tool.Keywords)
	examples, _ := json.Marshal(tool.Examples)
	rateLimit, _ := json.Marshal(tool.RateLimit)

	_, err := r.db.ExecContext(ctx, `
		INSERT OR REPLACE INTO tools (
			id, name, description, source, mcp_server, input_schema, output_schema,
			capabilities, keywords, examples, indexed_at, version, requires_approval, rate_limit
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
	metadata.CandidatesRetrieved = int32(len(candidates))

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
		sql += " AND t.source IN (" + strings.Join(placeholders, ",") + ")"
	}

	// Add capability filters (check if any capability matches)
	if len(capFilters) > 0 {
		for _, cap := range capFilters {
			sql += " AND t.capabilities LIKE ?"
			args = append(args, "%"+cap+"%")
		}
	}

	sql += " ORDER BY score LIMIT ?"
	args = append(args, limit)

	rows, err := r.db.QueryContext(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

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
	defer rows.Close()

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
		sql += " AND source IN (" + strings.Join(placeholders, ",") + ")"
	}

	sql += " LIMIT ?"
	args = append(args, maxResults)

	rows, err := r.db.QueryContext(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

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
