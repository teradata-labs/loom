# Judge Functionality Migration from Hawk to Loom

**Date:** 2026-01-13
**Branch:** `judge-integration`
**Status:** ✅ Complete

## Summary

Successfully migrated Hawk's judge functionality directly into Loom, eliminating the external dependency.

## What Changed

### Files Migrated
- `hawk/pkg/core/judge/judge.go` → `loom/pkg/evals/judges/llm/judge.go`
- `hawk/pkg/core/judge/judge_promptio.go` → `loom/pkg/evals/judges/llm/judge_promptio.go`
- `hawk/pkg/core/judge/judge_nopromptio.go` → `loom/pkg/evals/judges/llm/judge_nopromptio.go`
- Test files included

### Renamed Components
- `HawkJudge` → `LLMJudge` (more accurate name)
- `NewHawkJudge()` → `NewLLMJudge()`
- `JudgeEvalRun(core.EvalRun)` → `Judge(Evidence)` (simpler API)

### Removed
- Hawk dependency from `go.mod`
- `//go:build hawk` tags (judge now always available)
- `VerdictToJudgeVerdict()` converter (no longer needed)
- `pkg/evals/hawk_export_stub.go` (no longer needed)

### Updated
- **go.mod:** Removed `github.com/teradata-labs/hawk` dependency
- **Justfile:** Updated build targets - judge is now built-in
- **All references:** `NewHawkJudge` → `NewLLMJudge` across codebase

## Impact

### Before Migration
```go
// Required -tags hawk to build
// Depended on github.com/teradata-labs/hawk (unpublished)

import hawkjudge "github.com/teradata-labs/hawk/pkg/core/judge"

judge, err := judges.NewHawkJudge(llmProvider, config, tracer)
```

### After Migration
```go
// Always available, no build tags needed
// No external dependencies (except optional Promptio)

import llmjudge "github.com/teradata-labs/loom/pkg/evals/judges/llm"

judge, err := judges.NewLLMJudge(llmProvider, config, tracer)
```

## Build System Changes

### Justfile Updates

**Before:**
```bash
just build       # No judge (required -tags hawk)
just build-hawk  # With judge
just build-full  # Hawk + Promptio
```

**After:**
```bash
just build       # Judge included by default!
just build-full  # Judge + Promptio
```

## Architecture

**New Structure:**
```
loom/pkg/evals/judges/
├── judge.go              # Judge interface
├── orchestrator.go       # Multi-judge orchestration
├── llm/                  # LLM judge implementation (from Hawk)
│   ├── judge.go
│   ├── judge_promptio.go
│   └── judge_nopromptio.go
└── [other judge types]
```

## Benefits

✅ **No external dependencies** - Loom is self-contained
✅ **Always available** - No build tags required
✅ **Simpler for users** - `just build` works out of the box
✅ **Faster iteration** - Loom can improve judge independently
✅ **Clearer ownership** - Loom owns evaluation, Hawk owns observability
✅ **No publish blockers** - Loom doesn't wait for Hawk releases

## What Still Works

- ✅ LLM-based evaluation (core functionality)
- ✅ Multi-judge orchestration (Loom's advanced features)
- ✅ Promptio integration (still optional with -tags promptio)
- ✅ All aggregation strategies (6 types)
- ✅ Circuit breakers and retry logic
- ✅ Streaming evaluation
- ✅ Agent-as-judge (Loom's unique feature)

## Known Issues

~~### Test Files Need Minor Updates~~ ✅ **FIXED**

~~**Issue:** Test files use old Hawk API~~ **RESOLVED**

**Fix Applied (Commit 5e07d28):**
- Updated all test files to use new `Judge(Evidence)` API
- Removed references to `JudgeEvalRun(core.EvalRun)`
- Removed tests for deleted `VerdictToJudgeVerdict` function
- Removed `EvalRunID` field references
- Added `//go:build !promptio` tag to `judge_mock_test.go`
- All tests now pass: `go test -tags fts5 ./pkg/evals/judges/llm/... -v` ✅

## Testing

### What Works
```bash
# Build main binary
cd ~/Projects/loom-public
just build

# Judge commands now available
./bin/looms judge evaluate --help
```

### What's Verified
```bash
# Tests pass successfully
go test -tags fts5 ./pkg/evals/judges/llm/... -v  # ✅ PASS (14 tests)

# Binary builds successfully
go build -tags fts5 -o bin/looms ./cmd/looms  # ✅ Success
```

## Next Steps

1. ✅ **Migration complete** - Code committed to `judge-integration` branch
2. ✅ **Update test files** - All tests now pass
3. ⚠️ **Update documentation** - README, BUILD_TAGS.md
4. 📝 **Verify end-to-end** - Run actual judge evaluation
5. 🚀 **Merge to main** - Ready when approved

## Rollback Plan

If needed, revert the commit:
```bash
git checkout main
git branch -D judge-integration
```

Or keep branch and fix incrementally:
```bash
git checkout judge-integration
# Fix test files
git add -A && git commit -m "fix: update test files to use new Judge API"
```

## Documentation Updates Needed

### README.md
- Remove "requires Hawk" notes
- Update "Judge Evaluation System" section
- Note that judge is now built-in

### BUILD_TAGS.md
- Remove `hawk` build tag section
- Update examples to show judge always available
- Keep `promptio` section (still optional)

## For Hawk Project

**Recommendation:** Remove judge from Hawk, or document that Loom owns evaluation.

**Hawk should focus on:**
- Telemetry (traces, sessions, metrics)
- Observability dashboards
- Multi-source telemetry adapters

**Loom owns:**
- Agent framework
- Evaluation and judging
- Pattern-guided learning

## Commit Message

```
feat: migrate Hawk judge functionality into Loom

BREAKING CHANGE: Judge functionality is now built-in to Loom

- Moved Hawk pkg/core/judge to Loom pkg/evals/judges/llm/
- Renamed HawkJudge to LLMJudge for clarity
- Removed Hawk dependency from go.mod
- Removed //go:build hawk tags - judges now always available
- Updated Justfile to reflect judge is built-in
- Judge uses simple LLM-based evaluation (no external dependencies)
- Promptio still optional for YAML prompt templates

Technical changes:
- Changed JudgeEvalRun(core.EvalRun) to Judge(Evidence)
- Removed VerdictToJudgeVerdict converter (no longer needed)
- Updated all NewHawkJudge calls to NewLLMJudge
- Package renamed from 'judge' to 'llm'

Status: Migration complete, test files need minor updates
```

## Questions?

Contact the team or see the diff:
```bash
git diff main..judge-integration
```
