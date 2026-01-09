# Judge Fix Plan: Additive Weaver-Specific Validations

**Date**: 2025-12-24
**Issue**: Need to add validations WITHOUT breaking other use cases
**Scope**: Weaver agent generation validation ONLY

---

## Current State Analysis

### Two Different "Judge" Systems

1. **REAL Judge System** (`pkg/evals/judges/`)
   - General-purpose LLM-as-a-judge
   - Evaluates: quality, safety, cost, domain dimensions
   - Used across: evals, teleprompter, learning, collaboration
   - **NOT what we're fixing**

2. **Weaver Validation** (`pkg/metaagent/coordinator.go:validateWithJudges`)
   - Weaver-specific config validation
   - Confusingly named "judges" (legacy naming)
   - **ONLY called in weaver coordinator** (line 566)
   - **This is what we're fixing**

### Key Finding

```bash
$ grep -n "validateWithJudges" pkg/**/*.go

pkg/metaagent/coordinator.go:566:    if err := wc.validateWithJudges(...)
pkg/metaagent/coordinator.go:1043:   func (wc *WeaverCoordinator) validateWithJudges(...)
```

**Only 1 call site** → Already weaver-specific! ✅

---

## Implementation Plan: Additive Approach

### Phase 1: Keep Existing Validations (Don't Break Anything)

**Current validations** (lines 1056-1119):
```go
✅ YAML syntax validation
✅ MCP server hallucination detection
✅ Physical impossibility detection
✅ Tool availability check
```

**Action**: KEEP EXACTLY AS-IS

---

### Phase 2: Add New Validations (Additive Pattern)

**Pattern**: Use existing `issues` and `suggestions` slices

```go
func (wc *WeaverCoordinator) validateWithJudges(...) error {
    // Existing code (lines 1047-1119)
    var config map[string]interface{}
    yaml.Unmarshal(result.ConfigYAML, &config)
    issues := []string{}
    suggestions := []string{}

    // [EXISTING] MCP validation (lines 1056-1096)
    // [EXISTING] Physical validation (lines 1098-1113)
    // [EXISTING] Tool availability (lines 1115-1119)

    // [NEW] Standalone code detection (ADDITIVE)
    if codeIssues := detectStandaloneCode(result.ConfigYAML); len(codeIssues) > 0 {
        issues = append(issues, codeIssues...)
    }

    // [NEW] Loom schema validation (ADDITIVE)
    if schemaIssues := validateLoomSchema(config); len(schemaIssues) > 0 {
        issues = append(issues, schemaIssues...)
    }

    // [NEW] Anti-pattern detection (ADDITIVE)
    if antiPatternIssues := validateAntiPatterns(config); len(antiPatternIssues) > 0 {
        issues = append(issues, antiPatternIssues...)
    }

    // [NEW] Tool reference validation (ADDITIVE)
    if toolIssues := validateToolReferences(config, toolSel); len(toolIssues) > 0 {
        issues = append(issues, toolIssues...)
    }

    // [NEW] Pattern reference validation (ADDITIVE)
    if patternIssues := validatePatternReferences(config, patternSel, wc.patternLibrary); len(patternIssues) > 0 {
        issues = append(issues, patternIssues...)
    }

    // [EXISTING] Error formatting (lines 1122-1143)
    if len(issues) > 0 {
        return fmt.Errorf(...)  // Same as before
    }

    return nil
}
```

**Why this approach is safe**:
1. ✅ Existing checks run first (same behavior)
2. ✅ New checks only APPEND to `issues` slice
3. ✅ Same error handling logic (lines 1122-1143)
4. ✅ No breaking changes to function signature
5. ✅ Only called in weaver context (one call site)

---

### Phase 3: Extract Validators (Clean Code)

Create helper functions with clear naming:

```go
// detectStandaloneCode checks if output is code instead of Loom YAML
func detectStandaloneCode(yamlContent string) []string {
    issues := []string{}

    patterns := map[string]string{
        `class\s+\w+Agent\s*\(`: "Python class definition",
        `def\s+__init__`:        "Python constructor",
        `import\s+\w+`:          "Python import",
        // ... more patterns
    }

    for pattern, desc := range patterns {
        if matched, _ := regexp.MatchString(pattern, yamlContent); matched {
            issues = append(issues, fmt.Sprintf(
                "Output contains standalone code (%s). Loom agents are YAML configs, not code implementations.",
                desc))
            break
        }
    }

    return issues
}

// validateLoomSchema checks required Loom agent fields
func validateLoomSchema(config map[string]interface{}) []string {
    issues := []string{}

    required := []string{"name", "system_prompt", "llm_config"}
    for _, field := range required {
        if _, ok := config[field]; !ok {
            issues = append(issues, fmt.Sprintf(
                "Missing required Loom field: %s", field))
        }
    }

    return issues
}

// validateAntiPatterns checks for role-prompting and other anti-patterns
func validateAntiPatterns(config map[string]interface{}) []string {
    issues := []string{}

    systemPrompt, ok := config["system_prompt"].(string)
    if !ok {
        return issues
    }

    rolePatterns := []string{
        `(?i)you\s+are\s+a\s+`,
        `(?i)as\s+a\s+`,
        `(?i)act\s+as\s+`,
    }

    for _, pattern := range rolePatterns {
        if matched, _ := regexp.MatchString(pattern, systemPrompt); matched {
            issues = append(issues,
                "System prompt contains role-prompting (anti-pattern). Use task-oriented prompts instead.")
            break
        }
    }

    return issues
}

// validateToolReferences checks selected tools are in config
func validateToolReferences(config map[string]interface{}, toolSel *agents.ToolSelection) []string {
    issues := []string{}

    if len(toolSel.BuiltinTools) == 0 && len(toolSel.MCPTools) == 0 {
        return issues
    }

    toolsSection, ok := config["tools"]
    if !ok {
        issues = append(issues, fmt.Sprintf(
            "Tools selected (%d builtin, %d MCP) but not included in config",
            len(toolSel.BuiltinTools), len(toolSel.MCPTools)))
    }

    return issues
}

// validatePatternReferences checks selected patterns exist
func validatePatternReferences(config map[string]interface{}, patternSel *agents.PatternSelection, library *patterns.Library) []string {
    issues := []string{}

    if len(patternSel.SelectedPatterns) == 0 {
        return issues
    }

    // Check patterns exist in library
    if library != nil {
        for _, patternName := range patternSel.SelectedPatterns {
            if _, err := library.Load(patternName); err != nil {
                issues = append(issues, fmt.Sprintf(
                    "Pattern '%s' does not exist in library", patternName))
            }
        }
    }

    return issues
}
```

---

## Why This Won't Break Other Use Cases

### 1. **Scoped to Weaver Context**
- `validateWithJudges` is ONLY called in `coordinator.go:566`
- Function is a **private method** of `WeaverCoordinator`
- No other code can call it

### 2. **Additive Pattern**
- New checks only **append** to `issues` slice
- Existing checks run **unchanged**
- Same error handling logic

### 3. **Validation Scope**
- All new checks are **weaver-specific**:
  - "Is this a Loom agent config?" (weaver generates agents)
  - "Does system_prompt have role-prompting?" (weaver generates prompts)
  - "Are selected tools referenced?" (weaver selects tools)

### 4. **Future-Proof**
- If someone wants to reuse validation logic, they can:
  - Call individual helper functions (`detectStandaloneCode`, etc.)
  - Not call `validateWithJudges` directly (it's weaver-specific)

---

## Alternative: Rename for Clarity (Optional)

If we want to make it OBVIOUS this is weaver-specific:

```go
// Before
func (wc *WeaverCoordinator) validateWithJudges(...)

// After
func (wc *WeaverCoordinator) validateAgentGeneration(...)
```

**Pros**:
- Clear that it's agent generation validation
- No confusion with real judge system

**Cons**:
- Requires updating one call site (line 566)
- Legacy name made sense (validates like a judge would)

**Recommendation**: Keep name, add clear comment

---

## Testing Strategy

### Unit Tests (Ensure Nothing Breaks)

```go
// Test existing validations still work
func TestValidateWithJudges_MCPHallucination(t *testing.T)
func TestValidateWithJudges_PhysicalRequest(t *testing.T)
func TestValidateWithJudges_NoTools(t *testing.T)

// Test new validations work
func TestValidateWithJudges_StandaloneCode(t *testing.T)
func TestValidateWithJudges_MissingLoomFields(t *testing.T)
func TestValidateWithJudges_RolePrompting(t *testing.T)
func TestValidateWithJudges_ToolReferences(t *testing.T)

// Test combined scenarios
func TestValidateWithJudges_MultipleIssues(t *testing.T)
func TestValidateWithJudges_ValidConfig(t *testing.T)
```

### Integration Test

```go
func TestWeaverGenerateAgent_ValidatesOutput(t *testing.T) {
    // Generate agent with bad ConfigAssembler
    result := weaver.GenerateAgent("weather agent")

    // Should fail validation
    assert.Error(t, result.Error)
    assert.Contains(t, result.Error.Error(), "standalone code")
}
```

---

## Implementation Checklist

### Phase 1: Preparation
- [x] Analyze current validation logic
- [x] Identify call sites (only 1)
- [x] Design additive approach
- [ ] Write comprehensive tests for existing validations

### Phase 2: Add New Validators
- [ ] Implement `detectStandaloneCode()`
- [ ] Implement `validateLoomSchema()`
- [ ] Implement `validateAntiPatterns()`
- [ ] Implement `validateToolReferences()`
- [ ] Implement `validatePatternReferences()`

### Phase 3: Integration
- [ ] Add new validations to `validateWithJudges()`
- [ ] Update function comment to clarify scope
- [ ] Run all existing tests (ensure no regressions)
- [ ] Add new tests for new validations

### Phase 4: Validation
- [ ] Test with known bad cases (standalone Python)
- [ ] Test with known good cases (valid Loom agents)
- [ ] Test with edge cases (partial configs, empty fields)
- [ ] Verify no performance degradation

---

## Success Criteria

✅ **All existing validations still work** (MCP, physical, YAML, tools)
✅ **New validations catch standalone code** (Python, JS, Go, etc.)
✅ **New validations catch schema issues** (missing fields)
✅ **New validations catch anti-patterns** (role-prompting)
✅ **No breaking changes** to other code
✅ **Tests pass** with race detector
✅ **Clear documentation** of validation scope

---

## Conclusion

**Safe to proceed** because:
1. Function is weaver-specific (only 1 call site)
2. Additive pattern preserves existing behavior
3. New validations are clearly scoped to agent generation
4. Comprehensive testing ensures no regressions

**No risk** of breaking other use cases because this validation is already isolated to the weaver context.
