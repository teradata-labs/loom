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
// Package agent provides dynamic tool discovery for MCP servers.
package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/teradata-labs/loom/pkg/mcp/adapter"
	"github.com/teradata-labs/loom/pkg/mcp/manager"
	"github.com/teradata-labs/loom/pkg/mcp/protocol"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/storage"
	"go.uber.org/zap"
)

// DynamicToolDiscovery enables runtime tool discovery using simple text search.
//
// Instead of registering all tools upfront, tools are discovered on-demand
// based on user intent. This prevents context window bloat while maintaining
// access to all available tools.
//
// Search Strategy:
//   - Simple text matching (case-insensitive) on tool name and description
//   - No complex NLP or embedding required
//   - Results cached for future use
//
// Example:
//
//	discovery := NewDynamicToolDiscovery(mcpMgr, logger)
//	tool, err := discovery.Search(ctx, "read file")
//	// Finds "filesystem:read_file" by matching "read" and "file"
type DynamicToolDiscovery struct {
	mcpMgr       *manager.Manager
	logger       *zap.Logger
	cache        map[string]shuttle.Tool // Intent → Tool cache
	mu           sync.RWMutex
	sqlStore     *storage.SQLResultStore    // For storing large SQL results
	sharedMemory *storage.SharedMemoryStore // For storing other large data
}

// NewDynamicToolDiscovery creates a new dynamic tool discovery system.
func NewDynamicToolDiscovery(mcpMgr *manager.Manager, logger *zap.Logger) *DynamicToolDiscovery {
	if logger == nil {
		logger = zap.NewNop()
	}

	return &DynamicToolDiscovery{
		mcpMgr:       mcpMgr,
		logger:       logger,
		cache:        make(map[string]shuttle.Tool),
		sqlStore:     nil, // Will be set by SetSQLResultStore if needed
		sharedMemory: nil, // Will be set by SetSharedMemory if needed
	}
}

// SetSQLResultStore configures SQL result store for dynamically discovered tools.
func (d *DynamicToolDiscovery) SetSQLResultStore(store *storage.SQLResultStore) {
	d.sqlStore = store
}

// SetSharedMemory configures shared memory store for dynamically discovered tools.
func (d *DynamicToolDiscovery) SetSharedMemory(store *storage.SharedMemoryStore) {
	d.sharedMemory = store
}

// Search finds a tool matching the user intent using simple text search.
//
// Search process:
//  1. Check cache for previously discovered tools
//  2. Search all MCP servers for matching tools
//  3. Use simple text matching on tool name and description
//  4. Cache the result for future use
//  5. Return the first matching tool
//
// Returns an error if no matching tool is found.
func (d *DynamicToolDiscovery) Search(ctx context.Context, intent string) (shuttle.Tool, error) {
	// Check cache first (fast path)
	d.mu.RLock()
	if tool, exists := d.cache[intent]; exists {
		d.mu.RUnlock()
		d.logger.Debug("Tool found in cache",
			zap.String("intent", intent),
			zap.String("tool", tool.Name()))
		return tool, nil
	}
	d.mu.RUnlock()

	d.logger.Debug("Searching for tool",
		zap.String("intent", intent))

	// Search all servers (slow path)
	for _, serverName := range d.mcpMgr.ServerNames() {
		client, err := d.mcpMgr.GetClient(serverName)
		if err != nil {
			d.logger.Warn("Failed to get client for dynamic search",
				zap.String("server", serverName),
				zap.Error(err))
			continue
		}

		// List all tools from this server
		tools, err := client.ListTools(ctx)
		if err != nil {
			d.logger.Warn("Failed to list tools for dynamic search",
				zap.String("server", serverName),
				zap.Error(err))
			continue
		}

		// Simple text matching on name and description
		for _, tool := range tools {
			if d.matches(intent, tool) {
				// Found a match! Convert to shuttle.Tool
				mcpAdapter := adapter.NewMCPToolAdapter(client, tool, serverName)

				// CRITICAL: Inject storage backends for progressive disclosure
				if d.sqlStore != nil {
					mcpAdapter.SetSQLResultStore(d.sqlStore)
				}
				if d.sharedMemory != nil {
					mcpAdapter.SetSharedMemory(d.sharedMemory)
				}

				// Cache for future use
				d.mu.Lock()
				d.cache[intent] = mcpAdapter
				d.mu.Unlock()

				d.logger.Info("Dynamically discovered tool",
					zap.String("intent", intent),
					zap.String("tool", mcpAdapter.Name()),
					zap.String("server", serverName))

				return mcpAdapter, nil
			}
		}
	}

	return nil, fmt.Errorf("no tool found for intent: %s", intent)
}

// SearchMultiple finds multiple tools matching the intent.
//
// Unlike Search which returns the first match, this returns all matching tools.
// Useful when you want to give the LLM multiple options.
func (d *DynamicToolDiscovery) SearchMultiple(ctx context.Context, intent string) ([]shuttle.Tool, error) {
	d.logger.Debug("Searching for multiple tools",
		zap.String("intent", intent))

	var matchingTools []shuttle.Tool

	// Search all servers
	for _, serverName := range d.mcpMgr.ServerNames() {
		client, err := d.mcpMgr.GetClient(serverName)
		if err != nil {
			continue
		}

		tools, err := client.ListTools(ctx)
		if err != nil {
			continue
		}

		// Find all matches
		for _, tool := range tools {
			if d.matches(intent, tool) {
				mcpAdapter := adapter.NewMCPToolAdapter(client, tool, serverName)

				// CRITICAL: Inject storage backends for progressive disclosure
				if d.sqlStore != nil {
					mcpAdapter.SetSQLResultStore(d.sqlStore)
				}
				if d.sharedMemory != nil {
					mcpAdapter.SetSharedMemory(d.sharedMemory)
				}

				matchingTools = append(matchingTools, mcpAdapter)
			}
		}
	}

	if len(matchingTools) == 0 {
		return nil, fmt.Errorf("no tools found for intent: %s", intent)
	}

	d.logger.Info("Dynamically discovered multiple tools",
		zap.String("intent", intent),
		zap.Int("count", len(matchingTools)))

	return matchingTools, nil
}

// matches checks if a tool satisfies the intent using simple text matching.
//
// Matching strategy:
//   - Convert intent and tool info to lowercase
//   - Check if tool name contains any words from intent
//   - Check if tool description contains any words from intent
//   - Returns true on first match
//
// This is intentionally simple. No NLP, no embeddings, no complexity.
// For 90% of use cases, simple string matching is sufficient.
func (d *DynamicToolDiscovery) matches(intent string, tool protocol.Tool) bool {
	intentLower := strings.ToLower(intent)
	intentWords := strings.Fields(intentLower)

	toolName := strings.ToLower(tool.Name)
	toolDesc := strings.ToLower(tool.Description)

	// Strategy 1: Check if tool name contains the full intent
	if strings.Contains(toolName, intentLower) {
		return true
	}

	// Strategy 2: Check if tool description contains the full intent
	if strings.Contains(toolDesc, intentLower) {
		return true
	}

	// Strategy 3: Check if tool name contains any word from intent
	for _, word := range intentWords {
		if len(word) > 2 && strings.Contains(toolName, word) {
			return true
		}
	}

	// Strategy 4: Check if tool description contains multiple words from intent
	matchCount := 0
	for _, word := range intentWords {
		if len(word) > 2 && strings.Contains(toolDesc, word) {
			matchCount++
		}
	}

	// If at least half the words match, consider it a match
	if len(intentWords) > 1 && matchCount >= len(intentWords)/2 {
		return true
	}

	return false
}

// ClearCache clears the discovery cache.
func (d *DynamicToolDiscovery) ClearCache() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cache = make(map[string]shuttle.Tool)
	d.logger.Debug("Cleared dynamic tool discovery cache")
}

// CacheSize returns the current cache size.
func (d *DynamicToolDiscovery) CacheSize() int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return len(d.cache)
}

// EnableDynamicDiscovery enables dynamic tool discovery on the agent.
//
// When enabled, if a tool is not found in the registered tools,
// the agent will attempt to discover it from MCP servers at runtime.
//
// Example:
//
//	agent := NewAgent(config)
//	agent.EnableDynamicDiscovery(mcpMgr)
//
//	// Don't register tools upfront
//	// Tools discovered on-demand during conversations
func (a *Agent) EnableDynamicDiscovery(mcpMgr *manager.Manager) {
	// Get logger (use agent's tracer's logger or create new one)
	logger := zap.L()
	if logger == nil {
		logger = zap.NewNop()
	}

	// Initialize dynamic discovery with MCP manager
	a.dynamicDiscovery = NewDynamicToolDiscovery(mcpMgr, logger)

	// CRITICAL: Inject storage backends for progressive disclosure
	// This ensures dynamically discovered tools can detect and store SQL results properly
	if a.sqlResultStore != nil {
		a.dynamicDiscovery.SetSQLResultStore(a.sqlResultStore)
	}
	if a.sharedMemory != nil {
		a.dynamicDiscovery.SetSharedMemory(a.sharedMemory)
	}

	// Log enablement
	if a.config != nil && a.config.Name != "" {
		logger.Info("Dynamic tool discovery enabled",
			zap.String("agent", a.config.Name),
			zap.Int("server_count", len(mcpMgr.ServerNames())))
	}
}
