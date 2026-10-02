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

package agent

import (
	"context"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/decision/sites"
	"github.com/teradata-labs/loom/pkg/session"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// DecideToolName is the builtin that lets an agent put one typed question to
// its decision layer mid-turn.
const DecideToolName = "decide"

// DecisionTool is the "decide" builtin. It asks the agent's decision layer
// one question (yes/no, a choice among options, or a level on a scale)
// about facts the agent supplies and returns calibrated probabilities, so
// the agent can branch on a number instead of talking itself into an
// answer. Nothing acts on the answer automatically: the caller decides what
// to do with it. Every call is recorded as a shadow row at sites.SiteAsk
// with no reference, so `loom decision report --site tool.decide` shows how
// the tool is used and how decisive the decider was.
type DecisionTool struct {
	agent *Agent
}

// NewDecisionTool builds the decide tool over an agent's decision layer.
func NewDecisionTool(a *Agent) *DecisionTool { return &DecisionTool{agent: a} }

func (t *DecisionTool) Name() string    { return DecideToolName }
func (t *DecisionTool) Backend() string { return "" }

func (t *DecisionTool) Description() string {
	return `Ask the typed decision model one question about facts you provide and get calibrated probabilities back, without generating text.

Kinds:
- yes_no (default): answered with p_yes. Optionally give when_yes / when_no to pin down what each side means.
- choice: pick among options (2 or more short labels). Returns a probability per option plus none_of_these.
- scale: rate on ordered options from lowest to highest (2-10 levels). Returns a probability per level and the expected level.

Put everything the answer should depend on in facts (plain text or a JSON object). The model sees only facts and the question, not this conversation.
Use it for a decision you would otherwise reason about at length: is this result an error, which branch fits, how severe is this, does this output satisfy a requirement.`
}

func (t *DecisionTool) InputSchema() *shuttle.JSONSchema {
	return &shuttle.JSONSchema{
		Type: "object",
		Properties: map[string]*shuttle.JSONSchema{
			"question": {
				Type:        "string",
				Description: "The question to decide, phrased so the facts can answer it",
			},
			"kind": {
				Type:        "string",
				Description: "yes_no (default), choice, or scale",
				Enum:        []interface{}{sites.AskYesNo, sites.AskChoice, sites.AskScale},
			},
			"facts": {
				Type:        "string",
				Description: "Everything the answer depends on: plain text, or a JSON object of named facts",
			},
			"options": {
				Type:        "array",
				Description: "(choice) the labels to choose among; (scale) the levels, lowest first",
				Items:       &shuttle.JSONSchema{Type: "string"},
			},
			"when_yes": {
				Type:        "string",
				Description: "(yes_no) what a yes means, when the question alone is ambiguous",
			},
			"when_no": {
				Type:        "string",
				Description: "(yes_no) what a no means",
			},
		},
		Required: []string{"question", "facts"},
	}
}

// Execute implements shuttle.Tool.
func (t *DecisionTool) Execute(ctx context.Context, input map[string]interface{}) (*shuttle.Result, error) {
	if t.agent == nil || t.agent.decisionRouter == nil {
		return errorResult("DECISION_LAYER_OFF", "this agent has no decision layer configured"), nil
	}
	ask := sites.Ask{
		Kind:     getStr(input, "kind"),
		Question: getStr(input, "question"),
		Facts:    input["facts"],
		Options:  getStrSlice(input, "options"),
		WhenYes:  getStr(input, "when_yes"),
		WhenNo:   getStr(input, "when_no"),
	}
	req, err := sites.AskRequest(ask)
	if err != nil {
		return errorResult("INVALID_PARAMETER", err.Error()), nil
	}

	sessionID := session.SessionIDFromContext(ctx)
	out := t.agent.liveDecide(ctx, sessionID, req)
	// Record the call whatever happened; there is nothing to compare it with.
	t.agent.recordDecisionAsync(ctx, sessionID, req, out, nil)

	ans, err := sites.AskAnswerOf(ask.Kind, out)
	if err != nil {
		code := "DECIDER_ERROR"
		switch out.Path {
		case loomv1.DecisionPath_DECISION_PATH_BUDGET:
			code = "DECISION_BUDGET_EXHAUSTED"
		case loomv1.DecisionPath_DECISION_PATH_DISABLED:
			code = "DECISION_LAYER_OFF"
		}
		return errorResult(code, err.Error()), nil
	}
	return &shuttle.Result{
		Success: true,
		Data:    ans,
		Metadata: map[string]interface{}{
			"decision.site": sites.SiteAsk,
			"decision.path": out.Path.String(),
		},
	}, nil
}

// checkAndRegisterDecideTool registers the decide builtin when the decision
// layer is on and the agent's decision config asks for the tool surface
// (decision.expose_tool). The layer's other uses (reranks, gates) do not
// depend on this switch. Idempotent.
func (a *Agent) checkAndRegisterDecideTool() {
	if a.isBuiltinToolSuppressed(DecideToolName) {
		return
	}
	if a.tools.IsRegistered(DecideToolName) {
		return
	}
	if a.decisionRouter == nil || a.decisionCfg == nil || !a.decisionCfg.ExposeTool {
		return
	}
	a.tools.Register(shuttle.Tool(NewDecisionTool(a)))
}
