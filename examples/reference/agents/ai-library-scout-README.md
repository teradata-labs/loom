# AI Library Scout Agent

**Reference implementation demonstrating structured task execution and verification patterns**

## Overview

The AI Library Scout is a specialized research agent that discovers and evaluates new AI/ML libraries on GitHub for potential Loom integration. It demonstrates best practices for:

- **Context gathering** before searching (prevents wasted effort)
- **Structured task execution** (using `patterns/agent/structured-task.yaml`)
- **3-level verification** (using `patterns/agent/verification-protocol.yaml`)
- **Systematic evaluation** (integration effort, value proposition, risks)

## Key Features

### 1. Context-Driven Search

The agent **always starts** by gathering context through questions:
- Focus area (LLM providers, agent frameworks, vector DBs, tools, etc.)
- Technology stack (Go preferred, Python, TypeScript, etc.)
- Maturity level (production-ready, beta, experimental)
- Integration complexity tolerance
- Time range for recency

This ensures focused, relevant results instead of overwhelming the user.

### 2. Structured Task Format

Uses the **structured_task_execution** pattern to plan searches:

```
Task: Search for [focus_area] AI libraries on GitHub

Name: Discover [category] libraries for Loom integration

Search Strategy:
- GitHub queries with filters
- Star count thresholds
- Recent activity checks
- Language/topic filters

Evaluation Criteria:
- ✓ Active development
- ✓ Good documentation
- ✓ Community adoption
- ✓ License compatibility
- ✓ Integration feasibility
```

### 3. Three-Level Verification

Applies the **verification_protocol** pattern to validate findings:

**Level 1: Syntactic** - Repository structure, README, license
**Level 2: Quality** - Documentation, tests, issue management, community
**Level 3: Integration** - API/SDK, examples, compatibility with Loom

### 4. Integration Assessment

Evaluates each library for integration effort:
- **Easy** (⭐⭐⭐⭐⭐): Go SDK, 1-2 days
- **Moderate** (⭐⭐⭐): Python/TS SDK, 3-5 days
- **Complex** (⭐): No SDK, 1-2 weeks

### 5. Structured Reports

Generates comprehensive reports with:
- Top recommendations (3-5 libraries)
- Integration effort estimates
- Verification results
- Next steps checklist
- Also-considered (lower priority)

## Usage

### Start the Agent

```bash
# Option 1: Add to looms.yaml agents section
agents:
  agents:
    ai-library-scout:
      name: AI Library Scout
      description: Discovers new AI libraries on GitHub
      backend_path: ./examples/reference/agents/ai-library-scout.yaml

# Option 2: Use directly with loom CLI
loom chat ai-library-scout
```

### Example Session

```bash
$ loom chat ai-library-scout

Agent: Hello! I'm the AI Library Scout for Loom. I help discover and evaluate
new AI/ML libraries on GitHub that could enhance Loom's capabilities.

Let me start by understanding what you're looking for:

1. **Focus Area**: What type of libraries interest you?
   - LLM providers (new APIs, models)
   - Agent frameworks (orchestration, multi-agent)
   - Vector databases (embeddings, search)
   - Tools & plugins (extending capabilities)
   - Observability (tracing, monitoring)
   - MCP servers (Model Context Protocol)
   - Other (please specify)

User: I'm interested in new LLM providers with Go support

Agent: Great! A few follow-up questions to narrow the search:

2. **Specific needs**: Are you looking for:
   - OpenAI alternatives (cost/features)
   - Local/open-source models (privacy)
   - Specialized models (code, reasoning)
   - Enterprise providers (SLAs, support)

User: OpenAI alternatives with better pricing

Agent: Perfect! Two more questions:

3. **API compatibility**: Preference for:
   - OpenAI-compatible API (drop-in replacement)
   - Native Go SDK (better features)
   - Either is fine

4. **Maturity**: Development stage?
   - Production-ready only
   - Beta/RC acceptable
   - Experimental is fine

User: OpenAI-compatible API preferred, production-ready only

Agent: Excellent! I have enough context. Let me search for production-ready
LLM providers with Go support and OpenAI-compatible APIs.

[Creates structured search plan using structured_task_execution pattern]

Task: Search for OpenAI-compatible LLM providers with Go support

Name: Discover production-ready LLM provider alternatives

Search Strategy:
- GitHub search: language:go "OpenAI compatible" OR "OpenAI API" stars:>200
- Filter: Last 6 months activity, production-ready tags
- Focus: Cost-effective alternatives to OpenAI

Evaluation Criteria:
- ✓ Go SDK with OpenAI-compatible API
- ✓ Active maintenance (commits in last 3 months)
- ✓ Production-ready (stable releases)
- ✓ Good documentation
- ✓ Community adoption (stars, usage)

[Executes search and finds libraries...]

[Applies verification_protocol to each finding...]

## Top Recommendations

### 1. OpenRouter Go SDK ⭐⭐⭐⭐⭐
**URL**: https://github.com/example/openrouter-go
**Stars**: 450 | **Language**: Go | **License**: MIT

**What it does**: Go client for OpenRouter - access 100+ models through
single OpenAI-compatible API

**Why Loom should consider it**:
- Drop-in replacement for OpenAI client (same interface)
- Access to cheaper models (Llama, Mistral, etc.)
- Fallback/routing between models
- Cost tracking built-in

**Integration Effort**: Low (⭐⭐⭐⭐⭐)
- Go SDK available with full OpenAI compatibility
- Simply swap import and API key
- Examples in repo demonstrate usage

**Verification Results**:
- Syntactic: ✅ Well-documented, clear structure, MIT license
- Quality: ✅ Active (commits this week), 50+ contributors, issues managed
- Integration: ✅ Go SDK, OpenAI-compatible, examples provided

**Next Steps**:
- [ ] Review pricing (check OpenRouter rates)
- [ ] Test SDK with Loom's LLM provider interface
- [ ] Prototype integration
- [ ] Compare performance vs OpenAI direct

[Report continues with 2-3 more recommendations...]

[Agent saves report to workspace as artifact]

Agent: I've completed the search and created a detailed report. The top
recommendation is OpenRouter Go SDK - it offers OpenAI-compatible API with
access to cheaper models. I've saved the full report to your workspace.

Would you like me to:
1. Deep-dive into any specific library?
2. Search for a different category?
3. Help prototype an integration?
```

## Pattern Integration

### How It Uses structured_task_execution

The agent applies the pattern in **Phase 2** (Search Planning):

1. **Name**: Creates clear, action-oriented task name
2. **Search Strategy**: Lists specific files/queries to execute
3. **Evaluation Criteria**: Defines measurable completion criteria
4. **Verification**: Includes steps to verify findings

### How It Uses verification_protocol

The agent applies the pattern in **Phase 4** (Verification):

1. **Level 1 (Syntactic)**: Repository structure, README, license
2. **Level 2 (Quality)**: Documentation, tests, community health
3. **Level 3 (Integration)**: SDK availability, examples, compatibility
4. **Done Criteria**: Checks against evaluation criteria

## Search Query Examples

```bash
# LLM Providers (Go, OpenAI-compatible)
language:go ("OpenAI compatible" OR "OpenAI API") stars:>200 pushed:>2025-01

# Agent Frameworks (Go or Python)
(language:go OR language:python) (agent framework OR orchestration) stars:>300

# Vector Databases (Go SDK)
language:go (vector database OR embeddings) stars:>500 pushed:>2024-06

# MCP Servers (recent)
("Model Context Protocol" OR "MCP server") pushed:>2025-01

# Observability (LLM-specific)
language:go (observability OR tracing) (llm OR agent) stars:>100
```

## Configuration Options

### Required Environment Variables

```bash
# GitHub API access (optional but recommended for higher rate limits)
export GITHUB_TOKEN=ghp_your_token_here
```

### Agent Configuration

```yaml
# In looms.yaml
agents:
  agents:
    ai-library-scout:
      name: AI Library Scout
      description: Discovers AI libraries for Loom integration
      backend_path: ./examples/reference/agents/ai-library-scout.yaml
      max_turns: 30
      max_tool_executions: 50
      enable_tracing: true
```

## Report Format

The agent generates reports in this structure:

```markdown
# AI Library Scout Report
**Date**: 2026-02-15
**Focus Area**: LLM Providers
**Search Criteria**: Go SDK, OpenAI-compatible, production-ready

## Top Recommendations (3-5)
### 1. [Library Name] ⭐⭐⭐⭐⭐
- Description, stars, license
- Why Loom should consider it
- Integration effort
- Verification results
- Next steps

## Also Considered (Lower Priority)
- [Brief list with reasons]

## Search Statistics
- Repositories searched: [count]
- Libraries evaluated: [count]
- Top recommendations: [count]
```

## Tips for Best Results

### DO:
- ✅ Answer all context-gathering questions thoroughly
- ✅ Be specific about requirements (e.g., "Go SDK required")
- ✅ Indicate priority (cost vs features vs ease-of-use)
- ✅ Review the full report (includes lower-priority options)
- ✅ Check verification results (3-level validation)

### DON'T:
- ❌ Skip context gathering (leads to irrelevant results)
- ❌ Request too broad a search (e.g., "find me anything AI-related")
- ❌ Ignore integration effort estimates
- ❌ Overlook license compatibility
- ❌ Rush without verification

## Customization

### Modify Search Criteria

Edit the `system_prompt` section to adjust:
- Star count thresholds (currently 100-500+ depending on category)
- Activity requirements (currently 3-6 months)
- Language priorities (currently Go > Python > TypeScript)
- Maturity expectations (currently production-ready preferred)

### Add New Focus Areas

Add new search patterns in the `Search Patterns by Focus Area` section:

```yaml
### [New Category]
- Search: "[keywords]"
- Filters: [language, stars, topics]
- Look for: [specific features]
```

## Related Patterns

This agent demonstrates integration of:
- `patterns/agent/structured-task.yaml` - Task structuring
- `patterns/agent/verification-protocol.yaml` - 3-level verification

See these patterns for detailed methodology.

## Contributing

To improve the AI Library Scout:

1. **Add new focus areas**: Edit search patterns
2. **Improve evaluation rubric**: Refine integration assessment
3. **Enhance verification**: Add domain-specific checks
4. **Update examples**: Add new search queries

## Support

- **Issues**: Report problems or request features
- **Questions**: Ask in Loom community channels
- **Contributions**: Submit PRs to improve the agent

---

**Note**: This is a reference implementation demonstrating pattern usage. Customize for your specific needs!
