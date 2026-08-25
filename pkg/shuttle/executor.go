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
package shuttle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/session"
	"github.com/teradata-labs/loom/pkg/storage"
	"go.uber.org/zap"
)

// ToolRegistry is an interface for dynamic tool discovery.
// This avoids import cycles with pkg/tools/registry.
type ToolRegistry interface {
	Search(ctx context.Context, req *loomv1.SearchToolsRequest) (*loomv1.SearchToolsResponse, error)
}

// ToolEvictor is an optional extension of ToolRegistry: registries that
// implement it get stale entries removed when dynamic registration proves
// them dead (their MCP server no longer exists), so the same stale tool is
// never served twice (issue #334).
type ToolEvictor interface {
	EvictTool(ctx context.Context, toolID string) error
}

// ErrMCPServerNotFound reports that an MCP tool's server does not exist —
// not configured at all, as opposed to configured but temporarily
// unreachable. MCPManager implementations wrap this sentinel so the executor
// can evict the tool's index entry instead of retrying it forever.
var ErrMCPServerNotFound = errors.New("MCP server not found")

// MCPManager is an interface for getting MCP clients.
// This avoids import cycles with pkg/mcp/manager.
type MCPManager interface {
	GetClient(serverName string) (interface{}, error)
}

// BuiltinToolProvider is an interface for getting builtin tools.
// This avoids import cycles with pkg/shuttle/builtin.
type BuiltinToolProvider interface {
	GetTool(name string) Tool
}

// Executor executes tools with tracking and error handling.
type Executor struct {
	registry            *Registry
	sharedMemory        *storage.SharedMemoryStore
	threshold           int64 // Threshold for using shared memory (bytes)
	permissionChecker   *PermissionChecker
	admissionChain      *Chain                       // Admission hook chain; nil is a pure pass-through
	approvedSet         ApprovedSetAccessor          // Approved-set accessor threaded to hooks as req.State; nil until an approved-set store is wired
	identityResolver    func(context.Context) string // Resolves AdmissionRequest.UserID from ctx; nil yields ""
	toolRegistry        ToolRegistry                 // Tool registry for dynamic tool discovery
	mcpManager          MCPManager                   // MCP manager for dynamic MCP tool registration
	builtinToolProvider BuiltinToolProvider          // Builtin tool provider for dynamic builtin tool registration
	logger              *zap.Logger                  // Structured logger; never nil (defaults to no-op)

	// Metrics for large parameter optimization
	largeParamStores      atomic.Int64 // Count of parameters stored
	largeParamDerefs      atomic.Int64 // Count of parameters dereferenced
	largeParamBytesStored atomic.Int64 // Total bytes stored
	largeParamDerefErrors atomic.Int64 // Count of dereference failures

}

// NewExecutor creates a new tool executor.
func NewExecutor(registry *Registry) *Executor {
	return &Executor{
		registry:  registry,
		threshold: storage.DefaultSharedMemoryThreshold,
		logger:    zap.NewNop(),
	}
}

// SetLogger configures structured logging for executor housekeeping (e.g.
// stale-index evictions that fail). A nil logger keeps the no-op default.
func (e *Executor) SetLogger(logger *zap.Logger) {
	if logger != nil {
		e.logger = logger
	}
}

// SetSharedMemory configures shared memory for large result handling.
func (e *Executor) SetSharedMemory(sharedMemory *storage.SharedMemoryStore, threshold int64) {
	e.sharedMemory = sharedMemory
	if threshold >= 0 {
		e.threshold = threshold
	}
}

// SharedMemoryThreshold reports the byte threshold at or above which a result is
// stored by reference. Exposed so callers can assert both offload sites agree.
func (e *Executor) SharedMemoryThreshold() int64 {
	return e.threshold
}

// SetPermissionChecker configures permission checking for tool execution.
func (e *Executor) SetPermissionChecker(checker *PermissionChecker) {
	e.permissionChecker = checker
}

// SetAdmissionChain configures the admission hook chain consulted before every
// tool body runs. A nil chain leaves execution as a pure pass-through.
func (e *Executor) SetAdmissionChain(chain *Chain) {
	e.admissionChain = chain
}

// SetApprovedSet configures the approved-set accessor threaded to admission
// hooks as AdmissionRequest.State. A nil accessor leaves a gated-allowlist with
// no store to read, so it fails closed.
func (e *Executor) SetApprovedSet(accessor ApprovedSetAccessor) {
	e.approvedSet = accessor
}

// ApprovedSet returns the wired approved-set accessor (nil when none). The set
// is per-accessor state, so introspection must go through the same instance the
// executor threads to hooks.
func (e *Executor) ApprovedSet() ApprovedSetAccessor {
	return e.approvedSet
}

// SetIdentityResolver configures how AdmissionRequest.UserID is read from the
// call context. pkg/shuttle cannot import the storage layer that owns the
// user-id context key without an import cycle, so the composition layer injects
// the resolver here. A nil resolver yields an empty UserID.
func (e *Executor) SetIdentityResolver(resolver func(context.Context) string) {
	e.identityResolver = resolver
}

// SetToolRegistry configures the tool registry for dynamic tool discovery.
// When a tool is not found in the local registry, the executor will check
// the tool registry and dynamically register MCP tools if found.
func (e *Executor) SetToolRegistry(registry ToolRegistry) {
	e.toolRegistry = registry
}

// SetMCPManager configures the MCP manager for dynamic MCP tool registration.
func (e *Executor) SetMCPManager(manager MCPManager) {
	e.mcpManager = manager
}

// SetBuiltinToolProvider configures the builtin tool provider for dynamic builtin tool registration.
func (e *Executor) SetBuiltinToolProvider(provider BuiltinToolProvider) {
	e.builtinToolProvider = provider
}

// CanonicalToolName returns the registered tool's own name for a lookup key.
// Registry aliases point at the same Tool instance, so this keeps permissions,
// admission, and circuit breakers keyed consistently with direct calls.
// When the name is not found by direct lookup, a local suffix scan is tried
// (same logic as tryDynamicRegistration) so callers get the server-qualified
// name even for suffix or sanitized-name inputs.
func (e *Executor) CanonicalToolName(name string) string {
	if tool, ok := e.registry.Get(name); ok {
		return tool.Name()
	}
	suffix := ":" + name
	var matches []Tool
	for _, t := range e.registry.ListTools() {
		n := t.Name()
		if strings.HasSuffix(n, suffix) || strings.ReplaceAll(n, ":", "_") == name {
			matches = append(matches, t)
		}
	}
	if len(matches) == 1 {
		return matches[0].Name()
	}
	return name
}

// admit runs the admission gates for a tool call. It returns the request handed
// to the hooks, the admission result, and — when the decision is Deny — a ready
// permission_denied Result to return in place of running the tool. The
// name-level PermissionChecker enforces UNCONDITIONALLY when set — with or
// without a chain attached — so SetPermissionChecker is never silently inert:
// a host that sets both gets the checker first, then the chain. With neither
// configured the call is a pure pass-through.
func (e *Executor) admit(ctx context.Context, toolName string, params map[string]interface{}) (AdmissionRequest, AdmissionResult, *Result) {
	if e.permissionChecker != nil {
		if err := e.permissionChecker.CheckPermission(ctx, toolName, params); err != nil {
			denied := &Result{
				Success: false,
				Error:   &Error{Code: "permission_denied", Message: err.Error(), Retryable: false},
			}
			return AdmissionRequest{}, AdmissionResult{Decision: Decision{Kind: Deny, Reason: err.Error()}}, denied
		}
	}
	if e.admissionChain == nil {
		return AdmissionRequest{}, AdmissionResult{Decision: Decision{Kind: NoDecision}}, nil
	}

	userID := ""
	if e.identityResolver != nil {
		userID = e.identityResolver(ctx)
	}

	req := AdmissionRequest{
		Ctx:       ctx,
		ToolName:  toolName,
		Params:    params,
		UserID:    userID,
		SessionID: session.SessionIDFromContext(ctx),
		State:     e.approvedSet,
	}

	res := e.admissionChain.Admit(req)
	if res.Decision.Kind == Deny {
		denied := &Result{
			Success: false,
			Error:   &Error{Code: "permission_denied", Message: res.Decision.Reason, Retryable: false},
		}
		return req, res, denied
	}

	return req, res, nil
}

// stampAdmissionDecision makes "admission.decision" a RESERVED metadata key
// sourced solely from the chain: a matched AuditHook's decision is written, and
// with no audit decision any value a tool body put under the key is deleted, so
// the persisted trail can never carry a tool-forged verdict. Both executor entry
// points stamp exactly once, via a deferred call over the named return, so every
// exit — success, deny, and each error path — carries the correct value.
func stampAdmissionDecision(result *Result, auditDecision string) {
	if result == nil {
		return
	}
	if auditDecision == "" {
		delete(result.Metadata, "admission.decision")
		return
	}
	if result.Metadata == nil {
		result.Metadata = make(map[string]interface{})
	}
	result.Metadata["admission.decision"] = auditDecision
}

// Execute executes a tool by name with the given parameters.
func (e *Executor) Execute(ctx context.Context, toolName string, params map[string]interface{}) (result *Result, err error) {
	tool, ok := e.registry.Get(toolName)
	if !ok {
		// Tool not found locally, try dynamic registration
		dynamicTool, err := e.tryDynamicRegistration(ctx, toolName)
		if err != nil {
			return nil, fmt.Errorf("tool not found: %s (dynamic registration failed: %w)", toolName, err)
		}
		if dynamicTool == nil {
			return nil, fmt.Errorf("tool not found: %s", toolName)
		}
		tool = dynamicTool
	}

	// Normalize parameters BEFORE admission so the chain and the tool body see
	// one map: the matcher must judge the exact params the tool would receive,
	// or the caller's key spelling would decide whether a binding matches.
	normalizedParams := normalizeParametersToSchema(tool, params)

	// Stamp the audit verdict at every exit — success, deny, and each error
	// return — through the one deferred call (the key is reserved: with no
	// audit decision a tool-written value is removed).
	var adm AdmissionResult
	defer func() { stampAdmissionDecision(result, adm.PersistedDecision()) }()

	// Admit before execution. The tool is already acquired (locally or via
	// dynamic registration) so an externally-resolved tool is governed at the
	// same seam as a local one. A Deny returns the permission_denied Result
	// without running the tool body.
	//
	// Check both the requested name (alias/unqualified) and the canonical name
	// so that deny rules written against either form are effective. Without this,
	// a deny rule on the unqualified name would be inert once the tool is
	// resolved to its server-qualified canonical name.
	canonicalName := tool.Name()
	req, admRes, denied := e.admit(ctx, canonicalName, normalizedParams)
	adm = admRes
	if denied != nil {
		return denied, nil
	}
	if toolName != canonicalName {
		_, admRes2, denied2 := e.admit(ctx, toolName, normalizedParams)
		if denied2 != nil {
			adm = admRes2
			return denied2, nil
		}
	}

	// Handle large parameters: store in shared memory to prevent context bloat
	referencedParams, err := e.handleLargeParameters(ctx, normalizedParams)
	if err != nil {
		return &Result{
			Success: false,
			Error: &Error{
				Code:      "LARGE_PARAM_ERROR",
				Message:   fmt.Sprintf("Failed to handle large parameters: %v", err),
				Retryable: false,
			},
		}, nil
	}

	// Dereference parameters before tool execution (transparent to tools)
	finalParams, err := e.dereferenceLargeParameters(ctx, referencedParams)
	if err != nil {
		return &Result{
			Success: false,
			Error: &Error{
				Code:      "DEREF_ERROR",
				Message:   fmt.Sprintf("Failed to dereference parameters: %v", err),
				Retryable: false,
			},
		}, nil
	}

	start := time.Now()
	result, err = tool.Execute(ctx, finalParams)
	duration := time.Since(start)

	if err != nil {
		return &Result{
			Success:         false,
			Error:           &Error{Code: "execution_failed", Message: err.Error(), Retryable: false},
			ExecutionTimeMs: duration.Milliseconds(),
		}, nil
	}

	if result != nil {
		// Always set execution time, even if tool already set it
		// (executor timing is authoritative)
		result.ExecutionTimeMs = duration.Milliseconds()
		// Arrival appends (HLD §1): the result passes through WHOLE. Large-result
		// offload is a pure render condition of ContextCompilation (§5.2).
	} else {
		// Tool returned nil result, create one
		result = &Result{
			Success:         true,
			ExecutionTimeMs: duration.Milliseconds(),
		}
	}

	e.admissionChain.Observe(req, result)

	return result, nil
}

// ExecuteWithTool executes a specific tool instance (not from registry).
func (e *Executor) ExecuteWithTool(ctx context.Context, tool Tool, params map[string]interface{}) (result *Result, err error) {
	// Normalize parameters BEFORE admission so the chain and the tool body see
	// one map (same invariant as Execute).
	normalizedParams := normalizeParametersToSchema(tool, params)

	// Stamp the audit verdict at every exit through the one deferred call.
	var adm AdmissionResult
	defer func() { stampAdmissionDecision(result, adm.PersistedDecision()) }()

	// Admit before execution. A Deny returns the permission_denied Result
	// without running the tool body.
	req, admRes, denied := e.admit(ctx, tool.Name(), normalizedParams)
	adm = admRes
	if denied != nil {
		return denied, nil
	}

	// Handle large parameters: store in shared memory to prevent context bloat
	referencedParams, err := e.handleLargeParameters(ctx, normalizedParams)
	if err != nil {
		return &Result{
			Success: false,
			Error: &Error{
				Code:      "LARGE_PARAM_ERROR",
				Message:   fmt.Sprintf("Failed to handle large parameters: %v", err),
				Retryable: false,
			},
		}, nil
	}

	// Dereference parameters before tool execution (transparent to tools)
	finalParams, err := e.dereferenceLargeParameters(ctx, referencedParams)
	if err != nil {
		return &Result{
			Success: false,
			Error: &Error{
				Code:      "DEREF_ERROR",
				Message:   fmt.Sprintf("Failed to dereference parameters: %v", err),
				Retryable: false,
			},
		}, nil
	}

	start := time.Now()
	result, err = tool.Execute(ctx, finalParams)
	duration := time.Since(start)

	if err != nil {
		return &Result{
			Success:         false,
			Error:           &Error{Code: "execution_failed", Message: err.Error(), Retryable: false},
			ExecutionTimeMs: duration.Milliseconds(),
		}, nil
	}

	if result != nil {
		// Always set execution time, even if tool already set it
		// (executor timing is authoritative)
		result.ExecutionTimeMs = duration.Milliseconds()
		// Arrival appends (HLD §1): the result passes through WHOLE.
	} else {
		// Tool returned nil result, create one
		result = &Result{
			Success:         true,
			ExecutionTimeMs: duration.Milliseconds(),
		}
	}

	e.admissionChain.Observe(req, result)

	return result, nil
}

// estimateValueSize calculates approximate byte size of a parameter value.
// Used to determine if a parameter should be stored in shared memory.
func estimateValueSize(value interface{}) int64 {
	switch v := value.(type) {
	case string:
		return int64(len(v))
	case []byte:
		return int64(len(v))
	case map[string]interface{}, []interface{}:
		// Serialize to estimate size
		data, err := json.Marshal(v)
		if err != nil {
			return 0
		}
		return int64(len(data))
	default:
		// Small primitive types (int, bool, float), don't store
		return 0
	}
}

// handleLargeParameters checks if any parameter values exceed threshold
// and stores them in shared memory, replacing with DataReference objects.
// This prevents massive parameters from bloating context windows. Stored
// parameters are owned by the caller's session partition (from ctx) so
// dereferenceLargeParameters resolves them for the same caller.
func (e *Executor) handleLargeParameters(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error) {
	if e.sharedMemory == nil {
		return params, nil // No storage configured, skip optimization
	}

	sessionID := session.SessionIDFromContext(ctx)

	result := make(map[string]interface{})
	modified := false

	for key, value := range params {
		size := estimateValueSize(value)

		if size > e.threshold {
			// Store in shared memory
			data, err := json.Marshal(value)
			if err != nil {
				return nil, fmt.Errorf("failed to serialize large parameter %s: %w", key, err)
			}

			id := storage.GenerateID()
			ref, err := e.sharedMemory.Store(id, data, "application/json", map[string]string{
				"parameter_name": key,
				"original_size":  fmt.Sprintf("%d", size),
				"source":         "parameter_optimization",
			}, sessionID)
			if err != nil {
				return nil, fmt.Errorf("failed to store large parameter %s: %w", key, err)
			}

			// Replace value with DataReference (type-safe, zero collision risk)
			result[key] = ref
			modified = true

			// Metrics: track stored parameter
			e.largeParamStores.Add(1)
			e.largeParamBytesStored.Add(size)
		} else {
			result[key] = value
		}
	}

	if !modified {
		return params, nil // No large params, return original
	}

	return result, nil
}

// dereferenceLargeParameters replaces DataReference objects with actual data.
// This happens transparently before tool execution - tools never see references.
// Resolution is scoped to the caller's session partition (from ctx).
func (e *Executor) dereferenceLargeParameters(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error) {
	if e.sharedMemory == nil {
		return params, nil
	}

	sessionID := session.SessionIDFromContext(ctx)

	result := make(map[string]interface{})
	hasRefs := false

	for key, value := range params {
		// Check if value is a DataReference
		if ref, ok := value.(*loomv1.DataReference); ok {
			hasRefs = true

			// Retrieve from shared memory
			data, err := e.sharedMemory.Get(ref, sessionID)
			if err != nil {
				e.largeParamDerefErrors.Add(1) // Metrics: track error
				return nil, fmt.Errorf("failed to dereference parameter %s: %w", key, err)
			}

			// Deserialize back to original type
			var originalValue interface{}
			if err := json.Unmarshal(data, &originalValue); err != nil {
				e.largeParamDerefErrors.Add(1) // Metrics: track error
				return nil, fmt.Errorf("failed to deserialize parameter %s: %w", key, err)
			}

			result[key] = originalValue
			e.largeParamDerefs.Add(1) // Metrics: track successful dereference
		} else {
			result[key] = value
		}
	}

	if !hasRefs {
		return params, nil // No references, return original
	}

	return result, nil
}

// ListAvailableTools returns all tools available in the executor's registry.
func (e *Executor) ListAvailableTools() []Tool {
	return e.registry.ListTools()
}

// ListToolsByBackend returns all tools for a specific backend.
func (e *Executor) ListToolsByBackend(backend string) []Tool {
	return e.registry.ListByBackend(backend)
}

// ExecutorStats holds metrics about executor operations.
type ExecutorStats struct {
	LargeParamStores      int64 // Count of parameters stored in shared memory
	LargeParamDerefs      int64 // Count of parameters dereferenced
	LargeParamBytesStored int64 // Total bytes stored for parameters
	LargeParamDerefErrors int64 // Count of dereference failures
}

// Stats returns metrics about executor operations.
// Includes large parameter optimization statistics.
func (e *Executor) Stats() ExecutorStats {
	return ExecutorStats{
		LargeParamStores:      e.largeParamStores.Load(),
		LargeParamDerefs:      e.largeParamDerefs.Load(),
		LargeParamBytesStored: e.largeParamBytesStored.Load(),
		LargeParamDerefErrors: e.largeParamDerefErrors.Load(),
	}
}

// normalizeParametersToSchema attempts to normalize parameter names to match the tool's schema.
// This handles the common issue where LLMs use snake_case but tools expect camelCase (or vice versa).
func normalizeParametersToSchema(tool Tool, params map[string]interface{}) map[string]interface{} {
	if len(params) == 0 {
		return params
	}

	schema := tool.InputSchema()
	if schema == nil || schema.Properties == nil {
		return params // No schema to normalize against
	}

	// Build a mapping of lowercase parameter names to actual schema names
	schemaKeys := make(map[string]string)
	for key := range schema.Properties {
		schemaKeys[toLowerUnderscore(key)] = key
	}

	// Normalize incoming parameters
	normalized := make(map[string]interface{}, len(params))
	for key, value := range params {
		// Try to find matching schema key (case-insensitive with underscore normalization)
		normalizedKey := toLowerUnderscore(key)
		if schemaKey, exists := schemaKeys[normalizedKey]; exists {
			// Use the schema's preferred key name
			normalized[schemaKey] = value
		} else {
			// No match found, use original key
			normalized[key] = value
		}
	}

	return normalized
}

// toLowerUnderscore converts any naming convention to lowercase with underscores.
// This allows matching camelCase, snake_case, PascalCase, etc.
func toLowerUnderscore(s string) string {
	if s == "" {
		return ""
	}

	var result []rune
	for i, r := range s {
		// Convert to lowercase
		lower := unicode.ToLower(r)

		// Add underscore before uppercase letters (except first char)
		if i > 0 && unicode.IsUpper(r) {
			result = append(result, '_')
		}

		result = append(result, lower)
	}

	return string(result)
}

// tryDynamicRegistration attempts to dynamically register a tool from the tool registry.
// This enables agents to use tools they discover via tool_search without explicit registration.
// Returns the registered tool, or nil if registration fails or tool not found.
func (e *Executor) tryDynamicRegistration(ctx context.Context, toolName string) (Tool, error) {
	// Fast path: the tool may already be registered locally under its
	// server-qualified name (e.g., "teradata-aiop-mcp-server:base_readQuery")
	// while the LLM called it using either:
	//   - the plain unprefixed name ("base_readQuery"), because the ROM or
	//     tool_search result returned the unprefixed form, or
	//   - the LLM-sanitized qualified name ("teradata-aiop-mcp-server_base_readQuery"),
	//     since some providers reject ':' in tool names (see llm.SanitizeToolName)
	//     and the caller re-derived the sanitized form from an earlier turn
	//     instead of using the provider's reverse-mapped original name.
	// Scan the local registry first before hitting the external tool registry.
	suffix := ":" + toolName
	var matches []Tool
	for _, t := range e.registry.ListTools() {
		name := t.Name()
		if strings.HasSuffix(name, suffix) || strings.ReplaceAll(name, ":", "_") == toolName {
			matches = append(matches, t)
		}
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].Name() < matches[j].Name() })
	if len(matches) == 1 {
		// Register an alias so subsequent calls skip this scan.
		e.registry.RegisterAlias(toolName, matches[0])
		return matches[0], nil
	}
	if len(matches) > 1 {
		names := make([]string, len(matches))
		for i, match := range matches {
			names[i] = match.Name()
		}
		return nil, fmt.Errorf("ambiguous tool name %q matches %s", toolName, strings.Join(names, ", "))
	}

	// Check if tool registry is configured
	if e.toolRegistry == nil {
		return nil, fmt.Errorf("tool registry not configured")
	}

	// Search for the tool in the registry
	// Use the exact tool name as the query for a precise match
	resp, err := e.toolRegistry.Search(ctx, &loomv1.SearchToolsRequest{
		Query:         toolName,
		Mode:          loomv1.SearchMode_SEARCH_MODE_FAST, // Use fast mode for keyword match
		MaxResults:    5,
		IncludeSchema: true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to search tool registry: %w", err)
	}

	// The search is fuzzy (tokenized full-text match), but the caller asked
	// for one specific tool by name: only an exact name match may be
	// registered and executed. Without this guard, a request for a tool whose
	// index entry is gone would silently execute whichever different tool
	// happens to share tokens with the dead tool's name.
	var toolInfo *loomv1.IndexedTool
	for _, result := range resp.Results {
		if result.Tool != nil && result.Tool.Name == toolName {
			toolInfo = result.Tool
			break
		}
	}
	if toolInfo == nil {
		return nil, fmt.Errorf("tool not found in registry: no indexed tool is named %q", toolName)
	}

	// Handle based on tool source
	switch toolInfo.Source {
	case loomv1.ToolSource_TOOL_SOURCE_MCP:
		return e.registerMCPTool(ctx, toolInfo)
	case loomv1.ToolSource_TOOL_SOURCE_BUILTIN:
		return e.registerBuiltinTool(ctx, toolInfo)
	case loomv1.ToolSource_TOOL_SOURCE_CUSTOM:
		return nil, fmt.Errorf("custom tools not yet supported for dynamic registration")
	default:
		return nil, fmt.Errorf("unknown tool source: %v", toolInfo.Source)
	}
}

// registerMCPTool dynamically registers an MCP tool from the tool registry.
func (e *Executor) registerMCPTool(ctx context.Context, toolInfo *loomv1.IndexedTool) (Tool, error) {
	if e.mcpManager == nil {
		return nil, fmt.Errorf("MCP manager not configured")
	}

	if toolInfo.McpServer == "" {
		return nil, fmt.Errorf("MCP tool missing server name")
	}

	// Get MCP client for the server
	client, err := e.mcpManager.GetClient(toolInfo.McpServer)
	if err != nil {
		// A server that does not exist (vs. temporarily unreachable) means
		// the index entry is stale: evict it so it is never served again
		// (issue #334). Transient failures keep the entry.
		if errors.Is(err, ErrMCPServerNotFound) {
			if evictor, ok := e.toolRegistry.(ToolEvictor); ok {
				evictErr := evictor.EvictTool(ctx, toolInfo.Id)
				if evictErr == nil {
					return nil, fmt.Errorf("failed to get MCP client for server %s: %w (stale tool index entry %s evicted)", toolInfo.McpServer, err, toolInfo.Id)
				}
				// A failed eviction means the dead entry will be served
				// again; that must be visible, not swallowed.
				e.logger.Warn("Failed to evict stale tool index entry",
					zap.String("tool_id", toolInfo.Id),
					zap.String("tool_name", toolInfo.Name),
					zap.String("mcp_server", toolInfo.McpServer),
					zap.Error(evictErr))
			}
		}
		return nil, fmt.Errorf("failed to get MCP client for server %s: %w", toolInfo.McpServer, err)
	}

	// Parse input schema from JSON string
	var inputSchema *JSONSchema
	if toolInfo.InputSchema != "" {
		if err := json.Unmarshal([]byte(toolInfo.InputSchema), &inputSchema); err != nil {
			// If schema parsing fails, create a basic schema
			inputSchema = &JSONSchema{
				Type:       "object",
				Properties: map[string]*JSONSchema{},
			}
		}
	}

	// Create MCP tool wrapper
	mcpTool := &mcpToolWrapper{
		name:        toolInfo.Name,
		description: toolInfo.Description,
		inputSchema: inputSchema,
		client:      client,
		serverName:  toolInfo.McpServer,
	}

	// Register the tool so subsequent calls don't require dynamic lookup
	e.registry.Register(mcpTool)

	return mcpTool, nil
}

// registerBuiltinTool dynamically registers a builtin tool from the builtin tool provider.
func (e *Executor) registerBuiltinTool(ctx context.Context, toolInfo *loomv1.IndexedTool) (Tool, error) {
	if e.builtinToolProvider == nil {
		return nil, fmt.Errorf("builtin tool provider not configured")
	}

	// Get tool from builtin provider
	tool := e.builtinToolProvider.GetTool(toolInfo.Name)
	if tool == nil {
		return nil, fmt.Errorf("builtin tool not found: %s", toolInfo.Name)
	}

	// Register the tool so subsequent calls don't require dynamic lookup
	e.registry.Register(tool)

	return tool, nil
}

// mcpToolWrapper wraps an MCP client to implement the Tool interface.
type mcpToolWrapper struct {
	name        string
	description string
	inputSchema *JSONSchema
	client      interface{} // MCP client interface
	serverName  string
}

func (t *mcpToolWrapper) Name() string {
	return t.name
}

func (t *mcpToolWrapper) Description() string {
	return t.description
}

func (t *mcpToolWrapper) InputSchema() *JSONSchema {
	return t.inputSchema
}

func (t *mcpToolWrapper) Execute(ctx context.Context, params map[string]interface{}) (*Result, error) {
	// Use the MCP client's CallTool method
	// We use reflection-safe interface matching to avoid import cycles with mcp/protocol package

	// Define interface that matches actual MCP client CallTool signature
	// The actual return type is *protocol.CallToolResult, but we use interface{} to avoid import cycles
	type mcpClient interface {
		CallTool(ctx context.Context, name string, arguments map[string]interface{}) (interface{}, error)
	}

	client, ok := t.client.(mcpClient)
	if !ok {
		// Debug: Log the actual type we received
		actualType := fmt.Sprintf("%T", t.client)
		return &Result{
			Success: false,
			Error: &Error{
				Code:    "MCP_CLIENT_ERROR",
				Message: fmt.Sprintf("MCP client does not support CallTool method (actual type: %s, server: %s)", actualType, t.serverName),
			},
		}, nil
	}

	result, err := client.CallTool(ctx, t.name, params)
	if err != nil {
		return &Result{
			Success: false,
			Error: &Error{
				Code:      "MCP_EXECUTION_FAILED",
				Message:   fmt.Sprintf("MCP tool execution failed: %v", err),
				Retryable: true,
			},
		}, nil
	}

	// Extract and parse MCP Content if present
	// This handles CallToolResult structures with Content arrays
	cleanData := extractMCPContentData(result)

	return &Result{
		Success: true,
		Data:    cleanData,
	}, nil
}

func (t *mcpToolWrapper) Backend() string {
	return "" // MCP tools don't have a specific backend
}

// extractMCPContentData extracts clean data from MCP CallToolResult structures.
// This handles the Content array format and attempts to parse SQL results directly.
func extractMCPContentData(result interface{}) interface{} {
	if result == nil {
		return nil
	}

	// Try to extract Content field using reflection-safe type assertions
	// MCP CallToolResult has structure like: {Content: [{Type: "text", Text: "..."}], IsError: false}
	resultMap, ok := result.(map[string]interface{})
	if !ok {
		// Not a map, return as-is (might already be clean data)
		return result
	}

	// Check for Content field
	contentRaw, hasContent := resultMap["content"]
	if !hasContent {
		// No Content field, might be clean data already
		return result
	}

	// Content should be an array
	contentArray, ok := contentRaw.([]interface{})
	if !ok || len(contentArray) == 0 {
		return result
	}

	// Handle single text content (most common case)
	if len(contentArray) == 1 {
		contentItem, ok := contentArray[0].(map[string]interface{})
		if !ok {
			return result
		}

		contentType, _ := contentItem["type"].(string)
		if contentType == "text" {
			text, _ := contentItem["text"].(string)

			// Try to parse as JSON - many MCP tools return JSON in text field
			text = strings.TrimSpace(text)
			// Skip any message prefix like "✓ Success\n\n{...}"
			jsonStart := strings.Index(text, "{")
			if jsonStart >= 0 {
				jsonText := text[jsonStart:]
				var parsed map[string]interface{}
				if err := json.Unmarshal([]byte(jsonText), &parsed); err == nil {
					// Successfully parsed JSON - check if it's SQL result
					if hasColumns, hasRows := parsed["columns"], parsed["rows"]; hasColumns != nil && hasRows != nil {
						// This is a SQL result! Return the clean parsed structure
						return parsed
					}
					if hasColumns, hasRows := parsed["Columns"], parsed["Rows"]; hasColumns != nil && hasRows != nil {
						// Capitalized version
						return parsed
					}
					// Not SQL, but valid JSON - return parsed
					return parsed
				}
			}

			// Not JSON or parsing failed, return text as-is
			return text
		}
	}

	// Multiple content items or non-text - return structured Content array
	results := make([]map[string]interface{}, len(contentArray))
	for i, c := range contentArray {
		if contentItem, ok := c.(map[string]interface{}); ok {
			results[i] = contentItem
		}
	}
	return results
}
