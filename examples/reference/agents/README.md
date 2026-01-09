# Ephemeral Thread Policies

This directory demonstrates **declarative ephemeral thread spawning** - a key differentiator from traditional DAG-based orchestration.

**Note:** Configuration field `ephemeral_agents:` will change to `ephemeral_threads:` in v0.6.0, but functionality remains the same.

## Loom vs Traditional DAG Frameworks

### Traditional DAG (Airflow/Temporal)

```python
# ALL tasks predefined, whether they execute or not
with DAG('code_review') as dag:
    reviewer_1 = PythonOperator(task_id='reviewer_1', ...)
    reviewer_2 = PythonOperator(task_id='reviewer_2', ...)
    reviewer_3 = PythonOperator(task_id='reviewer_3', ...)
    reviewer_4 = PythonOperator(task_id='reviewer_4', ...)
    reviewer_5 = PythonOperator(task_id='reviewer_5', ...)
    
    # Judge ALWAYS defined, conditionally executed
    judge = PythonOperator(task_id='judge', ...)
    
    # Static branching logic
    check_consensus = BranchPythonOperator(
        task_id='check_consensus',
        python_callable=lambda: 'judge' if consensus < 0.67 else 'end'
    )
    
    [reviewer_1, reviewer_2, reviewer_3, reviewer_4, reviewer_5] >> check_consensus
    check_consensus >> [judge, end]
```

**Problems:**
- Fixed infrastructure (5 reviewers + judge always allocated)
- Static branching logic (can't adapt)
- No cost optimization (can't use cheaper models conditionally)
- Judge decision logic hardcoded in Python

### Loom (Thread-Driven)

```yaml
# swarm-coordinator.yaml
apiVersion: loom/v1
kind: Agent  # Will be 'Thread' in v0.6.0
spec:
  ephemeral_agents:  # Will be 'ephemeral_threads' in v0.6.0
    - role: judge
      trigger:
        type: CONSENSUS_NOT_REACHED
        threshold: 0.67  # Thread config decides when to spawn
      template:
        llm:
          model: claude-sonnet-4  # Can use different model
          temperature: 0.5  # Different params for judgment
      max_spawns: 1
      cost_limit_usd: 0.50
```

**Advantages:**
1. **Pay-per-use**: Judge thread only spawns if needed (saves ~70% of runs)
2. **Thread-driven**: Swarm intelligence decides when escalation is needed
3. **Cost optimization**: Use cheaper models for reviewer threads, expensive for judge
4. **Self-documenting**: Config shows exactly what might happen
5. **Easy iteration**: Test different strategies by editing YAML

## Example: Cost Comparison

### Code Review Scenario
- 5 reviewers vote on PR approval
- Judge needed 30% of the time

**Traditional DAG:**
```
Fixed cost per run:
  5 reviewers  @ $0.10 each = $0.50
  1 judge      @ $0.20      = $0.20  (even if unused 70% of runs)
  Total: $0.70/run
```

**Loom Ephemeral:**
```
Dynamic cost per run:
  5 reviewers  @ $0.10 each = $0.50
  1 judge      @ $0.20      = $0.20  (only 30% of runs)
  Average: $0.50 + (0.30 * $0.20) = $0.56/run  (20% savings)
```

**With model optimization:**
```
Loom with cheaper reviewers:
  5 reviewers  @ $0.03 each = $0.15  (Sonnet 4)
  1 judge      @ $0.60      = $0.60  (Opus 4 for hard cases)
  Average: $0.15 + (0.30 * $0.60) = $0.33/run  (53% savings!)
```

## Running Examples

See examples/04-collaboration/ for working code.

## Available Agent Configurations

This directory contains example agent configurations:

### `code_reviewer.yaml`
Expert code reviewer for quality, maintainability, and best practices. Provides structured reviews with severity levels and actionable feedback.

### `security_analyst.yaml`
Security-focused agent for vulnerability analysis, threat modeling, and secure coding practices.

### `sql_expert.yaml`
SQL query expert for optimization, debugging, and database design.

### `swarm-coordinator.yaml`
Demonstrates ephemeral thread spawning with dynamic agent allocation.

### `presentation_agent.yaml`
**NEW (v2.0)** - Data visualization and report generation agent following the Hawk StyleGuide design system.

**Features:**
- **Dynamic StyleGuide loading** - Reads `StyleGuide.tsx` via `file_read` for latest design specs
- ECharts visualization configuration
- Interactive report generation
- Terminal aesthetic with glass morphism
- Support for 12+ chart types (bar, line, pie, scatter, heatmap, sankey, etc.)

**Tools:**
- `file_read`: Read data files (JSON, CSV, etc.)
- `file_write`: Write reports and output files
- `top_n_query`: Top N aggregation from shared memory
- `group_by_query`: GROUP BY aggregation
- `generate_visualization`: Interactive HTML report with ECharts
- `generate_workflow_visualization`: Workflow pipeline visualization

**Usage:**
```bash
# Start server with presentation agent
looms serve --agents-dir ./examples/reference/agents

# Or use with workflow
looms workflow run ./examples/visualization/workflow-v3.5-visualization-demo.yaml
```

**Design System Reference:**
The agent includes comprehensive knowledge of the Hawk StyleGuide:
- **Color Palette**: Teradata Orange (#F37021), semantic colors (success, error, warning, info)
- **Typography**: IBM Plex Mono for data, Inter for body text
- **Design Principles**: Glass morphism, terminal aesthetic, purposeful motion
- **Chart Recommendations**: Best practices for each visualization type
