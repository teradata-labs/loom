package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/teradata-labs/loom/pkg/decision/jev"
)

// runJudgeCmd scores every deferred (Jev) check in the run tree. It runs
// after all arm runs, so decider traffic never overlaps a latency
// measurement. Each run's verdict goes to judge.json beside its result.json,
// and an existing judge.json is never overwritten.
func runJudgeCmd(args []string) error {
	flags := flag.NewFlagSet("judge", flag.ContinueOnError)
	out := flags.String("out", "", "results directory (required)")
	model := flags.String("model", defaultDeciderModel, "decider model id")
	calPath := flags.String("calibration", "", "calibration.json; the gate must have passed (required)")
	file := flags.String("file", "judge.json", "verdict file name in each run directory; an existing one is never overwritten, so a re-judgement takes a new name")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *out == "" || *calPath == "" {
		return fmt.Errorf("-out and -calibration are required")
	}
	if _, err := validatedCalibration(*calPath); err != nil {
		return err
	}
	client, err := newJevClient(*model)
	if err != nil {
		return err
	}

	runs, err := findRuns(filepath.Join(*out, "tasks"))
	if err != nil {
		return err
	}
	judged := 0
	for _, runDir := range runs {
		done, err := judgeRun(client, *model, runDir, *file)
		if err != nil {
			return fmt.Errorf("%s: %w", runDir, err)
		}
		if done {
			judged++
		}
	}
	fmt.Printf("judged %d runs with deferred checks\n", judged)
	return nil
}

// findRuns returns each run directory that has a result.json.
func findRuns(root string) ([]string, error) {
	var runs []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && d.Name() == "result.json" {
			runs = append(runs, filepath.Dir(p))
		}
		return nil
	})
	return runs, err
}

// judgeRun scores the deferred checks of one run. It returns false when the
// run has none or was already judged.
func judgeRun(client *jev.Client, model, runDir, file string) (bool, error) {
	var rec runRecord
	b, err := os.ReadFile(filepath.Join(runDir, "result.json"))
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(b, &rec); err != nil {
		return false, err
	}

	var deferred []checkResult
	for _, c := range rec.Checks {
		if c.Deferred {
			deferred = append(deferred, c)
		}
	}
	if len(deferred) == 0 {
		return false, nil
	}

	if file != filepath.Base(file) || !strings.HasSuffix(file, ".json") {
		return false, fmt.Errorf("-file %q must be a bare .json file name", file)
	}
	jPath := filepath.Join(runDir, file)
	if _, err := os.Stat(jPath); err == nil {
		return false, nil // never overwrite a judgement
	}

	final := ""
	if n := len(rec.Turns); n > 0 {
		final = rec.Turns[n-1].Assistant
	}

	var results []judgement
	for _, c := range deferred {
		j := judgement{Check: c.Value + "@" + c.Path, Criterion: c.Value, Model: model}
		crit, ok := criteriaByName[c.Value]
		if !ok {
			j.Error = "unknown criterion " + c.Value
			results = append(results, j)
			continue
		}
		// The source is the fixture copy, never the agent's workspace.
		source, err := readWorkspaceFile(filepath.Join(runDir, "fixture"), c.Path)
		if err != nil {
			j.Error = "source: " + err.Error()
			results = append(results, j)
			continue
		}
		p, err := askDefective(client, source, final, crit)
		if err != nil {
			j.Error = err.Error()
		} else {
			j.Probability = p
			j.Defective = p >= deciderThreshold
		}
		results = append(results, j)
	}

	out, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return false, err
	}
	if err := os.WriteFile(jPath, out, 0o644); err != nil {
		return false, err
	}
	return true, nil
}
