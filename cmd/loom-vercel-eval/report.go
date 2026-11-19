package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// cellKey groups runs by arm and task.
type cellKey struct{ Arm, Task string }

// cell is the aggregate of the repetitions of one task on one arm.
type cell struct {
	Reps       int     `json:"reps"`
	Pass       int     `json:"pass"`
	Unjudged   int     `json:"unjudged"`
	Errors     int     `json:"errors"`
	ToolCalls  int     `json:"tool_calls"`
	CostUSD    float64 `json:"cost_usd"`
	WallMSMean float64 `json:"wall_ms_mean"`
	wallSum    int64
}

// runVerdict is the final decision for one run once the judge pass is applied.
type runVerdict struct {
	Arm       string  `json:"arm"`
	Task      string  `json:"task"`
	Rep       int     `json:"rep"`
	Pass      bool    `json:"pass"`
	Unjudged  bool    `json:"unjudged"`
	Error     string  `json:"error,omitempty"`
	CostUSD   float64 `json:"cost_usd"`
	ToolCalls int     `json:"tool_calls"`
	WallMS    int64   `json:"wall_ms"`
	RunDir    string  `json:"run_dir"`
}

// runReportCmd aggregates every run in the results tree into a new summary
// file. It never overwrites an earlier summary.
func runReportCmd(args []string) error {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	out := fs.String("out", "", "results directory (required)")
	judgeFile := fs.String("judge-file", "judge.json", "verdict file the judge pass wrote (judge -file)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		return fmt.Errorf("-out is required")
	}

	runs, err := findRuns(filepath.Join(*out, "tasks"))
	if err != nil {
		return err
	}
	var verdicts []runVerdict
	for _, dir := range runs {
		v, err := verdictFor(dir, *judgeFile)
		if err != nil {
			return fmt.Errorf("%s: %w", dir, err)
		}
		verdicts = append(verdicts, v)
	}

	cells := map[cellKey]*cell{}
	var totalCost float64
	for _, v := range verdicts {
		k := cellKey{v.Arm, v.Task}
		c := cells[k]
		if c == nil {
			c = &cell{}
			cells[k] = c
		}
		c.Reps++
		if v.Pass {
			c.Pass++
		}
		if v.Unjudged {
			c.Unjudged++
		}
		if v.Error != "" {
			c.Errors++
		}
		c.ToolCalls += v.ToolCalls
		c.CostUSD += v.CostUSD
		c.wallSum += v.WallMS
		totalCost += v.CostUSD
	}
	keys := make([]cellKey, 0, len(cells))
	for k := range cells {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Arm != keys[j].Arm {
			return keys[i].Arm < keys[j].Arm
		}
		return keys[i].Task < keys[j].Task
	})

	fmt.Printf("%-5s %-4s %-7s %-8s %-6s %-9s %-10s\n", "arm", "task", "pass", "unjudged", "errors", "tools", "cost")
	for _, k := range keys {
		c := cells[k]
		c.WallMSMean = float64(c.wallSum) / float64(c.Reps)
		fmt.Printf("%-5s %-4s %d/%d    %-8d %-6d %-9d $%.4f\n", k.Arm, k.Task, c.Pass, c.Reps, c.Unjudged, c.Errors, c.ToolCalls, c.CostUSD)
	}
	fmt.Printf("total arm-run cost $%.4f across %d runs\n", totalCost, len(verdicts))

	summary := map[string]any{
		"generated_at": time.Now().UTC().Format(time.RFC3339),
		"total_cost":   totalCost,
		"runs":         len(verdicts),
		"cells":        cellsForJSON(keys, cells),
		"verdicts":     verdicts,
	}
	b, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(*out, fmt.Sprintf("summary-%s.json", time.Now().UTC().Format("20060102T150405Z")))
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("summary already exists: %s", path)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return err
	}
	fmt.Println("wrote", path)
	return nil
}

// verdictFor applies the judge pass to one run's deterministic result.
func verdictFor(runDir, judgeFile string) (runVerdict, error) {
	var rec runRecord
	// #nosec G304 -- path under the results directory the operator passed with -out, or a fixture the harness wrote; never request input
	b, err := os.ReadFile(filepath.Join(runDir, "result.json"))
	if err != nil {
		return runVerdict{}, err
	}
	if err := json.Unmarshal(b, &rec); err != nil {
		return runVerdict{}, err
	}
	v := runVerdict{
		Arm: rec.Arm, Task: rec.TaskID, Rep: rec.Rep, Pass: rec.Pass,
		Error: rec.Error, CostUSD: rec.CostUSD, ToolCalls: len(rec.ToolCalls),
		WallMS: rec.WallMS, RunDir: runDir,
	}
	hasDeferred := false
	for _, c := range rec.Checks {
		if c.Deferred {
			hasDeferred = true
		}
	}
	if !hasDeferred {
		return v, nil
	}
	// #nosec G304 -- path under the results directory the operator passed with -out, or a fixture the harness wrote; never request input
	jb, err := os.ReadFile(filepath.Join(runDir, judgeFile))
	if err != nil {
		v.Unjudged = true
		v.Pass = false
		return v, nil
	}
	var js []judgement
	if err := json.Unmarshal(jb, &js); err != nil {
		return v, err
	}
	for _, j := range js {
		if j.Error != "" || j.Defective {
			v.Pass = false
		}
	}
	return v, nil
}

// cellsForJSON flattens the cells into a stable list for the summary file.
func cellsForJSON(keys []cellKey, cells map[cellKey]*cell) []map[string]any {
	out := make([]map[string]any, 0, len(keys))
	for _, k := range keys {
		c := cells[k]
		out = append(out, map[string]any{
			"arm": k.Arm, "task": k.Task, "reps": c.Reps, "pass": c.Pass,
			"unjudged": c.Unjudged, "errors": c.Errors, "tool_calls": c.ToolCalls,
			"cost_usd": c.CostUSD, "wall_ms_mean": c.WallMSMean,
		})
	}
	return out
}
