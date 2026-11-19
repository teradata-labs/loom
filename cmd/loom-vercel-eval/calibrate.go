package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

// Calibration gate, fixed before the run. Jev scores are reported as
// validated only when both hold.
const (
	gateMinAgreement = 0.90
	gateMaxFalsePass = 0.10
)

// calItem is one known-answer item. Defective is the construction label.
type calItem struct {
	ID        string `json:"id"`
	TaskType  string `json:"task_type"`
	Defect    string `json:"defect"` // "" for a correct output
	Defective bool   `json:"defective"`
	Source    string `json:"source"`
	Output    string `json:"output"`
	Criterion string `json:"criterion"`
}

// calResult is what Jev answered for one item.
type calResult struct {
	calItem
	Probability float64 `json:"probability_defective"`
	JevSaysBad  bool    `json:"jev_says_defective"`
	Agree       bool    `json:"agree"`
	Error       string  `json:"error,omitempty"`
}

// calSummary is the gate verdict written with the raw results.
type calSummary struct {
	Model         string      `json:"model"`
	Items         int         `json:"items"`
	Agreement     float64     `json:"agreement"`
	FalsePassRate float64     `json:"false_pass_rate"`
	FalseFailRate float64     `json:"false_fail_rate"`
	Validated     bool        `json:"validated"`
	GateAgreement float64     `json:"gate_min_agreement"`
	GateFalsePass float64     `json:"gate_max_false_pass"`
	Results       []calResult `json:"results"`
}

// runCalibrateCmd sends each known-answer item to Jev and applies the gate.
func runCalibrateCmd(args []string) error {
	fs := flag.NewFlagSet("calibrate", flag.ContinueOnError)
	out := fs.String("out", "", "results directory (required)")
	model := fs.String("model", defaultDeciderModel, "decider model id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		return fmt.Errorf("-out is required")
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return err
	}

	client, err := newJevClient(*model)
	if err != nil {
		return err
	}

	items := calibrationItems()
	sum := calSummary{
		Model:         *model,
		Items:         len(items),
		GateAgreement: gateMinAgreement,
		GateFalsePass: gateMaxFalsePass,
	}
	var agree, defective, correct, falsePass, falseFail int
	scored := 0
	for _, it := range items {
		res := calResult{calItem: it}
		p, err := askDefective(client, it.Source, it.Output, it.Criterion)
		if err != nil {
			res.Error = err.Error()
		} else {
			res.Probability = p
			res.JevSaysBad = p >= deciderThreshold
			scored++
			res.Agree = res.JevSaysBad == it.Defective
			if it.Defective {
				defective++
				if !res.JevSaysBad {
					falsePass++
				}
			} else {
				correct++
				if res.JevSaysBad {
					falseFail++
				}
			}
			if res.Agree {
				agree++
			}
		}
		sum.Results = append(sum.Results, res)
		fmt.Printf("%-16s defective=%-5v jev=%.2f agree=%v %s\n", it.ID, it.Defective, res.Probability, res.Agree, res.Error)
	}
	if scored > 0 {
		sum.Agreement = float64(agree) / float64(scored)
	}
	if defective > 0 {
		sum.FalsePassRate = float64(falsePass) / float64(defective)
	}
	if correct > 0 {
		sum.FalseFailRate = float64(falseFail) / float64(correct)
	}
	sum.Validated = scored == len(items) &&
		sum.Agreement >= gateMinAgreement &&
		sum.FalsePassRate <= gateMaxFalsePass

	path := filepath.Join(*out, "calibration.json")
	b, err := json.MarshalIndent(sum, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return err
	}
	fmt.Printf("agreement=%.3f (gate >= %.2f) false_pass=%.3f (gate <= %.2f) false_fail=%.3f validated=%v\n",
		sum.Agreement, gateMinAgreement, sum.FalsePassRate, gateMaxFalsePass, sum.FalseFailRate, sum.Validated)
	fmt.Println("wrote", path)
	return nil
}
