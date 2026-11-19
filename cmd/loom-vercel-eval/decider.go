package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/decision"
	"github.com/teradata-labs/loom/pkg/decision/jev"
)

// defaultDeciderModel is the pinned Jev id. The probe showed the gateway
// accepts it, but its response echoes the requested id, so the version that
// answered is not independently confirmed.
const defaultDeciderModel = "typesafe-ai/jev-1.14.0"

// deciderThreshold is the probability at or above which the output counts as
// defective. Fixed before the calibration run.
const deciderThreshold = 0.5

// criteriaByName maps a check's Value to its criterion text.
var criteriaByName = map[string]string{
	"action_items": criterionActionItems,
	"spec_bullets": criterionSpecBullets,
}

// newJevClient builds the decider client through loom's own decision config
// path, so credentials and gateway URL resolve exactly as the product does.
func newJevClient(model string) (*jev.Client, error) {
	cfg, err := jev.FromDecisionConfig(&loomv1.DecisionConfig{Model: model, RequestsPerMinute: 28})
	if err != nil {
		return nil, err
	}
	return jev.New(cfg)
}

// askDefective asks Jev one yes/no question about an output against a
// source and returns the probability that the output is defective.
func askDefective(client *jev.Client, source, output, criterion string) (float64, error) {
	q := decision.Noul(criterion)
	req, err := decision.NewRequest("eval.defect", map[string]any{
		"source": source,
		"output": output,
	}, map[string]*loomv1.DecisionQuestion{"defective": q})
	if err != nil {
		return 0, fmt.Errorf("request: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	resp, err := client.Decide(ctx, req)
	if err != nil {
		return 0, err
	}
	a, err := decision.NoulOf(resp, "defective")
	if err != nil {
		return 0, fmt.Errorf("answer: %w", err)
	}
	return a.GetProbability(), nil
}

// judgement is the judge pass result for one run, written beside result.json.
type judgement struct {
	Check       string  `json:"check"`
	Criterion   string  `json:"criterion"`
	Model       string  `json:"model"`
	Probability float64 `json:"probability_defective"`
	Defective   bool    `json:"defective"`
	Error       string  `json:"error,omitempty"`
}

// validatedCalibration reads calibration.json and refuses unless the gate
// passed. Jev scores are never reported without a passing calibration.
func validatedCalibration(path string) (*calSummary, error) {
	// #nosec G304 -- path under the results directory the operator passed with -out, or a fixture the harness wrote; never request input
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("calibration file: %w", err)
	}
	var s calSummary
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("calibration file: %w", err)
	}
	if !s.Validated {
		return nil, fmt.Errorf("calibration gate did not pass (agreement %.3f, false pass %.3f); Jev scores are not reported", s.Agreement, s.FalsePassRate)
	}
	return &s, nil
}
