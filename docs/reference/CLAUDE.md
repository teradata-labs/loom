# Technical Reference Documentation Standards

## Purpose

The `reference/` directory contains **technical specifications** for developers building with or extending Loom.

**Target Audience**: Developers, integrators, and system administrators who need precise technical details.

**Not For**: End users seeking how-to guides (those belong in `/guides/`).

---

## Critical Rules

### 1. Developer-First Content

Reference docs answer:
- **"What are all the options?"** (complete parameter lists)
- **"What's the exact syntax?"** (precise API signatures, command formats)
- **"What does this return?"** (response schemas, exit codes, error conditions)
- **"What are the constraints?"** (limits, requirements, edge cases)

Reference docs do NOT answer:
- ❌ "How do I accomplish task X?" → That's a guide
- ❌ "Why should I use this?" → That's architecture or a guide
- ❌ "What's the overall design?" → That's architecture

### 2. Completeness Over Brevity

Unlike guides, reference docs must be **exhaustive**:
- ✅ Every flag, parameter, option documented
- ✅ All return values and error codes listed
- ✅ Complete configuration examples with all fields
- ✅ Edge cases and constraints explicitly stated
- ✅ Version compatibility noted when relevant

**Example - BAD (incomplete):**
```markdown
## agent.timeout

Configure agent timeout.
```

**Example - GOOD (complete):**
```markdown
## agent.timeout

Configure maximum execution time for agent operations.

**Type**: `duration` (e.g., `30s`, `5m`, `1h`)
**Default**: `300s` (5 minutes)
**Range**: `1s` - `1h`
**Required**: No

**Behavior**:
- Applies to: LLM calls, tool executions, total conversation time
- When exceeded: Operation cancelled, error returned to client
- Session state: Preserved (conversation history maintained)
- Retries: Not automatic (client must retry if desired)

**Example**:
```yaml
agent:
  timeout: 120s  # 2 minutes
```

**See Also**: `limits.max_turns`, `tools.timeout`
```

### 3. Structure Requirements

Every reference document MUST include:

1. **Table of Contents** - For any doc >200 lines
2. **Quick Reference Section** - Summary table/list at top
3. **Detailed Sections** - One section per concept/command/API
4. **Examples** - At least 2 working examples per major feature
5. **Cross-references** - Links to related docs

**Standard Template:**

```markdown
---
title: "[Feature] Reference"
weight: [number]
---

# [Feature] Reference

Brief 1-2 sentence description.

**Version**: v1.0.0-beta.2

---

## Quick Reference

[Summary table or command list]

---

## [Section 1]

### [Subsection 1.1]

**Description**: What this does

**Syntax/Signature**: Exact format

**Parameters/Options**:
- `param1` (type) - Description, default, constraints
- `param2` (type) - Description, default, constraints

**Returns/Output**: What you get back

**Errors**: Possible error conditions

**Examples**:

```[language]
[Working example 1]
```

```[language]
[Working example 2]
```

**When to Use**: Clear use cases

**Constraints/Limitations**: What doesn't work

**See Also**: Related docs

---

## Configuration Reference

[Complete config schema with all fields]

---

## Error Codes

[All possible errors with codes and meanings]

---

## See Also

- Related reference docs
- Relevant guides for practical usage
```

### 4. No Marketing Speak (Same as Guides)

Reference docs are **purely technical**. Never use:
- ❌ "seamless" / "effortless"
- ❌ "powerful" / "robust"
- ❌ "comprehensive" / "complete" (unless you document EVERY feature)
- ❌ "production-ready" (unless verified with tests)
- ❌ "enterprise-grade"

Use **specific, measurable technical facts**:
- ✅ "Supports 5 LLM providers: Anthropic, Bedrock, Ollama, OpenAI, Azure"
- ✅ "Maximum 10,000 rows per query result"
- ✅ "Hot reload completes in 89-143ms"
- ✅ "Configuration supports JSON, YAML, TOML formats"

### 5. Code Examples Must Be Complete and Working

Every code example must:
1. **Be complete** - Copy-paste runnable without modifications
2. **Include context** - Show imports, setup, teardown if needed
3. **Have expected output** - Show what the code produces
4. **Handle errors** - Include error handling when relevant
5. **Be tested** - Actually run the code before documenting

**Example - BAD:**
```go
agent.Chat(ctx, "query")
```

**Example - GOOD:**
```go
package main

import (
    "context"
    "fmt"
    "log"

    "github.com/teradata-labs/loom/pkg/agent"
)

func main() {
    ctx := context.Background()

    // Create agent with backend and LLM
    backend := createBackend() // see backend.md
    llm := createLLMProvider()  // see llm-providers.md

    ag := agent.NewAgent(backend, llm)

    // Send query
    response, err := ag.Chat(ctx, "sess_123", "Show sales by region")
    if err != nil {
        log.Fatalf("Chat failed: %v", err)
    }

    fmt.Printf("Agent: %s\n", response.Message)
    fmt.Printf("Tokens used: %d\n", response.TokensUsed)
}

// Output:
// Agent: Based on the sales data, here are the results by region:
// West: $2.4M, East: $2.1M, South: $1.5M, Central: $0.9M
// Tokens used: 1,245
```

### 6. Technical Precision

Use **exact terminology**:
- ✅ "gRPC service" not "API"
- ✅ "HTTP/1.1 gateway" not "REST endpoint"
- ✅ "YAML configuration file" not "config"
- ✅ "SQLite database" not "storage"
- ✅ "Exponential backoff with jitter" not "retry logic"

Specify **exact types**:
- ✅ `string` / `int64` / `float64` / `bool`
- ✅ `[]string` / `map[string]interface{}`
- ✅ `duration` / `timestamp` / `enum`
- ✅ `required` / `optional` / `default: value`

Document **exact behavior**:
- ✅ "Timeout after exactly 30 seconds, no retries"
- ✅ "Caches result for 5 minutes using LRU eviction"
- ✅ "Returns HTTP 503 when circuit breaker open"

### 7. Version and Compatibility Information

Always specify:
- **Feature availability**: "Available since: v0.7.0"
- **Breaking changes**: "⚠️ Breaking change in v1.0.0-beta.2: field renamed from X to Y"
- **Deprecations**: "⚠️ Deprecated in v0.8.0, removed in v1.0.0"
- **Platform compatibility**: "Requires: Go 1.21+, Linux/macOS/Windows"

**Example:**
```markdown
## agent.memory.type

**Available since**: v0.8.2
**Supported values**: `memory`, `sqlite`
**Planned**: `postgres` (v1.0.0)

⚠️ **Breaking change in v1.0.0-beta.2**: Field renamed from `storage_type` to `memory.type`

**Platform notes**:
- `sqlite`: Works on all platforms
- `postgres`: Requires PostgreSQL 12+ (coming in v1.0.0)
```

### 8. Configuration Reference Format

All configuration sections must use this format:

```markdown
## config.section.field

**Type**: `type`
**Default**: `default_value`
**Required**: Yes/No
**Available since**: vX.Y.Z

**Description**: What this field does.

**Allowed values**:
- `value1` - What this means
- `value2` - What this means

**Constraints**:
- Must be > 0
- Cannot exceed max_value
- Must match pattern: `^[a-z]+$`

**Example**:
```yaml
config:
  section:
    field: value
```

**Environment variable**: `LOOM_SECTION_FIELD`

**Command-line flag**: `--section-field`

**See also**: Related config fields
```

### 9. API Reference Format

For Go APIs:

```markdown
## func FunctionName

```go
func FunctionName(ctx context.Context, param1 Type1, param2 Type2) (ReturnType, error)
```

**Description**: What this function does.

**Parameters**:
- `ctx` - Context for cancellation and deadlines
- `param1` (`Type1`) - Parameter description, constraints
- `param2` (`Type2`) - Parameter description, constraints

**Returns**:
- `ReturnType` - Description of return value
- `error` - Possible errors (see Error Codes section)

**Errors**:
| Error | Condition |
|-------|-----------|
| `ErrInvalidParameter` | When param1 is nil or empty |
| `ErrTimeout` | When operation exceeds context deadline |
| `ErrNotFound` | When resource doesn't exist |

**Thread safety**: Safe for concurrent use / Not safe for concurrent use

**Example**:

```go
ctx := context.Background()
result, err := FunctionName(ctx, value1, value2)
if err != nil {
    return fmt.Errorf("operation failed: %w", err)
}
fmt.Printf("Result: %v\n", result)
```

**See also**: `RelatedFunction`, `RelatedType`
```

For gRPC APIs:

```markdown
## Weave RPC

```proto
rpc Weave(WeaveRequest) returns (WeaveResponse)
```

**Description**: Single-turn agent interaction.

**Request**:
```proto
message WeaveRequest {
  string session_id = 1;   // Required: Session identifier
  string message = 2;      // Required: User message
  repeated string tools = 3; // Optional: Tool whitelist
}
```

**Response**:
```proto
message WeaveResponse {
  string message = 1;       // Agent response
  int64 tokens_used = 2;    // Total tokens consumed
  repeated ToolCall tools = 3; // Tools executed
}
```

**Errors**:
| Code | Error | Condition |
|------|-------|-----------|
| `INVALID_ARGUMENT` | `session_id empty` | When session_id not provided |
| `RESOURCE_EXHAUSTED` | `max turns exceeded` | When session exceeds max_turns limit |
| `UNAVAILABLE` | `agent not ready` | When agent still starting up |

**Example (Go client)**:

```go
import (
    loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

client := loomv1.NewLoomServiceClient(conn)

resp, err := client.Weave(ctx, &loomv1.WeaveRequest{
    SessionId: "sess_abc123",
    Message:   "Show sales by region",
})
if err != nil {
    log.Fatalf("RPC failed: %v", err)
}

fmt.Printf("Response: %s\n", resp.Message)
```

**Example (HTTP gateway)**:

```bash
curl -X POST http://localhost:8080/v1/weave \
  -H "Content-Type: application/json" \
  -d '{
    "session_id": "sess_abc123",
    "message": "Show sales by region"
  }'
```

**See also**: `StreamWeave` (streaming variant)
```

### 10. Error Documentation Requirements

Every reference doc must have an **Error Codes** or **Errors** section:

```markdown
## Error Codes

### ErrInvalidConfiguration

**Code**: `invalid_configuration`
**HTTP Status**: 400 Bad Request
**gRPC Code**: `INVALID_ARGUMENT`

**Cause**: Configuration file has syntax errors or invalid values.

**Example**:
```
Error: invalid_configuration: field 'agent.timeout' must be positive duration, got: -5s
```

**Resolution**:
1. Validate YAML syntax: `looms config validate`
2. Check field types match schema
3. Verify all required fields present

**See also**: Configuration Reference

---

### ErrConnectionFailed

**Code**: `connection_failed`
**HTTP Status**: 503 Service Unavailable
**gRPC Code**: `UNAVAILABLE`

**Cause**: Cannot connect to backend service (database, API, MCP server).

**Example**:
```
Error: connection_failed: failed to connect to postgres://localhost:5432: connection refused
```

**Resolution**:
1. Verify service is running: `pg_isready` or `curl`
2. Check firewall/network connectivity
3. Verify credentials in configuration
4. Check service logs for errors

**Retry behavior**: Automatic exponential backoff, max 3 attempts
```

---

## Reference Document Types

### 1. CLI Reference (`cli.md`)

**Purpose**: Complete command-line interface documentation

**Must include**:
- Table of contents with ALL commands
- Every command with all flags
- Multiple examples per command
- Expected output for each example
- Environment variables
- Configuration file examples
- Exit codes
- Common workflows
- Troubleshooting section

**See**: `cli.md` as the gold standard example

---

### 2. API Reference (`[service]-api.md`)

**Purpose**: Programmatic API documentation

**Must include**:
- All functions/methods/RPCs
- Complete signatures with types
- Parameter descriptions with constraints
- Return value schemas
- Error conditions with codes
- Thread safety notes
- Code examples in relevant languages
- gRPC and HTTP gateway examples

---

### 3. Configuration Reference (`[system]-config.md`)

**Purpose**: All configuration options for a system

**Must include**:
- Every configuration field
- Types, defaults, constraints
- Hierarchical structure (YAML/JSON/TOML)
- Environment variable overrides
- Command-line flag equivalents
- Complete working examples
- Validation rules
- Secrets management (keyring references)

---

### 4. LLM Provider Reference (`llm-[provider].md`)

**Purpose**: Provider-specific integration details

**Must include**:
- Model availability and IDs
- Authentication methods
- Configuration options (region, endpoint, etc.)
- Rate limits and quotas
- Cost information (if public)
- Feature support matrix (streaming, tools, vision)
- Error codes specific to provider
- Region availability
- Setup instructions
- Troubleshooting

**Standard sections**:
```markdown
# [Provider] Reference

## Overview
[1-2 sentences]

## Models

| Model | ID | Features | Context Window |
|-------|----|-----------| ---------------|
| Claude Sonnet 4.5 | claude-sonnet-4-5-20250929 | Tools, vision, streaming | 200k |

## Configuration

### Required Fields
### Optional Fields
### Environment Variables

## Authentication

## Features

### Tool Calling
### Streaming
### Vision
### Function Calling

## Rate Limits

## Error Codes

## Examples

## Troubleshooting

## See Also
```

---

### 5. Backend Reference (`backend.md`)

**Purpose**: ExecutionBackend interface and implementations

**Must include**:
- Interface definition
- All methods with signatures
- Implementation guide for custom backends
- Available backend types
- Configuration per backend type
- Connection string formats
- Query result schemas
- Error handling patterns
- Performance considerations

---

### 6. Tool Reference (`tools.md` or `[toolset]-tools.md`)

**Purpose**: Tool system documentation

**Must include**:
- Tool interface definition
- Built-in tools list with descriptions
- Tool parameters and schemas
- Return value formats
- Error conditions
- Registration patterns
- Dynamic discovery (MCP)
- Tool execution lifecycle
- Concurrent execution behavior

---

### 7. Pattern Reference (`patterns.md`)

**Purpose**: Pattern system specification

**Must include**:
- Pattern YAML schema
- All fields with types and constraints
- Template variable syntax
- Example library structure
- Validation rules
- Hot reload behavior
- Pattern matching algorithm
- Variant system (A/B testing)
- Performance characteristics

---

### 8. Protocol Reference (`protocol.md`)

**Purpose**: Wire protocol and message formats

**Must include**:
- Proto definitions
- Message schemas
- Serialization formats
- Versioning strategy
- Compatibility rules
- Extension mechanisms

---

## Anti-Patterns (What NOT to Do)

### ❌ Mixing Guides and Reference

**BAD:**
```markdown
# Agent Configuration Reference

To configure an agent, first you need to understand that Loom uses
a multi-layered approach to agent management...

Here's how to get started:
1. Create a config file
2. Add your settings
3. Start the server
```

This is a guide, not reference. Reference should jump straight to the complete specification.

**GOOD:**
```markdown
# Agent Configuration Reference

Complete configuration options for Loom agents.

## Quick Reference

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `name` | string | required | Agent identifier |
| `llm.provider` | enum | required | LLM provider (anthropic, bedrock, ollama) |
| `llm.model` | string | required | Model ID |
| `llm.temperature` | float | 0.7 | Sampling temperature (0.0-2.0) |
| ... | ... | ... | ... |

## Configuration Schema

### name

**Type**: `string`
**Required**: Yes
**Constraints**: Must match `^[a-z][a-z0-9-]*$` (lowercase, alphanumeric, hyphens)
...
```

### ❌ Incomplete Parameter Lists

**BAD:**
```markdown
## looms serve

Start the server.

Flags:
- `--port` - Port number
- `--config` - Config file
```

**GOOD:**
```markdown
## looms serve

Start the multi-agent server.

**Flags**:
```
--config string       Path to server config (default: $LOOM_DATA_DIR/server.yaml)
--port int            gRPC port (default: 50051)
--http-port int       HTTP gateway port (default: 8080)
--agents stringArray  Agent configs to load (repeatable)
--hot-reload         Enable pattern hot reload (default: false)
--tls-cert string     TLS certificate path
--tls-key string      TLS private key path
--log-level string    Log level: debug|info|warn|error (default: info)
--log-format string   Log format: json|text (default: text)
```
```

### ❌ Missing Error Documentation

**BAD:**
```markdown
## agent.Chat

Send message to agent.

Returns: Response string
```

**GOOD:**
```markdown
## agent.Chat

```go
func (a *Agent) Chat(ctx context.Context, sessionID, message string) (*Response, error)
```

**Returns**:
- `*Response` - Agent response with message, tokens used, tool calls
- `error` - See error codes below

**Errors**:
| Error | Condition | Retry? |
|-------|-----------|--------|
| `ErrInvalidSession` | Session ID empty or invalid format | No |
| `ErrMaxTurnsExceeded` | Session exceeded max_turns limit | No |
| `ErrTimeout` | Operation exceeded timeout | Yes |
| `ErrLLMUnavailable` | LLM provider returned 503 | Yes |
```

### ❌ Vague Examples

**BAD:**
```bash
# Configure the server
looms config set some.value 123
```

**GOOD:**
```bash
# Set agent timeout to 2 minutes
looms config set agent.timeout 120s

# Configure MCP server with environment variable
looms config set mcp.servers.vantage.command ~/bin/vantage-mcp
looms config set mcp.servers.vantage.env.TD_USER myuser
looms config set mcp.servers.vantage.env.TD_PASSWORD "{{keyring:td_password}}"

# Verify configuration
looms config get mcp.servers.vantage
```

---

## Quality Checklist

Before committing any reference documentation:

- [ ] **Completeness**: All parameters, flags, options documented
- [ ] **Accuracy**: Every example tested and works
- [ ] **Precision**: Exact types, constraints, defaults specified
- [ ] **Errors**: All error conditions documented with codes
- [ ] **Examples**: At least 2 complete, working examples per major feature
- [ ] **Output**: Expected output shown for examples
- [ ] **Versions**: Availability and breaking changes noted
- [ ] **Cross-references**: Links to related docs provided
- [ ] **TOC**: Table of contents for docs >200 lines
- [ ] **No marketing**: Zero marketing speak, only technical facts
- [ ] **No mixing**: No how-to guide content in reference docs

---

## Verification Commands

### Check Completeness

For CLI reference:
```bash
# Extract all flags from help
looms serve --help | grep '^\s*--' | wc -l

# Compare to documented flags in cli.md
grep '^\s*--' docs/reference/cli.md | wc -l

# Numbers should match
```

For configuration reference:
```bash
# Extract all config fields from schema
cat config/schema.yaml | yq '.properties | keys' | wc -l

# Compare to documented fields
grep '^###' docs/reference/config.md | wc -l
```

### Test Examples

For code examples:
```bash
# Extract Go examples from markdown
awk '/```go/,/```/' docs/reference/api.md > /tmp/examples.go

# Test compilation
go build /tmp/examples.go
```

For CLI examples:
```bash
# Extract bash examples
awk '/```bash/,/```/' docs/reference/cli.md > /tmp/examples.sh

# Run shellcheck
shellcheck /tmp/examples.sh
```

---

## Documentation Workflow

1. **Plan**: Identify what needs documenting
   - New feature? → Create/update reference doc
   - Changed API? → Update API reference
   - New config option? → Update config reference

2. **Spec First**: Document the complete specification
   - All parameters, types, constraints
   - All error conditions
   - All edge cases

3. **Examples**: Write complete, working examples
   - Test each example
   - Show expected output
   - Include error handling

4. **Cross-reference**: Link to related docs
   - Related reference docs
   - Relevant guides for practical usage
   - Architecture docs for design context

5. **Review**: Check against quality checklist
   - Run verification commands
   - Test all examples
   - Check for completeness

6. **Version**: Note version availability
   - When feature was added
   - Any breaking changes
   - Deprecation warnings

---

## Reference vs. Guide vs. Architecture

**Reference** (this directory):
- **What**: Complete technical specifications
- **Audience**: Developers needing exact details
- **Style**: Exhaustive, precise, technical
- **Example**: "The `timeout` parameter accepts `duration` type (e.g., `30s`), defaults to `300s`, and must be between `1s` and `1h`"

**Guide** (`/guides/`):
- **What**: Task-oriented how-to instructions
- **Audience**: Users accomplishing specific goals
- **Style**: Step-by-step, practical, example-focused
- **Example**: "To set a 2-minute timeout: `looms config set agent.timeout 120s`"

**Architecture** (`/architecture/`):
- **What**: System design and concepts
- **Audience**: Architects understanding the system
- **Style**: Conceptual, high-level, no code snippets
- **Example**: "The timeout system uses a context-based cancellation pattern to ensure graceful shutdown"

---

## Questions to Ask Before Writing

1. **Is this complete?**
   - Did I document every parameter/flag/option?
   - Did I cover all error conditions?
   - Did I note all constraints and edge cases?

2. **Is this precise?**
   - Did I specify exact types (not just "number")?
   - Did I give exact defaults (not just "reasonable default")?
   - Did I specify exact behavior (not "attempts to retry")?

3. **Is this tested?**
   - Do all code examples compile/run?
   - Does all example output match reality?
   - Have I verified the behavior I'm documenting?

4. **Is this technical?**
   - Am I using marketing speak? (If yes, remove it)
   - Am I making claims without data? (If yes, add data or remove claim)
   - Am I showing, not telling? (Use examples, not assertions)

5. **Is this developer-appropriate?**
   - Would I find this useful if I were integrating with Loom?
   - Is there enough detail to use this without reading code?
   - Are error conditions clear enough to debug issues?

---

## Reference Documentation Responsibilities

### Individual Contributors
- **Completeness**: Document ALL options for your feature
- **Accuracy**: Test every example you write
- **Precision**: Use exact types, constraints, defaults
- **Honesty**: No marketing speak, no unverified claims

### Reviewers
- **Verify completeness**: Check against actual implementation
- **Test examples**: Run all code examples
- **Check cross-references**: Ensure links work
- **Enforce standards**: Hold docs to this CLAUDE.md standard

### Maintainers
- **Keep current**: Update docs when APIs change
- **Version tracking**: Note when features added/deprecated
- **Quality bar**: Reference docs are the highest bar for accuracy
- **No exceptions**: Reference docs cannot have incomplete or inaccurate information

---

**Remember**: Reference documentation is the contract between Loom and its developers. It must be complete, accurate, and precise. No compromises.
