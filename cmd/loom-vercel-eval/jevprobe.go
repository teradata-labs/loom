package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/decision"
	"github.com/teradata-labs/loom/pkg/decision/jev"
)

// jevProbeResult is one raw answer from one model id, written to the results
// directory as JSON.
type jevProbeResult struct {
	RequestedModel string             `json:"requested_model"`
	ServedModel    string             `json:"served_model"`
	URL            string             `json:"url"`
	Error          string             `json:"error,omitempty"`
	LatencyMS      int64              `json:"latency_ms"`
	Probabilities  map[string]float64 `json:"probabilities,omitempty"`
	CostUSD        float64            `json:"cost_usd,omitempty"`
}

// runJevProbe sends one known-answer yes/no question to Jev through the
// gateway, once per model id, so the pinned-version question in the plan can
// be settled from the live response rather than from the code comments.
func runJevProbe(args []string) error {
	fs := flag.NewFlagSet("jev-probe", flag.ContinueOnError)
	out := fs.String("out", "", "results directory (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		return fmt.Errorf("-out is required")
	}
	if err := os.MkdirAll(*out, 0o750); err != nil {
		return err
	}

	models := []string{"typesafe-ai/jev-1.14.0", "typesafe-ai/jev"}
	var results []jevProbeResult
	for _, m := range models {
		results = append(results, probeOneJevModel(m))
	}

	path := filepath.Join(*out, "jev-probe.json")
	b, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return err
	}
	fmt.Println(string(b))
	fmt.Println("wrote", path)
	return nil
}

func probeOneJevModel(model string) jevProbeResult {
	res := jevProbeResult{RequestedModel: model}

	cfg, err := jev.FromDecisionConfig(&loomv1.DecisionConfig{Model: model})
	if err != nil {
		res.Error = "config: " + err.Error()
		return res
	}
	res.URL = redactURL(cfg.BaseURL + cfg.Path)

	client, err := jev.New(cfg)
	if err != nil {
		res.Error = "new client: " + err.Error()
		return res
	}

	// Known answer: the text names a fruit, so the probability should be high.
	q := decision.Noul("Does the text name a fruit?")
	req, err := decision.NewRequest("eval.jevprobe", map[string]any{
		"text": "Bananas are on sale this week.",
	}, map[string]*loomv1.DecisionQuestion{"fruit": q})
	if err != nil {
		res.Error = "request: " + err.Error()
		return res
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	start := time.Now()
	resp, err := client.Decide(ctx, req)
	res.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		res.Error = err.Error()
		return res
	}
	res.ServedModel = resp.GetModel()
	res.Probabilities = map[string]float64{}
	if a, err := decision.NoulOf(resp, "fruit"); err == nil {
		res.Probabilities["fruit"] = a.GetProbability()
	} else {
		res.Error = "answer: " + err.Error()
	}
	if resp.GetUsage() != nil {
		res.CostUSD = resp.GetUsage().GetCostUsd()
	}
	return res
}

// redactURL keeps scheme, host and path. Query strings are dropped, so no
// credential can reach the results file.
func redactURL(raw string) string {
	before, _, _ := strings.Cut(raw, "?")
	return before
}
