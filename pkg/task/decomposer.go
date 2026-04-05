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

package task

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"go.uber.org/zap"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/types"
)

// Default retry settings for decomposition output validation.
const (
	defaultDecomposeMaxRetries = 2
	defaultDecomposeMaxDepth   = 3
)

// Decomposer uses an LLM to break down a high-level goal into a dependency DAG.
type Decomposer struct {
	manager *Manager
	tracer  observability.Tracer
	logger  *zap.Logger
}

// NewDecomposer creates a new LLM-assisted task decomposer.
func NewDecomposer(manager *Manager, tracer observability.Tracer, logger *zap.Logger) *Decomposer {
	if tracer == nil {
		tracer = observability.NewNoOpTracer()
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Decomposer{manager: manager, tracer: tracer, logger: logger}
}

// DecomposeRequest contains the parameters for task decomposition.
type DecomposeRequest struct {
	Goal       string
	Context    string
	BoardID    string
	ParentTask *Task // optional: decompose under this parent
	MaxDepth   int
	Strategy   loomv1.DecomposeStrategy
	AgentID    string
}

// DecomposeResponse contains the decomposition results.
type DecomposeResponse struct {
	Tasks        []*Task
	Dependencies []*TaskDependency
	Reasoning    string
}

// Decompose uses an LLM to break a goal into a dependency DAG of tasks,
// creates them in the store, and wires up dependencies.
func (d *Decomposer) Decompose(ctx context.Context, llm types.LLMProvider, req *DecomposeRequest) (*DecomposeResponse, error) {
	ctx, span := d.tracer.StartSpan(ctx, "task_decomposer.decompose")
	defer d.tracer.EndSpan(span)

	if req.MaxDepth <= 0 {
		req.MaxDepth = defaultDecomposeMaxDepth
	}

	prompt := d.buildPrompt(req)

	// Call LLM with inline retry for JSON validation.
	raw, err := d.callWithRetry(ctx, llm, prompt, defaultDecomposeMaxRetries)
	if err != nil {
		return nil, fmt.Errorf("decompose LLM call failed: %w", err)
	}

	// Parse the structured output.
	parsed, err := parseDecomposeOutput(raw)
	if err != nil {
		return nil, fmt.Errorf("decompose parse failed: %w", err)
	}

	// Create tasks and dependencies in the store.
	resp, err := d.materialize(ctx, req, parsed)
	if err != nil {
		return nil, fmt.Errorf("decompose materialize failed: %w", err)
	}

	return resp, nil
}

// =============================================================================
// Prompt Building
// =============================================================================

func (d *Decomposer) buildPrompt(req *DecomposeRequest) string {
	var b strings.Builder

	// Preamble
	b.WriteString("Break down the following goal into a dependency graph of tasks.\n")
	b.WriteString("Each task is a unit of cognitive work — not limited to code.\n")
	b.WriteString("Tasks can be research, analysis, writing, decisions, reviews, investigations, or any other work.\n\n")

	b.WriteString(fmt.Sprintf("Goal: %s\n", req.Goal))
	if req.Context != "" {
		b.WriteString(fmt.Sprintf("Context: %s\n", req.Context))
	}
	if req.ParentTask != nil {
		b.WriteString(fmt.Sprintf("Parent task: %s (%s)\n", req.ParentTask.Title, req.ParentTask.Objective))
		b.WriteString("This decomposition creates subtasks under the parent.\n")
	}
	b.WriteString("\n")

	// Strategy-specific instructions
	switch req.Strategy {
	case loomv1.DecomposeStrategy_DECOMPOSE_STRATEGY_BACKWARD:
		b.WriteString("Work BACKWARD from the goal. Start with the end state and ask:\n")
		b.WriteString("\"What must be true immediately before this can be achieved?\"\n")
		b.WriteString("Then for each prerequisite, ask the same question recursively.\n")
		b.WriteString("Stop when you reach tasks that can begin immediately with no prerequisites.\n\n")
		b.WriteString("This produces a DAG where leaf nodes are the first things to work on,\n")
		b.WriteString("and the root is the final deliverable.\n\n")
	case loomv1.DecomposeStrategy_DECOMPOSE_STRATEGY_FORWARD:
		b.WriteString("Work FORWARD from the current state. Plan sequentially:\n")
		b.WriteString("\"What is the first thing to do? Then what? Then what?\"\n")
		b.WriteString("Create a mostly-linear chain with occasional parallel branches\n")
		b.WriteString("where independent work can happen simultaneously.\n\n")
		b.WriteString("This produces a pipeline-like structure with clear ordering.\n\n")
	case loomv1.DecomposeStrategy_DECOMPOSE_STRATEGY_PARALLEL:
		b.WriteString("Maximize parallelism. Identify all work that can begin immediately\n")
		b.WriteString("with no dependencies on other tasks in this decomposition.\n")
		b.WriteString("Group dependent work into the fewest sequential layers possible.\n\n")
		b.WriteString("This produces a wide, shallow DAG optimized for multiple agents\n")
		b.WriteString("working concurrently.\n\n")
	default:
		// Default to backward (the beads model)
		b.WriteString("Work BACKWARD from the goal to identify prerequisites recursively.\n\n")
	}

	// Output format
	b.WriteString("For each task provide:\n")
	b.WriteString("- title: short name (< 120 chars)\n")
	b.WriteString("- description: what needs to be done\n")
	b.WriteString("- objective: what \"done\" looks like\n")
	b.WriteString("- acceptance_criteria: how to verify completion\n")
	b.WriteString("- category: one of [research, analysis, implementation, review, writing, decision, investigation, planning]\n")
	b.WriteString("- priority: P0 (critical) through P4 (backlog)\n")
	b.WriteString("- estimated_effort: freeform (e.g., \"5 min\", \"1 hour\", \"multi-session\")\n")
	b.WriteString("- depends_on: list of task indices (0-based) this task depends on (BLOCKS dependency)\n")
	b.WriteString("- tags: freeform labels for classification\n\n")

	b.WriteString("Output valid JSON:\n")
	b.WriteString(`{
  "tasks": [
    {
      "index": 0,
      "title": "...",
      "description": "...",
      "objective": "...",
      "acceptance_criteria": "...",
      "category": "research",
      "priority": "P2",
      "estimated_effort": "30 min",
      "depends_on": [],
      "tags": ["exploration"]
    }
  ],
  "reasoning": "Brief explanation of the decomposition strategy and key decisions."
}`)
	b.WriteString("\n\n")

	b.WriteString(fmt.Sprintf("Rules:\n"))
	b.WriteString(fmt.Sprintf("- Keep decomposition depth within %d levels\n", req.MaxDepth))
	b.WriteString("- Each task must have a clear, verifiable objective\n")
	b.WriteString("- Dependencies must be acyclic (no circular references)\n")
	b.WriteString("- Use depends_on indices, not names\n")
	b.WriteString("- Leaf tasks (no dependencies) should be immediately actionable\n")
	b.WriteString("- Output ONLY the JSON object, no markdown fences or extra text\n")

	return b.String()
}

// =============================================================================
// LLM Call with Retry
// =============================================================================

// callWithRetry calls the LLM and retries with feedback if JSON parsing fails.
// Uses CONTINUE mode: appends validation error to the same conversation.
func (d *Decomposer) callWithRetry(ctx context.Context, llm types.LLMProvider, prompt string, maxRetries int) (string, error) {
	messages := []types.Message{
		{Role: "user", Content: prompt},
	}

	for attempt := 0; attempt <= maxRetries; attempt++ {
		resp, err := llm.Chat(ctx, messages, nil)
		if err != nil {
			return "", fmt.Errorf("LLM call failed (attempt %d): %w", attempt+1, err)
		}

		raw := resp.Content

		// Try parsing — if valid, we're done.
		if _, err := parseDecomposeOutput(raw); err == nil {
			return raw, nil
		} else if attempt < maxRetries {
			// Append assistant response + validation feedback for retry (CONTINUE mode).
			d.logger.Debug("decompose output invalid, retrying",
				zap.Int("attempt", attempt+1),
				zap.Error(err))

			messages = append(messages,
				types.Message{Role: "assistant", Content: raw},
				types.Message{Role: "user", Content: fmt.Sprintf(
					"Your output was not valid JSON. Error: %s\n\n"+
						"Output ONLY a valid JSON object matching the schema above. "+
						"No markdown fences, no extra text before or after the JSON.",
					err.Error(),
				)},
			)
		} else {
			return "", fmt.Errorf("decompose failed after %d attempts, last error: %w", maxRetries+1, err)
		}
	}

	return "", fmt.Errorf("decompose: unreachable")
}

// =============================================================================
// Output Parsing
// =============================================================================

// decomposeOutput is the JSON structure returned by the LLM.
type decomposeOutput struct {
	Tasks     []decomposeTaskOutput `json:"tasks"`
	Reasoning string                `json:"reasoning"`
}

type decomposeTaskOutput struct {
	Index              int      `json:"index"`
	Title              string   `json:"title"`
	Description        string   `json:"description"`
	Objective          string   `json:"objective"`
	AcceptanceCriteria string   `json:"acceptance_criteria"`
	Category           string   `json:"category"`
	Priority           string   `json:"priority"`
	EstimatedEffort    string   `json:"estimated_effort"`
	DependsOn          []int    `json:"depends_on"`
	Tags               []string `json:"tags"`
}

// parseDecomposeOutput extracts structured tasks from LLM output.
// Handles common LLM quirks: markdown fences, leading/trailing text.
func parseDecomposeOutput(raw string) (*decomposeOutput, error) {
	// Strip markdown code fences if present.
	cleaned := stripMarkdownFences(raw)

	var output decomposeOutput
	if err := json.Unmarshal([]byte(cleaned), &output); err != nil {
		return nil, fmt.Errorf("JSON parse error: %w (first 200 chars: %s)", err, truncate(cleaned, 200))
	}

	if len(output.Tasks) == 0 {
		return nil, fmt.Errorf("decomposition produced zero tasks")
	}

	// Validate dependency indices.
	for _, t := range output.Tasks {
		for _, dep := range t.DependsOn {
			if dep < 0 || dep >= len(output.Tasks) {
				return nil, fmt.Errorf("task %d (%q) has invalid depends_on index %d (max: %d)",
					t.Index, t.Title, dep, len(output.Tasks)-1)
			}
			if dep == t.Index {
				return nil, fmt.Errorf("task %d (%q) depends on itself", t.Index, t.Title)
			}
		}
	}

	return &output, nil
}

// stripMarkdownFences removes ```json ... ``` wrappers.
func stripMarkdownFences(s string) string {
	s = strings.TrimSpace(s)

	// Remove leading ```json or ```
	if strings.HasPrefix(s, "```json") {
		s = strings.TrimPrefix(s, "```json")
	} else if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```")
	}

	// Remove trailing ```
	if strings.HasSuffix(s, "```") {
		s = strings.TrimSuffix(s, "```")
	}

	return strings.TrimSpace(s)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// =============================================================================
// Materialization (create tasks + deps in store)
// =============================================================================

// materialize creates the decomposed tasks and dependencies in the store.
func (d *Decomposer) materialize(ctx context.Context, req *DecomposeRequest, parsed *decomposeOutput) (*DecomposeResponse, error) {
	resp := &DecomposeResponse{
		Reasoning: parsed.Reasoning,
	}

	// Create tasks (index → task ID mapping for dependency wiring).
	taskIDs := make(map[int]string, len(parsed.Tasks))

	for _, pt := range parsed.Tasks {
		t := &Task{
			Title:              pt.Title,
			Description:        pt.Description,
			Objective:          pt.Objective,
			AcceptanceCriteria: pt.AcceptanceCriteria,
			Category:           parseCategory(pt.Category),
			Priority:           parsePriority(pt.Priority),
			EstimatedEffort:    pt.EstimatedEffort,
			Tags:               pt.Tags,
			Status:             loomv1.TaskStatus_TASK_STATUS_OPEN,
			OwnerAgentID:       req.AgentID,
			BoardID:            req.BoardID,
		}

		if req.ParentTask != nil {
			t.ParentID = req.ParentTask.ID
		}

		created, err := d.manager.CreateTask(ctx, t)
		if err != nil {
			return nil, fmt.Errorf("create task %d (%q): %w", pt.Index, pt.Title, err)
		}

		taskIDs[pt.Index] = created.ID
		resp.Tasks = append(resp.Tasks, created)
	}

	// Wire dependencies using the index→ID map.
	for _, pt := range parsed.Tasks {
		for _, depIdx := range pt.DependsOn {
			dep := &TaskDependency{
				FromTaskID: taskIDs[pt.Index],
				ToTaskID:   taskIDs[depIdx],
				Type:       loomv1.TaskDependencyType_TASK_DEPENDENCY_TYPE_BLOCKS,
				CreatedBy:  req.AgentID,
			}

			if err := d.manager.AddDependency(ctx, dep); err != nil {
				return nil, fmt.Errorf("add dependency %d→%d: %w", pt.Index, depIdx, err)
			}

			resp.Dependencies = append(resp.Dependencies, dep)
		}
	}

	return resp, nil
}

// =============================================================================
// Enum Parsing
// =============================================================================

func parseCategory(s string) loomv1.TaskCategory {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "research":
		return loomv1.TaskCategory_TASK_CATEGORY_RESEARCH
	case "analysis":
		return loomv1.TaskCategory_TASK_CATEGORY_ANALYSIS
	case "implementation":
		return loomv1.TaskCategory_TASK_CATEGORY_IMPLEMENTATION
	case "review":
		return loomv1.TaskCategory_TASK_CATEGORY_REVIEW
	case "writing":
		return loomv1.TaskCategory_TASK_CATEGORY_WRITING
	case "decision":
		return loomv1.TaskCategory_TASK_CATEGORY_DECISION
	case "investigation":
		return loomv1.TaskCategory_TASK_CATEGORY_INVESTIGATION
	case "planning":
		return loomv1.TaskCategory_TASK_CATEGORY_PLANNING
	default:
		return loomv1.TaskCategory_TASK_CATEGORY_OTHER
	}
}

func parsePriority(s string) loomv1.TaskPriority {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "P0", "CRITICAL":
		return loomv1.TaskPriority_TASK_PRIORITY_CRITICAL
	case "P1", "HIGH":
		return loomv1.TaskPriority_TASK_PRIORITY_HIGH
	case "P2", "MEDIUM":
		return loomv1.TaskPriority_TASK_PRIORITY_MEDIUM
	case "P3", "LOW":
		return loomv1.TaskPriority_TASK_PRIORITY_LOW
	case "P4", "BACKLOG":
		return loomv1.TaskPriority_TASK_PRIORITY_BACKLOG
	default:
		return loomv1.TaskPriority_TASK_PRIORITY_MEDIUM
	}
}
