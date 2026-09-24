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

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/decision"
	"github.com/teradata-labs/loom/pkg/decision/mock"
	"github.com/teradata-labs/loom/pkg/decision/sites"
	"github.com/teradata-labs/loom/pkg/observability"
)

var (
	askKind    string
	askFacts   string
	askOptions []string
	askWhenYes string
	askWhenNo  string
	askJSON    bool
	askTimeout time.Duration
)

var decisionAskCmd = &cobra.Command{
	Use:   "ask [flags] QUESTION",
	Short: "Put one typed question to a decider and print its probabilities",
	Long: `Ask a decider one question about facts you supply, without an agent in the
loop. The same request an agent's "decide" tool would send, from the shell.

  loom decision ask --decider jev --facts 'exit status 137, "Killed"' \
      "Did the process run out of memory?"
  loom decision ask --decider jev --kind choice --options transient,auth,bad_input \
      --facts @error.txt "What kind of failure is this?"
  loom decision ask --decider llm --kind scale --options low,medium,high,critical \
      --facts @incident.json "How severe is this incident?"

--facts is plain text, JSON text, or @path to read either from a file. The
decider sees only the facts and the question. Nothing is written to loom.db.`,
	Args: cobra.ExactArgs(1),
	RunE: runDecisionAsk,
}

func init() {
	decisionAskCmd.Flags().StringVar(&decisionDecider, "decider", "jev", "Decider: jev | llm | mock (jev reads TYPESAFE_API_KEY or AI_GATEWAY_API_KEY)")
	decisionAskCmd.Flags().StringVar(&decisionBaseURL, "base-url", "", "Endpoint base URL for --decider jev")
	decisionAskCmd.Flags().StringVar(&decisionProvider, "provider", "", "LLM provider for --decider llm (default: $LOOM_LLM_PROVIDER or anthropic)")
	decisionAskCmd.Flags().StringVar(&decisionModel, "model", "", "Model for the chosen decider (default: the decider's default)")
	decisionAskCmd.Flags().Float64Var(&decisionRPM, "rpm", 0, "For --decider jev: cap requests per minute (0 = client default)")
	decisionAskCmd.Flags().StringVar(&askKind, "kind", sites.AskYesNo, "yes_no | choice | scale")
	decisionAskCmd.Flags().StringVar(&askFacts, "facts", "", "What the answer depends on: text, JSON text, or @path (required)")
	decisionAskCmd.Flags().StringSliceVar(&askOptions, "options", nil, "choice: labels to choose among; scale: levels, lowest first")
	decisionAskCmd.Flags().StringVar(&askWhenYes, "when-yes", "", "yes_no: what a yes means")
	decisionAskCmd.Flags().StringVar(&askWhenNo, "when-no", "", "yes_no: what a no means")
	decisionAskCmd.Flags().BoolVar(&askJSON, "json", false, "Print the answer as JSON")
	decisionAskCmd.Flags().DurationVar(&askTimeout, "timeout", 30*time.Second, "Decider call timeout")
	_ = decisionAskCmd.MarkFlagRequired("facts")

	decisionCmd.AddCommand(decisionAskCmd)
}

// loadAskFacts resolves --facts: @path reads the file; JSON text becomes a
// structured state; anything else is plain text.
func loadAskFacts(s string) (any, error) {
	if strings.HasPrefix(s, "@") {
		b, err := os.ReadFile(strings.TrimPrefix(s, "@"))
		if err != nil {
			return nil, fmt.Errorf("read facts: %w", err)
		}
		s = string(b)
	}
	trimmed := strings.TrimSpace(s)
	if strings.HasPrefix(trimmed, "{") {
		var obj map[string]any
		if err := json.Unmarshal([]byte(trimmed), &obj); err == nil {
			return obj, nil
		}
		// Not valid JSON: treat as text.
	}
	return s, nil
}

func runDecisionAsk(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	facts, err := loadAskFacts(askFacts)
	if err != nil {
		return err
	}
	ask := sites.Ask{
		Kind:     askKind,
		Question: args[0],
		Facts:    facts,
		Options:  askOptions,
		WhenYes:  askWhenYes,
		WhenNo:   askWhenNo,
	}
	req, err := sites.AskRequest(ask)
	if err != nil {
		return err
	}

	var decider decision.Decider
	if strings.EqualFold(decisionDecider, "mock") {
		// A scripted answer for the request just built, so the command can
		// be exercised end to end without credentials.
		decider = askMockDecider(req)
	} else {
		decider, err = buildReplayDecider(ctx)
		if err != nil {
			return err
		}
	}
	tracer := observability.NewNoOpTracer()
	router := decision.NewRouter(decision.NewInstrumented(decision.Chunk(decider), tracer), decision.WithTracer(tracer))

	callCtx, cancel := context.WithTimeout(ctx, askTimeout)
	defer cancel()
	out := router.Decide(callCtx, req)
	ans, err := sites.AskAnswerOf(ask.Kind, out)
	if err != nil {
		return fmt.Errorf("%s: %w", decider.Name(), err)
	}

	w := cmd.OutOrStdout()
	if askJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(ans)
	}
	switch ans.Kind {
	case sites.AskYesNo:
		emitf(w, "answer: %v  (p_yes %.3f)\n", ans.Answer, *ans.PYes)
	case sites.AskChoice:
		emitf(w, "answer: %v\n", ans.Answer)
		for _, p := range ans.Probabilities {
			emitf(w, "  %-24s %.3f\n", p.Option, p.P)
		}
	case sites.AskScale:
		emitf(w, "answer: %v  (expected level %.2f)\n", ans.Answer, *ans.ExpectedLevel)
		for _, p := range ans.Probabilities {
			emitf(w, "  %d %-22s %.3f\n", *p.Level, p.Label, p.P)
		}
	}
	emitf(w, "confidence %.3f  model %s  %d ms", ans.Confidence, ans.Model, ans.LatencyMs)
	if ans.CostUSD > 0 {
		emitf(w, "  $%.6f", ans.CostUSD)
	}
	emit(w, "\n")
	return nil
}

// askMockDecider scripts a plausible answer for whatever question the ask
// built: p(yes)=0.75, a choice that favours the alphabetically first option,
// a scale that favours the middle level.
func askMockDecider(req *loomv1.DecisionRequest) decision.Decider {
	m := mock.New().SetModel("mock")
	switch k := req.Questions[sites.QAsk].Kind.(type) {
	case *loomv1.DecisionQuestion_Noul:
		m.AnswerNoul(sites.QAsk, 0.75)
	case *loomv1.DecisionQuestion_Choice:
		keys := make([]string, 0, len(k.Choice.Options))
		for key := range k.Choice.Options {
			if key != decision.NoneOption {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		probs := map[string]float64{decision.NoneOption: 0.05}
		if len(keys) == 1 {
			probs[keys[0]] = 0.95
		} else {
			rest := 0.25 / float64(len(keys)-1)
			for i, key := range keys {
				if i == 0 {
					probs[key] = 0.7
				} else {
					probs[key] = rest
				}
			}
		}
		m.AnswerChoice(sites.QAsk, probs)
	case *loomv1.DecisionQuestion_Score:
		n := len(k.Score.Levels)
		probs := make(map[string]float64, n)
		mid := n / 2
		for i := 0; i < n; i++ {
			if i == mid {
				probs[fmt.Sprint(i)] = 0.6
			} else {
				probs[fmt.Sprint(i)] = 0.4 / float64(n-1)
			}
		}
		m.AnswerScore(sites.QAsk, probs)
	}
	return m
}
