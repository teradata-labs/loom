# Judge System Analysis: Why It Didn't Catch Standalone Python Output

**Date**: 2025-12-24
**Issue**: Weaver generated standalone Python code instead of Loom YAML configs
**Question**: Why didn't the judge system catch this?

---

## TL;DR: Judge Validates SPECIFIC Hallucinations, Not OUTPUT TYPE

**The judge system exists** (`coordinator.go:1043-1147`) but it checks for:
- ✅ MCP server hallucinations (servers that don't exist)
- ✅ Impossible physical requests ("robot", "pour", "grab")
- ✅ YAML parsing errors
- ✅ Tool availability

**The judge does NOT check**:
- ❌ **Is this a Loom agent config vs standalone code?**
- ❌ **Does the YAML follow Loom agent schema?**
- ❌ **Is the system_prompt task-oriented (no role-prompting)?**
- ❌ **Are tools/patterns actual Loom constructs?**

---

## Judge System Current Implementation

### Location
`pkg/metaagent/coordinator.go:1043-1147`

### What It Validates

#### 1. **YAML Syntax Validation** (Lines 1048-1051)
```go
var config map[string]interface{}
if err := yaml.Unmarshal([]byte(result.ConfigYAML), &config); err != nil {
    return fmt.Errorf("generated YAML is invalid: %w", err)
}
```

**Problem**: This only checks if it's **valid YAML**, not if it's **valid Loom agent YAML**.

**Example that would pass**:
```yaml
name: WeatherAgent
code: |
  class WeatherAgent:
      def __init__(self):
          self.api = "https://openmeteo.com"
```
☝️ Valid YAML, but NOT a Loom agent!

---

#### 2. **MCP Server Hallucination Detection** (Lines 1056-1096)
```go
// Validate MCP servers exist
if tools, ok := config["tools"].(map[string]interface{}); ok {
    if mcp, ok := tools["mcp"].([]interface{}); ok {
        for _, mcpTool := range mcp {
            serverName, _ := mcpMap["server"].(string)
            // Check if server exists in mcpManager
        }
    }
}
```

**Good**: Catches hallucinated MCP servers like `"vantage-mcp-ultra-pro-max"`

**Problem**: Doesn't check if the tools are Loom shuttle tools vs custom Python methods

---

#### 3. **Impossible Physical Request Detection** (Lines 1098-1113)
```go
impossibleKeywords := []string{
    "physical", "real world", "hardware", "robot", "pour", "grab", "pick up",
    "move", "touch", "press", "button", "switch", "actuator", "motor",
}
```

**Good**: Catches requests like "pour me a coffee" or "press the button"

**Problem**: Weather API fetching isn't physical, so standalone Python code for it would pass

---

#### 4. **Tool Availability Check** (Lines 1115-1119)
```go
if len(toolSel.BuiltinTools) == 0 && len(toolSel.MCPTools) == 0 {
    issues = append(issues, "No suitable tools found for the requested capabilities")
}
```

**Good**: Catches when no tools were selected

**Problem**: Doesn't validate that the YAML actually **uses** those tools via shuttle's tool system

---

## Why Standalone Python Passed Validation

### Scenario: User asks for "weather agent using openmeteo"

#### **What the BROKEN weaver did**:
1. ✅ DeepAnalyzer: Analyzed requirements (domain: rest, tools: http_request)
2. ✅ ToolSelector: Selected `http_request` tool
3. ✅ PatternMatcher: Selected REST patterns
4. ✅ WorkflowDesigner: Single agent workflow
5. ✅ ConflictDetector: No conflicts
6. ❌ ConfigAssembler: **Generated standalone Python class instead of Loom YAML**
7. ✅ Judge: **PASSED VALIDATION** (see below)

#### **Why the judge passed it**:
```
Judge Check 1: Is it valid YAML?
  → YES (could be wrapped: `code: | <Python here>`)
  ✅ PASS

Judge Check 2: MCP servers hallucinated?
  → NO (no MCP tools mentioned, or they exist)
  ✅ PASS

Judge Check 3: Impossible physical request?
  → NO ("weather" and "api" are not in impossibleKeywords)
  ✅ PASS

Judge Check 4: Tools selected?
  → YES (http_request was selected)
  ✅ PASS

Result: ✅ VALIDATION PASSED ← WRONG!
```

---

## What the Judge SHOULD Have Checked

### Critical Missing Validations

#### 1. **Loom Agent Schema Validation**
```go
// MISSING: Validate against Loom agent schema
requiredFields := []string{"name", "system_prompt", "llm_config", "memory_config"}
for _, field := range requiredFields {
    if _, ok := config[field]; !ok {
        issues = append(issues, fmt.Sprintf("Missing required Loom field: %s", field))
    }
}
```

#### 2. **Anti-Pattern Detection (Role Prompting)**
```go
// MISSING: Check for role-prompting in system_prompt
if systemPrompt, ok := config["system_prompt"].(string); ok {
    rolePromptingPatterns := []string{
        "(?i)you are a",
        "(?i)as a",
        "(?i)act as",
        "(?i)pretend to be",
    }
    for _, pattern := range rolePromptingPatterns {
        if matched, _ := regexp.MatchString(pattern, systemPrompt); matched {
            issues = append(issues, "System prompt contains role-prompting (anti-pattern)")
        }
    }
}
```

#### 3. **Loom Framework Context Check**
```go
// MISSING: Verify this is actually a Loom agent, not standalone code
suspiciousPatterns := []string{
    "class .+Agent:",           // Python class definition
    "def __init__",             // Python constructor
    "import ",                  // Python imports
    "function .+Agent\\(",      // JavaScript function
    "module.exports",           // Node.js exports
}
for _, pattern := range suspiciousPatterns {
    if matched, _ := regexp.MatchString(pattern, result.ConfigYAML); matched {
        issues = append(issues, "Output appears to be standalone code, not Loom YAML config")
        suggestions = append(suggestions, "Loom agents are YAML configurations, not code implementations")
    }
}
```

#### 4. **Tool Usage Validation**
```go
// MISSING: Verify tools are referenced in agent config
if len(toolSel.BuiltinTools) > 0 {
    toolsSection, ok := config["tools"]
    if !ok {
        issues = append(issues, "Tools were selected but not included in config")
    } else {
        // Check if selected tools appear in config
        for _, toolName := range toolSel.BuiltinTools {
            if !containsTool(toolsSection, toolName) {
                issues = append(issues, fmt.Sprintf("Selected tool '%s' not found in config", toolName))
            }
        }
    }
}
```

#### 5. **Pattern Validation**
```go
// MISSING: Verify patterns exist and are referenced correctly
if len(patternSel.SelectedPatterns) > 0 {
    patternsSection, ok := config["patterns"]
    if !ok {
        issues = append(issues, "Patterns were selected but not included in config")
    } else {
        // Check if patterns actually exist in library
        for _, patternName := range patternSel.SelectedPatterns {
            if !wc.patternLibrary.Exists(patternName) {
                issues = append(issues, fmt.Sprintf("Pattern '%s' does not exist in library", patternName))
            }
        }
    }
}
```

---

## Recommended Fixes

### P0 - Critical (Implement Immediately)

1. **Add Loom Schema Validation**
   ```go
   func validateLoomAgentSchema(config map[string]interface{}) []string
   ```
   - Check for required fields: name, system_prompt, llm_config, memory_config
   - Validate field types match schema
   - Ensure tools/patterns sections follow Loom format

2. **Add Standalone Code Detection**
   ```go
   func detectStandaloneCode(yamlContent string) (bool, string)
   ```
   - Regex patterns for class definitions, imports, function exports
   - Language-specific markers (Python `def`, JS `function`, etc.)
   - Return what language was detected if found

3. **Add Anti-Pattern Validation**
   ```go
   func validateSystemPrompt(systemPrompt string) []string
   ```
   - Check for role-prompting patterns
   - Validate task-oriented structure
   - Ensure no "you are a..." constructs

### P1 - High (Implement This Sprint)

4. **Add Tool Reference Validation**
   ```go
   func validateToolReferences(config map[string]interface{}, selectedTools []string) []string
   ```
   - Verify selected tools appear in config
   - Check tool names match registry
   - Validate MCP tool format

5. **Add Pattern Reference Validation**
   ```go
   func validatePatternReferences(config map[string]interface{}, selectedPatterns []string, library *patterns.Library) []string
   ```
   - Verify selected patterns appear in config
   - Check patterns exist in library
   - Validate pattern names

### P2 - Medium (Next Sprint)

6. **Add LLM-Based Semantic Validation**
   ```go
   func semanticValidation(ctx context.Context, llm agent.LLMProvider, result *AgentGenerationResult) (bool, string)
   ```
   - Use LLM to read generated YAML
   - Ask: "Is this a valid Loom agent configuration or standalone code?"
   - Ask: "Does the system_prompt follow task-oriented guidelines?"
   - Return validation result with reasoning

7. **Add Integration Test**
   ```go
   func TestJudgeDetectsStandalonePython(t *testing.T)
   ```
   - Generate standalone Python
   - Pass to judge
   - Assert: validation FAILS with appropriate message

---

## Example: What SHOULD Have Happened

### Input
```
User: "create a weather agent that uses openmeteo"
```

### Old Broken Flow (Pre-Fix)
```
1. DeepAnalyzer → domain:rest ✅
2. ToolSelector → http_request ✅
3. ConfigAssembler → STANDALONE PYTHON ❌
4. Judge → PASS ❌ (only checked MCP/physical/YAML syntax)
5. Result → User gets Python class ❌
```

### New Fixed Flow (Post-Fix)
```
1. DeepAnalyzer → domain:rest ✅ (now with LOOM FRAMEWORK CONTEXT)
2. ToolSelector → http_request ✅
3. ConfigAssembler → LOOM YAML CONFIG ✅ (understands it's building Loom agent)
4. Judge → PASS ✅ (validates schema, anti-patterns, tool references)
5. Result → User gets valid Loom agent ✅
```

### IF ConfigAssembler Still Generated Standalone Code
```
4. Judge → FAIL ❌
   Issues detected:
   - Output contains 'class WeatherAgent:' (standalone code pattern)
   - Missing required Loom fields: system_prompt, llm_config
   - Selected tool 'http_request' not found in config

   Suggestions:
   - Loom agents are YAML configurations, not code implementations
   - Regenerate using Loom agent schema
```

---

## Root Cause Analysis

### Why This Wasn't Caught Earlier

1. **Judge was designed for hallucinations**, not output type validation
   - MCP servers that don't exist
   - Physical impossibilities
   - Not for "is this actually what we wanted?"

2. **No schema validation** against Loom agent structure
   - Judge parses YAML but doesn't validate against schema
   - No check for required fields

3. **No anti-pattern detection**
   - Role-prompting check doesn't exist in judge
   - Standalone code patterns not detected

4. **No semantic validation**
   - Judge doesn't use LLM to understand output
   - Pure rule-based checks

### Why This Matters

**User Impact**:
- User asks for "weather agent"
- Gets Python class instead of runnable Loom agent
- Wasted time, confusion, lost trust

**System Impact**:
- Weaver appears broken
- Can't trust generated configs
- Manual validation required

---

## Testing Strategy

### Unit Tests Needed

```go
// Test judge detects standalone Python
func TestJudgeDetectsStandalonePython(t *testing.T)

// Test judge detects role-prompting
func TestJudgeDetectsRolePrompting(t *testing.T)

// Test judge validates Loom schema
func TestJudgeValidatesLoomSchema(t *testing.T)

// Test judge validates tool references
func TestJudgeValidatesToolReferences(t *testing.T)

// Test judge validates pattern references
func TestJudgeValidatesPatternReferences(t *testing.T)
```

### Integration Tests Needed

```go
// End-to-end: User request → Loom agent (not standalone code)
func TestWeaverGeneratesLoomAgent(t *testing.T)

// End-to-end: Invalid output → Judge catches it
func TestJudgeCatchesInvalidOutput(t *testing.T)
```

---

## Conclusion

**The judge system exists and works** - it just wasn't checking for the right things.

**What it checks**:
- ✅ MCP hallucinations (good!)
- ✅ Physical impossibilities (good!)
- ✅ YAML syntax (good!)

**What it SHOULD check**:
- ❌ Is this a Loom agent config? (CRITICAL!)
- ❌ Does it follow Loom schema? (CRITICAL!)
- ❌ Anti-patterns (role-prompting)? (HIGH!)
- ❌ Tool/pattern references valid? (HIGH!)

**Fix Priority**:
1. **P0**: Add standalone code detection + Loom schema validation
2. **P1**: Add anti-pattern detection + tool/pattern validation
3. **P2**: Add LLM-based semantic validation

**Impact**: With these fixes, the judge would have **caught and rejected** the standalone Python output, preventing the user from ever seeing it.
