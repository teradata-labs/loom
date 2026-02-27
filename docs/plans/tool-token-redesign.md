# Tool System Redesign: Eliminating Token Bloat

**Status**: Planning
**Branch strategy**: One branch per phase, land in order
**Goal**: Reduce tool schema tokens from ~12,000/request to near-zero effective cost

## Completion Criteria (applies to every phase)

A phase is **done** when ALL of the following pass:

1. `just check` — full CI gate: proto-lint + proto-format-check + proto-gen-check + fmt-check + vet + lint + test + build + security
2. `go test -tags fts5 -race ./...` — zero race conditions
3. Unit tests for all changed packages at >80% coverage of new code
4. Integration tests (`just test-integration`) passing for affected LLM providers
5. E2E tests (`just test-e2e-storage`) passing against a running `looms` server
6. Token savings verified via observability: check `~/.loom/observability.db` after a real multi-turn session and confirm expected metrics are populated

## Context

Every LLM API call sends full JSON schemas for ALL registered tools. With 16 tools this burns
~12,000 input tokens/request against a 30,000/minute Anthropic rate limit (≈2.5 calls/minute max).

**How Claude Code solves this**: Prompt caching with `cache_control: ephemeral`. Cached input
tokens (`cache_read_input_tokens`) do NOT count against the ITPM rate limit. After the first call
in a session, tool schemas cost nothing against the rate limit. This is Phase 1.

---

## Phase 1: Prompt Caching `feat/prompt-caching`

**Impact**: 5–10x throughput for multi-turn sessions. Zero behavioral change.
**Effort**: 2–3 days
**Risk**: Low — purely additive, falls back gracefully if cache misses

### Why it works

Anthropic's caching rules:
- `cache_creation_input_tokens`: costs 1.25x, counts against ITPM
- `cache_read_input_tokens`: costs 0.10x, **does NOT count against ITPM**
- Cache TTL: 5 minutes (ephemeral) — perfect for agentic conversations
- Trigger: add `anthropic-beta: prompt-caching-2024-07-31` header + `cache_control` markers

### 1.1 `pkg/llm/anthropic/types.go`

Add new types and update existing structs:

```go
type CacheControl struct {
    Type string `json:"type"` // "ephemeral"
}

// Replaces plain string System field
type TextBlockParam struct {
    Type         string        `json:"type"` // "text"
    Text         string        `json:"text"`
    CacheControl *CacheControl `json:"cache_control,omitempty"`
}

// Replaces Tool — adds CacheControl
type CacheableTool struct {
    Name         string        `json:"name"`
    Description  string        `json:"description"`
    InputSchema  InputSchema   `json:"input_schema"`
    CacheControl *CacheControl `json:"cache_control,omitempty"`
}

// Extend Usage with cache breakdown
type Usage struct {
    InputTokens              int `json:"input_tokens"`
    OutputTokens             int `json:"output_tokens"`
    CacheReadInputTokens     int `json:"cache_read_input_tokens"`
    CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

// MessagesRequest changes:
// System: string  →  System: []TextBlockParam
// Tools:  []Tool  →  Tools:  []CacheableTool
```

### 1.2 `pkg/llm/anthropic/client.go`

**`callAPI()` and `ChatStream()`** — add beta header:
```go
httpReq.Header.Set("anthropic-beta", "prompt-caching-2024-07-31")
```

**`convertMessages()`** — system prompt becomes cacheable block:
```go
// Before: returns string
// After: returns []TextBlockParam with cache_control on the combined system text
systemBlocks := []TextBlockParam{
    {Type: "text", Text: combinedText, CacheControl: &CacheControl{Type: "ephemeral"}},
}
```

**`convertTools()`** — mark the last tool as the cache boundary:
```go
func (c *Client) convertTools(tools []shuttle.Tool) []CacheableTool {
    // ... convert as before ...
    if len(apiTools) > 0 {
        apiTools[len(apiTools)-1].CacheControl = &CacheControl{Type: "ephemeral"}
    }
    return apiTools
}
```

**`calculateCost()`** — updated pricing:
```go
inputCost      = inputTokens * 3.0 / 1_000_000
outputCost     = outputTokens * 15.0 / 1_000_000
cacheWriteCost = cacheCreationTokens * 3.75 / 1_000_000  // 1.25x
cacheReadCost  = cacheReadTokens * 0.30 / 1_000_000      // 0.10x
```

### 1.3 `pkg/llm/bedrock/client_sdk.go`

The `anthropic-sdk-go` already has `CacheControl` fields on `TextBlockParam` and `ToolParam`:

```go
// System prompt
params.System = []anthropic.TextBlockParam{{
    Text: systemPrompt,
    CacheControl: anthropic.F(anthropic.CacheControlEphemeralParam{Type: "ephemeral"}),
}}

// Last tool in list
sdkTools[len(sdkTools)-1].CacheControl = anthropic.F(anthropic.CacheControlEphemeralParam{Type: "ephemeral"})
```

Note: `pkg/llm/bedrock/converse.go` (AWS Converse API) does NOT support caching — no changes there.

### 1.4 `pkg/types/types.go` — Usage struct

```go
type Usage struct {
    InputTokens              int
    OutputTokens             int
    TotalTokens              int
    CostUSD                  float64
    CacheReadInputTokens     int  // Free for ITPM purposes
    CacheCreationInputTokens int  // Counted against ITPM at 1.25x
}
```

### 1.5 Observability (`pkg/llm/instrumented_provider.go`)

Add span attributes and metrics:
```go
span.SetAttribute("llm.tokens.cache_read", resp.Usage.CacheReadInputTokens)
span.SetAttribute("llm.tokens.cache_write", resp.Usage.CacheCreationInputTokens)

// Cache hit rate metric
if total := read + write; total > 0 {
    p.tracer.RecordMetric("llm.cache.hit_rate", float64(read)/float64(total), labels)
}
```

### 1.6 Gemini (`pkg/llm/gemini/types.go` + `client.go`)

Gemini implicit caching is **already active** since May 2025 — no opt-in needed. Tool schemas are
cached automatically once they exceed 1,024 tokens. Just add observability:

```go
// types.go — add CachedContentTokenCount to UsageMetadata
type UsageMetadata struct {
    PromptTokenCount         int `json:"promptTokenCount"`
    CandidatesTokenCount     int `json:"candidatesTokenCount"`
    TotalTokenCount          int `json:"totalTokenCount"`
    CachedContentTokenCount  int `json:"cachedContentTokenCount"` // add
}
```

Map through to `Usage.CacheReadInputTokens` in `convertResponse()`. No beta header needed.

Note: Gemini cached tokens **still count against rate limits** (unlike Anthropic) but cost 75–90% less.

### 1.7 Tests

**Unit (`pkg/llm/anthropic/`)**
- `TestClient_convertTools_CacheControl`: serialize 3+ tools, verify last tool has `"cache_control":{"type":"ephemeral"}`, others don't
- `TestClient_convertMessages_SystemCacheControl`: system prompt serializes as `[]TextBlockParam` with `cache_control` on the block
- `TestClient_calculateCost_CacheTokens`: cache write = 1.25x rate, cache read = 0.10x rate; verify against known token counts
- `TestClient_convertResponse_CacheFields`: mock API response with `cache_read_input_tokens:500`, verify `Usage.CacheReadInputTokens == 500`
- `TestClient_callAPI_BetaHeader`: `httptest.Server` captures raw request, assert `anthropic-beta` header present
- Race: `go test -tags fts5 -race ./pkg/llm/anthropic/...`

**Unit (`pkg/llm/bedrock/`)**
- `TestSDKClient_convertTools_CacheControl`: last tool has `CacheControl` set
- `TestSDKClient_convertResponse_CacheFields`: cache token fields flow through to `Usage`

**Unit (`pkg/llm/gemini/`)**
- `TestClient_convertResponse_CachedTokens`: `cachedContentTokenCount` from API maps to `Usage.CacheReadInputTokens`

**E2E (`test/e2e/prompt_caching_e2e_test.go`)** — `//go:build integration`

```go
// TestE2E_PromptCaching_CacheHitOnSecondTurn verifies that turn 2 in a session
// reports cache_read_input_tokens > 0, proving tool schemas were cached.
// Requires ANTHROPIC_API_KEY env var; skips otherwise.
func TestE2E_PromptCaching_CacheHitOnSecondTurn(t *testing.T)

// TestE2E_PromptCaching_CostReduced verifies that total_cost_usd on turn 2
// is less than turn 1 for the same query length (cache read is 0.10x cost).
func TestE2E_PromptCaching_CostReduced(t *testing.T)

// TestE2E_PromptCaching_RateLimitRelief verifies that 5 rapid sequential Weave
// calls within a session all succeed without 429 errors (previously would have
// hit 30K ITPM limit with 16 tools).
// Only runs when LOOM_E2E_RATE_LIMIT_TEST=1 to avoid slow CI.
func TestE2E_PromptCaching_RateLimitRelief(t *testing.T)
```

These tests check `resp.GetCost().GetLlmCost()` — which means the `LlmCost` proto message needs
`cache_read_input_tokens` and `cache_creation_input_tokens` fields. **Proto change required**:

```protobuf
// In proto/loom/v1/loom.proto, LlmCost message:
int32 cache_read_input_tokens = 6;
int32 cache_creation_input_tokens = 7;
```

Run `buf generate` after adding these fields.

---

## Phase 2: Lazy UI Tools `feat/lazy-ui-tools`

**Impact**: Removes 4 tools (~3,000 tokens) from non-UI conversations
**Effort**: 1 day
**Risk**: Low — follows existing progressive disclosure pattern exactly

### Problem

`create_ui_app`, `update_ui_app`, `delete_ui_app`, `list_component_types` are registered eagerly
for every agent in `cmd/looms/cmd_serve.go`. Most conversations never use UI tools.

### 2.1 New mechanism: `WithLazyTools` agent option

**`pkg/agent/agent.go`** — add `lazyToolSet` and the option:

```go
type lazyToolSet struct {
    tools   []shuttle.Tool
    trigger func(msg string) bool
}

// In Agent struct:
lazyToolSets []lazyToolSet

// Option constructor:
func WithLazyTools(tools []shuttle.Tool, trigger func(msg string) bool) Option {
    return func(a *Agent) {
        a.lazyToolSets = append(a.lazyToolSets, lazyToolSet{tools: tools, trigger: trigger})
    }
}

// Public method for post-construction registration:
func (a *Agent) RegisterLazyTools(tools []shuttle.Tool, trigger func(msg string) bool) {
    a.lazyToolSets = append(a.lazyToolSets, lazyToolSet{tools: tools, trigger: trigger})
}
```

In the pre-LLM turn logic, evaluate triggers:
```go
for _, set := range a.lazyToolSets {
    if set.trigger(userMessage) {
        for _, t := range set.tools {
            if !a.tools.IsRegistered(t.Name()) {
                a.tools.Register(t)
            }
        }
    }
}
```

### 2.2 UI intent detection (`pkg/server/app_tools.go`)

```go
var UIKeywords = []string{
    "chart", "graph", "dashboard", "visualization", "table",
    "ui", "app", "display", "plot", "report", "widget",
}

func ContainsUIIntent(msg string) bool {
    lower := strings.ToLower(msg)
    for _, kw := range UIKeywords {
        if strings.Contains(lower, kw) { return true }
    }
    return false
}
```

### 2.3 `cmd/looms/cmd_serve.go`

Replace eager registration with lazy:
```go
// Before:
uiAppTools := server.UIAppTools(appCompiler, uiRegistry)
ag.RegisterTools(uiAppTools...)

// After:
uiAppTools := server.UIAppTools(appCompiler, uiRegistry)
ag.RegisterLazyTools(uiAppTools, server.ContainsUIIntent)
```

### 2.4 Tests

**Unit (`pkg/agent/`)**
- `TestLazyTools_NotRegisteredAtStart`: agent with `RegisterLazyTools` has 0 UI tools in `ListTools()` before any message
- `TestLazyTools_RegisteredOnTrigger`: after calling trigger func with "create a dashboard", UI tools appear in `ListTools()`
- `TestLazyTools_NotRegisteredOnNonMatchingMessage`: non-UI message doesn't add tools
- `TestLazyTools_NoDoubleRegistration`: calling trigger twice doesn't add duplicate tools
- `TestLazyTools_ConcurrentTriggers`: 10 goroutines fire the trigger simultaneously — no race, no duplicates
- Race: `go test -tags fts5 -race ./pkg/agent/...`

**Unit (`pkg/server/`)**
- `TestContainsUIIntent`: table-driven — "create a dashboard"→true, "chart the sales"→true, "query the database"→false, ""→false

**E2E (`test/e2e/lazy_tools_e2e_test.go`)** — `//go:build integration`

```go
// TestE2E_LazyUITools_AbsentOnFreshSession verifies that a newly created
// session's tool list does NOT include UI tools before any message is sent.
func TestE2E_LazyUITools_AbsentOnFreshSession(t *testing.T)
// Uses ListTools RPC, asserts none of create_ui_app/update_ui_app/
// delete_ui_app/list_component_types are present.

// TestE2E_LazyUITools_PresentAfterUIRequest verifies that after sending a
// message containing "create a dashboard", the tool list includes UI tools
// on the next call.
func TestE2E_LazyUITools_PresentAfterUIRequest(t *testing.T)
// Sends "create a dashboard" via Weave, then calls ListTools and asserts
// create_ui_app is present.

// TestE2E_LazyUITools_ToolCountReduced verifies that non-UI conversations
// show fewer tools than the old eager count (regression guard).
func TestE2E_LazyUITools_ToolCountReduced(t *testing.T)
// Asserts len(ListTools()) < 16 for a session that never mentions UI.
```

---

## Phase 3: Schema Pruning `feat/schema-pruning`

**Impact**: 15–25% smaller schemas, compounds with caching
**Effort**: 2 days
**Risk**: Low — LLM quality risk mitigated by keeping parameter names clear

### Approach

Two tracks in parallel:

**Track A** — Prune `InputSchema()` property descriptions to ≤15 words:
- `shared_memory_write.namespace`: "global/workflow/swarm/agent scope"
- `create_ui_app.spec`: "declarative app spec object"
- `grpc_call.reflection_address`: "gRPC reflection server address"
- `http_request.headers`: "request headers map"

**Track B** — Use PromptRegistry for tool-level `Description()`:
- Create `prompts/tools/*.yaml` with concise descriptions
- The `PromptAwareTool` wrapper already loads these when a registry is provided
- Reduces `Description()` tokens by 50%+ without touching Go source

### Tests

**Unit (`pkg/shuttle/builtin/`)**
- `TestToolSchemaSize`: table-driven across all tools — `json.Marshal(tool.InputSchema())` must be
  under a per-tool byte ceiling. Serves as a regression guard preventing future bloat.

  ```go
  var maxSchemaBytes = map[string]int{
      "shell_execute":        900,
      "agent_management":     500,
      "shared_memory_write":  600,
      "top_n_query":          700,
      "group_by_query":       700,
      // etc.
  }
  ```

- `TestToolDescriptionSize`: `len(tool.Description())` must be under 400 chars per tool
- `TestAllToolsTotalTokenBudget`: sum of all tool schemas ≤ 8,000 chars (~2,000 tokens). Hard ceiling.
- Race: `go test -tags fts5 -race ./pkg/shuttle/...`

**E2E (`test/e2e/schema_size_e2e_test.go`)** — `//go:build integration`

```go
// TestE2E_ToolSchemas_TotalTokenBudget retrieves all registered tools via
// ListTools RPC and verifies the combined schema size is under 8,000 chars.
// This is the real-world gate — catches any tool that slips past unit ceiling.
func TestE2E_ToolSchemas_TotalTokenBudget(t *testing.T)
// Calls ListTools, marshals each tool's description+schema, sums, asserts < 8000.

// TestE2E_ToolSchemas_InputTokenCountReduced verifies that turn 1 of a
// Weave call reports fewer input_tokens than the pre-pruning baseline of 12,000.
func TestE2E_ToolSchemas_InputTokenCountReduced(t *testing.T)
// Asserts resp.GetCost().GetLlmCost().GetInputTokens() < 9000 (25% reduction floor).
```

---

## Phase 4: Meta-Tool Consolidation `feat/meta-tools`

**Impact**: 11 tools → 5 meta-tools = 60–70% fewer schema tokens
**Effort**: 2–3 weeks
**Risk**: Medium — backwards compat via aliases, but requires thorough testing

### Reference pattern

Study `pkg/shuttle/builtin/agent_management.go` before writing any meta-tool.
Key design: minimal `InputSchema()` with only `action` required; route in `Execute()` switch.

### Consolidation map

| New tool | Replaces | Actions |
|----------|----------|---------|
| `filesystem` | `file_read`, `file_write` | `read`, `write`, `list`, `delete` |
| `memory` | `shared_memory_read`, `shared_memory_write` | `read`, `write` |
| `network` | `http_request`, `grpc_call` | `http`, `grpc` |
| `messaging` | `send_message`, `publish` | `send`, `publish` |
| `ui` | `create_ui_app`, `update_ui_app`, `delete_ui_app`, `list_component_types` | `create`, `update`, `delete`, `list_types` |

### Backward compatibility

Register aliases in `pkg/shuttle/builtin/registry.go` `ByName()`:
```go
case "file_read":  return newFilesystemAlias("read")
case "file_write": return newFilesystemAlias("write")
// etc.
```

Existing agent YAMLs with old tool names continue to work. New agent YAMLs use consolidated names.

No proto changes required. Tool names are `[]string` in agent config.

### New files

- `pkg/shuttle/builtin/filesystem.go`
- `pkg/shuttle/builtin/memory_tool.go`
- `pkg/shuttle/builtin/network_tool.go`
- `pkg/shuttle/builtin/messaging_tool.go`
- `pkg/server/ui_tool.go` (consolidates app_tools.go)

### Tests

**Unit (`pkg/shuttle/builtin/`)**
- `TestFilesystemTool_Actions`: table-driven: read, write, list, delete — each action executes correctly
- `TestMemoryTool_Actions`: read, write actions
- `TestNetworkTool_Actions`: http GET, grpc call
- `TestMessagingTool_Actions`: send, publish
- `TestUITool_Actions`: create, update, delete, list_types (mock compiler/provider)
- `TestBuiltinAlias_FileRead`: `ByName("file_read").Execute(readInput)` produces same output as `filesystem{action:"read"}.Execute(readInput)`
- `TestBuiltinAlias_FileWrite`: same for file_write
- `TestBuiltinAlias_SharedMemory`: same for shared_memory_read + shared_memory_write
- `TestRegistry_OldNamesResolvable`: all deprecated names still resolve via `ByName()`
- `TestMetaToolSchemaSize`: all 5 new meta-tools individually under 500 chars each
- Race: `go test -tags fts5 -race ./pkg/shuttle/...`

**E2E (`test/e2e/meta_tools_e2e_test.go`)** — `//go:build integration`

```go
// TestE2E_MetaTool_Filesystem_ReadWrite runs a real Weave session where the
// agent reads a temp file and writes a result file using the filesystem tool.
// Verifies the agent completes COMPLETED stage with correct file contents.
func TestE2E_MetaTool_Filesystem_ReadWrite(t *testing.T)

// TestE2E_MetaTool_BackwardCompat_FileRead verifies that an agent configured
// with tools: [file_read, file_write] (old names) still successfully reads
// and writes files after the meta-tool migration.
func TestE2E_MetaTool_BackwardCompat_FileRead(t *testing.T)

// TestE2E_MetaTool_ToolCount verifies that a fully configured agent using
// meta-tools has <= 8 tools registered (down from 16).
func TestE2E_MetaTool_ToolCount(t *testing.T)
// Asserts len(ListTools()) <= 8 for the default agent after migration.

// TestE2E_MetaTool_Network_HTTP runs a real Weave session where the agent
// uses the network tool to make an HTTP request and returns the result.
func TestE2E_MetaTool_Network_HTTP(t *testing.T)
```

**Agent YAML fixtures for E2E** (`test/e2e/fixtures/`):
- `meta-tools-agent.yaml` — uses new consolidated tool names
- `legacy-tools-agent.yaml` — uses old names (file_read, file_write, shared_memory_read, etc.)

Both must produce `COMPLETED` responses in E2E tests.

---

## Token Savings Summary

| Phase | Change | Tokens saved/request | Rate limit effect |
|-------|--------|---------------------|-------------------|
| 1 | Prompt caching | 0 nominal, ~12K free | 5–10x throughput |
| 2 | Lazy UI tools | ~3,000 for non-UI | -25% for typical agents |
| 3 | Schema pruning | ~2,000–3,000 | -17–25% |
| 4 | Meta-tools | ~6,000–8,000 | -50–67% |
| **All** | Combined | **~90% reduction** | **Near-unlimited** |

## Observability: measuring progress

After Phase 1 lands, watch these trace attributes in `~/.loom/observability.db`:
- `llm.tokens.cache_read` rising → Phase 1 working
- `llm.tools.count` dropping → Phases 2/4 working
- `llm.cache.hit_rate` → target >80% for multi-turn sessions
- `llm.cost.usd` → should drop significantly despite same throughput

---

## Phase completion checklist template

Copy this into each PR description:

```
### Completion checklist

- [ ] `just check` passes (proto-lint + fmt + vet + lint + test + build + security)
- [ ] `go test -tags fts5 -race ./...` — zero races
- [ ] New unit tests cover all changed functions
- [ ] `TestE2E_*` tests in test/e2e/ pass against running looms server
- [ ] `~/.loom/observability.db` queried manually — expected metrics present
- [ ] CLAUDE.md / docs updated if new patterns introduced
- [ ] No TODO comments left in implementation
```
