// Copyright 2026 Teradata Corporation
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package llm

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/teradata-labs/loom/pkg/types"
)

// Judge evaluates eval runs using LLM-as-judge
type Judge struct {
	provider  types.LLMProvider
	promptMgr interface{} // *prompt.Manager when built with -tags promptio, nil otherwise
}

// Config holds judge configuration
type Config struct {
	// Provider is the LLM provider to use for judging
	Provider types.LLMProvider
	// PromptsFS is an optional embedded filesystem containing prompt templates
	PromptsFS embed.FS
}

// Verdict represents the evaluation result
type Verdict struct {
	ID                 string
	EvalRunID          string
	JudgeModel         string
	FactualAccuracy    int
	HallucinationScore int
	QueryQuality       int
	Completeness       int
	Verdict            string // PASS|FAIL|PARTIAL
	Reasoning          string
	Issues             []string
	CreatedAt          int64
}

// Evidence contains all information for judging an eval run
type Evidence struct {
	Query         string
	Response      string
	ErrorMessage  string
	Success       bool
	ExecutionTime int64
	Model         string
}

// NewJudge creates a new judge instance
func NewJudge(cfg *Config) (*Judge, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is required")
	}
	if cfg.Provider == nil {
		return nil, fmt.Errorf("LLM provider is required")
	}

	// Create judge instance
	j := &Judge{
		provider: cfg.Provider,
	}

	return j, nil
}

// Judge evaluates evidence using LLM-as-judge
func (j *Judge) Judge(ctx context.Context, evidence *Evidence) (*Verdict, error) {
	// Build judge prompt
	promptText := j.buildJudgePrompt(evidence)

	// Call LLM judge via provider
	messages := []types.Message{
		{Role: "user", Content: promptText},
	}

	response, err := j.provider.Chat(ctx, messages, nil) // No tools needed for judge
	if err != nil {
		return nil, fmt.Errorf("judge LLM call failed: %w", err)
	}

	// Parse verdict
	verdict, err := j.parseJudgeVerdict(response.Content)
	if err != nil {
		return nil, fmt.Errorf("failed to parse verdict: %w", err)
	}

	// Set metadata
	verdict.ID = uuid.New().String()
	verdict.JudgeModel = j.provider.Model()
	verdict.CreatedAt = time.Now().Unix()

	return verdict, nil
}

// buildJudgePrompt constructs the evaluation prompt
// Uses hardcoded prompt template
func (j *Judge) buildJudgePrompt(evidence *Evidence) string {

	// Fall back to hardcoded prompt (always available)
	return j.buildJudgePromptHardcoded(evidence)
}

// buildJudgePromptHardcoded constructs the evaluation prompt (fallback)
func (j *Judge) buildJudgePromptHardcoded(evidence *Evidence) string {
	var sb strings.Builder

	sb.WriteString("Evaluate this AI agent's SQL query execution and response for accuracy and hallucination.\n\n")

	// User query
	sb.WriteString("## USER QUESTION\n")
	sb.WriteString(evidence.Query)
	sb.WriteString("\n\n")

	// Execution result
	sb.WriteString("## EXECUTION RESULT\n")
	if evidence.Success {
		sb.WriteString(fmt.Sprintf("✓ Success (execution time: %dms, model: %s)\n\n", evidence.ExecutionTime, evidence.Model))
	} else {
		sb.WriteString(fmt.Sprintf("✗ Failed (execution time: %dms, model: %s)\n", evidence.ExecutionTime, evidence.Model))
		sb.WriteString(fmt.Sprintf("Error: %s\n\n", evidence.ErrorMessage))
	}

	// Agent's response
	sb.WriteString("## AGENT'S RESPONSE\n")
	if evidence.Response != "" {
		sb.WriteString(evidence.Response)
	} else {
		sb.WriteString("(No response - execution failed)")
	}
	sb.WriteString("\n\n")

	// Evaluation criteria
	sb.WriteString(`## EVALUATION TASK

Evaluate the agent's response on these dimensions:

1. **Factual Accuracy (0-100)**: Are the facts in the agent's response correct?
   - 100 = All facts correct and grounded in query results
   - 50 = Some facts correct, some questionable
   - 0 = Facts are wrong or no factual content

2. **Hallucination Score (0-100)**: Did the agent make up information not present in the data?
   - 0 = No hallucinations, all statements grounded
   - 50 = Some speculation or unfounded claims
   - 100 = Entirely made up information

3. **Query Quality (0-100)**: Was the SQL query execution appropriate?
   - 100 = Optimal execution, produced expected results
   - 50 = Query worked but could be better
   - 0 = Failed execution or inappropriate approach

4. **Completeness (0-100)**: Was the user's question fully answered?
   - 100 = Question fully answered with sufficient detail
   - 50 = Partial answer or missing key information
   - 0 = Question not answered

**Verdict**: Based on the scores, classify as:
- PASS: Factual accuracy ≥80, Hallucination ≤20, Completeness ≥80
- FAIL: Factual accuracy <60, or Hallucination >40, or Completeness <50
- PARTIAL: Everything else

Return ONLY a JSON object with this structure:
{
  "factual_accuracy": <score 0-100>,
  "hallucination_score": <score 0-100>,
  "query_quality": <score 0-100>,
  "completeness": <score 0-100>,
  "verdict": "PASS|FAIL|PARTIAL",
  "reasoning": "<2-3 sentence explanation>",
  "issues": ["<specific problem 1>", "<specific problem 2>"]
}`)

	return sb.String()
}

// parseJudgeVerdict extracts the JSON verdict from LLM response
func (j *Judge) parseJudgeVerdict(response string) (*Verdict, error) {
	// Find JSON block
	start := strings.Index(response, "{")
	end := strings.LastIndex(response, "}")

	if start == -1 || end == -1 {
		return nil, fmt.Errorf("no JSON object found in response")
	}

	jsonStr := response[start : end+1]

	// Parse JSON
	var raw struct {
		FactualAccuracy    int      `json:"factual_accuracy"`
		HallucinationScore int      `json:"hallucination_score"`
		QueryQuality       int      `json:"query_quality"`
		Completeness       int      `json:"completeness"`
		Verdict            string   `json:"verdict"`
		Reasoning          string   `json:"reasoning"`
		Issues             []string `json:"issues"`
	}

	if err := json.Unmarshal([]byte(jsonStr), &raw); err != nil {
		return nil, fmt.Errorf("failed to parse JSON: %w", err)
	}

	// Validate scores
	if raw.FactualAccuracy < 0 || raw.FactualAccuracy > 100 {
		return nil, fmt.Errorf("factual_accuracy out of range: %d", raw.FactualAccuracy)
	}
	if raw.HallucinationScore < 0 || raw.HallucinationScore > 100 {
		return nil, fmt.Errorf("hallucination_score out of range: %d", raw.HallucinationScore)
	}
	if raw.QueryQuality < 0 || raw.QueryQuality > 100 {
		return nil, fmt.Errorf("query_quality out of range: %d", raw.QueryQuality)
	}
	if raw.Completeness < 0 || raw.Completeness > 100 {
		return nil, fmt.Errorf("completeness out of range: %d", raw.Completeness)
	}

	// Validate verdict
	switch raw.Verdict {
	case "PASS", "FAIL", "PARTIAL":
		// Valid
	default:
		return nil, fmt.Errorf("invalid verdict: %s", raw.Verdict)
	}

	// Build verdict
	verdict := &Verdict{
		FactualAccuracy:    raw.FactualAccuracy,
		HallucinationScore: raw.HallucinationScore,
		QueryQuality:       raw.QueryQuality,
		Completeness:       raw.Completeness,
		Verdict:            raw.Verdict,
		Reasoning:          raw.Reasoning,
		Issues:             raw.Issues,
	}

	if verdict.Issues == nil {
		verdict.Issues = []string{}
	}

	return verdict, nil
}
