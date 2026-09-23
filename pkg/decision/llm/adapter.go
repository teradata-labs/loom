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

// Package llm adapts any LLMProvider into a decision.Decider.
//
// It renders a DecisionRequest into one prompt that asks for the exact typed
// answer shape, calls the provider once, and parses the JSON strictly against
// the request. It is the fallback for the uncertain band, the offline
// comparator for shadow evaluation, and the egress-free path for deployments
// that run a local model. It is not fast or cheap: it pays for generated
// output tokens, which is the cost a decision model removes.
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/decision"
	"github.com/teradata-labs/loom/pkg/prompts"
	"github.com/teradata-labs/loom/pkg/types"
)

// PromptKey is the prompt-registry key for the adapter template. The file is
// prompts/decision/llm_adapter.yaml; DefaultTemplate is its fallback when no
// registry is wired, and a test keeps the two identical.
const PromptKey = "decision.llm_adapter"

// DefaultTemplate is the built-in prompt, used when no registry is set or the
// registry has no entry for PromptKey. Placeholders are substituted by plain
// replacement, not registry interpolation, so state text reaches the model
// exactly as filtered by the call site.
const DefaultTemplate = `Answer each question below about the STATE, and return ONLY a JSON object.
Do not add prose, markdown fences, or keys that are not requested.

STATE:
{{.state}}

QUESTIONS:
{{.questions}}

Return exactly this JSON shape, one entry per question id:
{{.schema}}

Rules:
- "noul" questions: "probability" is the probability in [0, 1] that the statement is true.
- "choice" questions: "probabilities" gives a probability for EVERY listed option key,
  summing to 1. Do not invent option keys.
- "score" questions: "probabilities" gives a probability for EVERY level index as a
  string ("0", "1", ...), summing to 1. Level 0 is the lowest.
- Judge only from the STATE. If the STATE does not settle a question, spread the
  probability rather than guessing.
`

// DefaultTimeout bounds one adapter call. Generative reranks today run with
// 10–20 s timeouts; the adapter is the same shape of call.
const DefaultTimeout = 20 * time.Second

// Adapter is a decision.Decider backed by a generative LLM.
type Adapter struct {
	provider types.LLMProvider
	registry prompts.PromptRegistry
	timeout  time.Duration
}

// Option configures an Adapter.
type Option func(*Adapter)

// WithPromptRegistry sources the template from a registry under PromptKey,
// falling back to DefaultTemplate when the key is absent.
func WithPromptRegistry(r prompts.PromptRegistry) Option {
	return func(a *Adapter) { a.registry = r }
}

// WithTimeout bounds each call. Zero keeps DefaultTimeout.
func WithTimeout(d time.Duration) Option {
	return func(a *Adapter) {
		if d > 0 {
			a.timeout = d
		}
	}
}

// New wraps provider. It panics on a nil provider: the adapter has nothing to
// adapt, and that is a wiring error, not a runtime condition.
func New(provider types.LLMProvider, opts ...Option) *Adapter {
	if provider == nil {
		panic("decision/llm: nil provider")
	}
	a := &Adapter{provider: provider, timeout: DefaultTimeout}
	for _, o := range opts {
		o(a)
	}
	return a
}

// Name implements decision.Decider as "llm:<provider>".
func (a *Adapter) Name() string { return "llm:" + a.provider.Name() }

// Model implements decision.Decider.
func (a *Adapter) Model() string { return a.provider.Model() }

// Decide renders the request, calls the provider once, and parses the answer
// strictly. A response that does not match the request is
// decision.ErrMalformedAnswer, never a best-effort guess.
func (a *Adapter) Decide(ctx context.Context, req *loomv1.DecisionRequest) (*loomv1.DecisionResponse, error) {
	if err := decision.Validate(req); err != nil {
		return nil, err
	}
	prompt, err := a.render(ctx, req)
	if err != nil {
		return nil, err
	}

	callCtx, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()
	resp, err := a.provider.Chat(callCtx, []types.Message{{Role: "user", Content: prompt}}, nil)
	if err != nil {
		return nil, fmt.Errorf("decision/llm: provider %s: %w", a.provider.Name(), err)
	}
	if resp == nil {
		return nil, fmt.Errorf("%w: nil provider response", decision.ErrMalformedAnswer)
	}

	answers, err := ParseAnswers(req, resp.Content)
	if err != nil {
		return nil, err
	}
	out := &loomv1.DecisionResponse{
		Model:   a.provider.Model(),
		Answers: answers,
		Usage: &loomv1.DecisionUsage{
			InputTokens:  int64(resp.Usage.InputTokens),
			OutputTokens: int64(resp.Usage.OutputTokens),
			CostUsd:      resp.Usage.CostUSD,
		},
	}
	if err := decision.CheckAnswers(req, out); err != nil {
		return nil, err
	}
	return out, nil
}

// render fills the template. Registry lookups pass nil vars so the template
// comes back verbatim; substitution is plain replacement.
func (a *Adapter) render(ctx context.Context, req *loomv1.DecisionRequest) (string, error) {
	tmpl := DefaultTemplate
	if a.registry != nil {
		if t, err := a.registry.Get(ctx, PromptKey, nil); err == nil && strings.TrimSpace(t) != "" {
			tmpl = t
		}
	}
	questions, schema, err := renderQuestions(req)
	if err != nil {
		return "", err
	}
	out := strings.NewReplacer(
		"{{.state}}", stateText(req.State),
		"{{.questions}}", questions,
		"{{.schema}}", schema,
	).Replace(tmpl)
	return out, nil
}

// wireAnswer is the JSON shape the prompt asks for, per question id.
type wireAnswer struct {
	Probability   *float64           `json:"probability,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
}

// renderQuestions returns the QUESTIONS block and the answer schema block.
func renderQuestions(req *loomv1.DecisionRequest) (questions string, schema string, err error) {
	ids := make([]string, 0, len(req.Questions))
	for id := range req.Questions {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	var qb strings.Builder
	shape := make(map[string]any, len(ids))
	for _, id := range ids {
		q := req.Questions[id]
		fmt.Fprintf(&qb, "- id: %s\n", id)
		fmt.Fprintf(&qb, "  instructions: %s\n", valueText(q.Instructions))
		switch k := q.Kind.(type) {
		case *loomv1.DecisionQuestion_Noul:
			qb.WriteString("  type: noul\n")
			if k.Noul.CriteriaTrue != nil {
				fmt.Fprintf(&qb, "  true_when: %s\n", valueText(k.Noul.CriteriaTrue))
			}
			if k.Noul.CriteriaFalse != nil {
				fmt.Fprintf(&qb, "  false_when: %s\n", valueText(k.Noul.CriteriaFalse))
			}
			shape[id] = map[string]any{"probability": "<0..1>"}
		case *loomv1.DecisionQuestion_Choice:
			qb.WriteString("  type: choice\n  options:\n")
			keys := sortedKeys(k.Choice.Options)
			probs := make(map[string]any, len(keys))
			for _, key := range keys {
				fmt.Fprintf(&qb, "    %s: %s\n", key, valueText(k.Choice.Options[key]))
				probs[key] = "<0..1>"
			}
			shape[id] = map[string]any{"probabilities": probs}
		case *loomv1.DecisionQuestion_Score:
			qb.WriteString("  type: score\n  levels:\n")
			probs := make(map[string]any, len(k.Score.Levels))
			for i, l := range k.Score.Levels {
				fmt.Fprintf(&qb, "    %d: %s\n", i, valueText(l))
				probs[strconv.Itoa(i)] = "<0..1>"
			}
			shape[id] = map[string]any{"probabilities": probs}
		default:
			return "", "", fmt.Errorf("decision/llm: question %q has no kind", id)
		}
	}
	// A plain encoder, not MarshalIndent: the default HTML escaping would
	// turn the "<0..1>" placeholders into <0..1>.
	var sb strings.Builder
	enc := json.NewEncoder(&sb)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(shape); err != nil {
		return "", "", fmt.Errorf("decision/llm: render schema: %w", err)
	}
	return qb.String(), strings.TrimRight(sb.String(), "\n"), nil
}

// ParseAnswers parses a model reply against the request. It tolerates a
// fenced code block or leading prose around the object (models add both)
// but nothing inside it: unknown question ids, unknown option keys, missing
// distributions and out-of-range values are decision.ErrMalformedAnswer.
func ParseAnswers(req *loomv1.DecisionRequest, content string) (map[string]*loomv1.DecisionAnswer, error) {
	body, ok := extractObject(content)
	if !ok {
		return nil, fmt.Errorf("%w: no JSON object in reply", decision.ErrMalformedAnswer)
	}
	var wire map[string]wireAnswer
	dec := json.NewDecoder(strings.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&wire); err != nil {
		return nil, fmt.Errorf("%w: %v", decision.ErrMalformedAnswer, err)
	}
	for id := range wire {
		if _, ok := req.Questions[id]; !ok {
			return nil, fmt.Errorf("%w: answer for unknown question %q", decision.ErrMalformedAnswer, id)
		}
	}

	answers := make(map[string]*loomv1.DecisionAnswer, len(req.Questions))
	for id, q := range req.Questions {
		w, ok := wire[id]
		if !ok {
			return nil, fmt.Errorf("%w: no answer for %q", decision.ErrMalformedAnswer, id)
		}
		switch k := q.Kind.(type) {
		case *loomv1.DecisionQuestion_Noul:
			if w.Probability == nil {
				return nil, fmt.Errorf("%w: %q missing probability", decision.ErrMalformedAnswer, id)
			}
			p := *w.Probability
			if p < 0 || p > 1 {
				return nil, fmt.Errorf("%w: %q probability %v outside [0,1]", decision.ErrMalformedAnswer, id, p)
			}
			answers[id] = &loomv1.DecisionAnswer{Kind: &loomv1.DecisionAnswer_Noul{Noul: &loomv1.NoulAnswer{Probability: p}}}
		case *loomv1.DecisionQuestion_Choice:
			probs, err := completeDistribution(id, w.Probabilities, sortedKeys(k.Choice.Options))
			if err != nil {
				return nil, err
			}
			c, err := decision.ChoiceFromProbabilities(probs)
			if err != nil {
				return nil, fmt.Errorf("%w: %q: %v", decision.ErrMalformedAnswer, id, errors.Unwrap(err))
			}
			answers[id] = &loomv1.DecisionAnswer{Kind: &loomv1.DecisionAnswer_Choice{Choice: c}}
		case *loomv1.DecisionQuestion_Score:
			levelKeys := make([]string, len(k.Score.Levels))
			for i := range k.Score.Levels {
				levelKeys[i] = strconv.Itoa(i)
			}
			probs, err := completeDistribution(id, w.Probabilities, levelKeys)
			if err != nil {
				return nil, err
			}
			s, err := decision.ScoreFromProbabilities(probs, decision.LegendOf(k.Score))
			if err != nil {
				return nil, fmt.Errorf("%w: %q: %v", decision.ErrMalformedAnswer, id, errors.Unwrap(err))
			}
			answers[id] = &loomv1.DecisionAnswer{Kind: &loomv1.DecisionAnswer_Score{Score: s}}
		default:
			return nil, fmt.Errorf("%w: question %q has no kind", decision.ErrMalformedAnswer, id)
		}
	}
	return answers, nil
}

// completeDistribution checks the reply's keys are a subset of the allowed
// keys, fills missing keys with 0, and rejects negative values. Normalization
// happens in the answer constructors.
func completeDistribution(id string, got map[string]float64, allowed []string) (map[string]float64, error) {
	if len(got) == 0 {
		return nil, fmt.Errorf("%w: %q missing probabilities", decision.ErrMalformedAnswer, id)
	}
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, k := range allowed {
		allowedSet[k] = struct{}{}
	}
	out := make(map[string]float64, len(allowed))
	for k, p := range got {
		if _, ok := allowedSet[k]; !ok {
			return nil, fmt.Errorf("%w: %q has probability for unknown key %q", decision.ErrMalformedAnswer, id, k)
		}
		if p < 0 {
			return nil, fmt.Errorf("%w: %q probability for %q is negative", decision.ErrMalformedAnswer, id, k)
		}
		out[k] = p
	}
	for _, k := range allowed {
		if _, ok := out[k]; !ok {
			out[k] = 0
		}
	}
	return out, nil
}

// extractObject returns the outermost balanced {...} in content, skipping
// braces inside JSON strings. It returns false when there is none.
func extractObject(content string) (string, bool) {
	start := strings.IndexByte(content, '{')
	if start < 0 {
		return "", false
	}
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(content); i++ {
		c := content[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return content[start : i+1], true
			}
		}
	}
	return "", false
}

func stateText(v *structpb.Value) string {
	if v == nil {
		return ""
	}
	if s, ok := v.Kind.(*structpb.Value_StringValue); ok {
		return s.StringValue
	}
	b, err := protojson.MarshalOptions{Multiline: true, Indent: "  "}.Marshal(v)
	if err != nil {
		return fmt.Sprint(v.AsInterface())
	}
	return string(b)
}

func valueText(v *structpb.Value) string {
	if v == nil {
		return ""
	}
	if s, ok := v.Kind.(*structpb.Value_StringValue); ok {
		return s.StringValue
	}
	b, err := protojson.Marshal(v)
	if err != nil {
		return fmt.Sprint(v.AsInterface())
	}
	return string(b)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
