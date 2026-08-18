// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package builtin

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/teradata-labs/loom/pkg/fabric"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// DbtBuildTool builds a dbt project the way a verifier does — seeds first, then
// the whole project, never a selector — and reports the grain of the models the
// caller authored.
//
// It exists because both halves are things an agent gets wrong for the same
// reason: they are invisible from inside its own frame. A build narrowed with
// --select exits 0 and looks green while the project is broken; a model with a
// fan-out join returns plausible numbers and a doubled row count. Neither shows
// up as an error, so neither triggers a check. Riding both on the build — an
// action the agent must take anyway — removes the need for it to suspect
// anything.
//
// The grain report computes nothing the agent could not compute itself. That is
// the point: it is unconditional.
type DbtBuildTool struct {
	backend fabric.ExecutionBackend
	baseDir string
}

const (
	// dbtAuthoredWindow is how recently a model file must have been written to
	// count as the caller's own work rather than project furniture.
	dbtAuthoredWindow = 6 * time.Hour
	// dbtMaxReported bounds the grain section.
	dbtMaxReported = 12
	// dbtLogTail is how much dbt output is echoed back on failure.
	dbtLogTail = 3000
	// dbtStepTimeout bounds a single dbt invocation.
	dbtStepTimeout = 15 * time.Minute
	// dbtRatioEpsilon is the tolerance for calling a row ratio an exact
	// integer multiple — the signature of a join fan-out.
	dbtRatioEpsilon = 0.001
	// dbtSumTolerance is the relative drift above which a measure is reported
	// as not conserved against its source.
	dbtSumTolerance = 0.005
)

// NewDbtBuildTool wraps the given backend.
func NewDbtBuildTool(backend fabric.ExecutionBackend, baseDir string) *DbtBuildTool {
	return &DbtBuildTool{backend: backend, baseDir: baseDir}
}

// Name returns the tool name.
func (t *DbtBuildTool) Name() string { return "dbt_correctness_check" }

// Backend returns "" — the concrete backend is injected, not looked up.
func (t *DbtBuildTool) Backend() string { return "" }

// Description returns the tool description.
func (t *DbtBuildTool) Description() string {
	return `Build a dbt project — mandatory to run and check the output before calling a dbt task complete`
}

// InputSchema returns the JSON schema for the tool input.
func (t *DbtBuildTool) InputSchema() *shuttle.JSONSchema {
	return shuttle.NewObjectSchema(
		"Parameters for building a dbt project",
		map[string]*shuttle.JSONSchema{
			"project_dir": shuttle.NewStringSchema(
				"Directory holding dbt_project.yml. Omit to search the workspace.",
			),
		},
		[]string{},
	)
}

// Execute runs the build and assembles the report.
func (t *DbtBuildTool) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	start := time.Now()

	dir, err := t.resolveProject(params)
	if err != nil {
		return &shuttle.Result{
			Success:         false,
			Error:           &shuttle.Error{Code: "NO_PROJECT", Message: err.Error()},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "PROJECT %s\n", dir)

	// deps is advisory: a project without packages.yml exits non-zero and that
	// is not a failure of the build.
	if out, code := t.runDbt(ctx, dir, "deps"); code != 0 {
		fmt.Fprintf(&b, "deps: skipped (%s)\n", firstLine(out))
	} else {
		fmt.Fprintln(&b, "deps: ok")
	}

	seedOut, seedCode := t.runDbt(ctx, dir, "seed")
	fmt.Fprintf(&b, "seed: %s\n", summarizeDbt(seedOut, seedCode))

	runOut, runCode := t.runDbt(ctx, dir, "run")
	fmt.Fprintf(&b, "run:  %s\n", summarizeDbt(runOut, runCode))

	if runCode != 0 {
		fmt.Fprintf(&b, "\nBUILD FAILED — the project does not compile as a whole.\n%s\n",
			tailOf(runOut, dbtLogTail))
		return &shuttle.Result{
			Success:         true,
			Data:            b.String(),
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	models := t.authoredModels(dir)
	if len(models) == 0 {
		fmt.Fprintln(&b, "\nNo recently written models found — grain report skipped.")
	} else {
		fmt.Fprintf(&b, "\nGRAIN — models you wrote (%d)\n", len(models))
		if len(models) > dbtMaxReported {
			models = models[:dbtMaxReported]
		}
		for _, m := range models {
			fmt.Fprint(&b, t.grainOf(ctx, m))
		}
	}

	return &shuttle.Result{
		Success:         true,
		Data:            b.String(),
		ExecutionTimeMs: time.Since(start).Milliseconds(),
	}, nil
}

// resolveProject finds the directory holding dbt_project.yml.
func (t *DbtBuildTool) resolveProject(params map[string]interface{}) (string, error) {
	if raw, ok := params["project_dir"].(string); ok && strings.TrimSpace(raw) != "" {
		dir := strings.TrimSpace(raw)
		if _, err := os.Stat(filepath.Join(dir, "dbt_project.yml")); err == nil {
			return dir, nil
		}
		return "", fmt.Errorf("no dbt_project.yml in %s", dir)
	}
	roots := []string{t.baseDir, "/app", "."}
	for _, root := range roots {
		if root == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, "dbt_project.yml")); err == nil {
			return root, nil
		}
		var found string
		_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil || found != "" {
				return nil
			}
			if !d.IsDir() && d.Name() == "dbt_project.yml" {
				found = filepath.Dir(p)
			}
			return nil
		})
		if found != "" {
			return found, nil
		}
	}
	return "", fmt.Errorf("no dbt_project.yml found under %s", strings.Join(roots, ", "))
}

// runDbt runs one dbt subcommand over the whole project and returns its
// combined output and exit code. No selector, ever — that is the contract.
func (t *DbtBuildTool) runDbt(ctx context.Context, dir, sub string) (string, int) {
	cctx, cancel := context.WithTimeout(ctx, dbtStepTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "dbt", sub, "--profiles-dir", dir) // #nosec G204 -- sub is a literal
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		code = 1
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
	}
	return string(out), code
}

var dbtDoneRe = regexp.MustCompile(`Done\.\s+(PASS=\d+\s+WARN=\d+\s+ERROR=\d+\s+SKIP=\d+[^\n]*)`)

// summarizeDbt reduces dbt's log to its verdict line.
func summarizeDbt(out string, code int) string {
	if m := dbtDoneRe.FindStringSubmatch(out); m != nil {
		return fmt.Sprintf("exit %d — %s", code, strings.TrimSpace(m[1]))
	}
	return fmt.Sprintf("exit %d", code)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}

func tailOf(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

// authoredModel is a model the caller wrote, with the sources it reads.
type authoredModel struct {
	name    string
	sources []string
}

var (
	refRe    = regexp.MustCompile(`(?i)\{\{\s*ref\(\s*['"]([a-z0-9_]+)['"]`)
	sourceRe = regexp.MustCompile(`(?i)\{\{\s*source\(\s*['"][a-z0-9_]+['"]\s*,\s*['"]([a-z0-9_]+)['"]`)
)

// authoredModels lists model files written inside the authoring window, newest
// first. Project furniture predates the session; the caller's work does not.
func (t *DbtBuildTool) authoredModels(dir string) []authoredModel {
	type entry struct {
		m   authoredModel
		mod time.Time
	}
	var found []entry
	cutoff := time.Now().Add(-dbtAuthoredWindow)
	_ = filepath.WalkDir(filepath.Join(dir, "models"), func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".sql") {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil || info.ModTime().Before(cutoff) {
			return nil
		}
		body, rerr := os.ReadFile(p) // #nosec G304 -- walking a project dir
		if rerr != nil {
			return nil
		}
		seen := map[string]bool{}
		var srcs []string
		for _, m := range refRe.FindAllStringSubmatch(string(body), -1) {
			if !seen[m[1]] {
				seen[m[1]] = true
				srcs = append(srcs, m[1])
			}
		}
		for _, m := range sourceRe.FindAllStringSubmatch(string(body), -1) {
			if !seen[m[1]] {
				seen[m[1]] = true
				srcs = append(srcs, m[1])
			}
		}
		name := strings.TrimSuffix(filepath.Base(p), ".sql")
		found = append(found, entry{authoredModel{name: name, sources: srcs}, info.ModTime()})
		return nil
	})
	sort.Slice(found, func(i, j int) bool { return found[i].mod.After(found[j].mod) })
	out := make([]authoredModel, 0, len(found))
	for _, e := range found {
		out = append(out, e.m)
	}
	return out
}

// grainOf reports a model's row count against each source it reads, and whether
// its numeric totals survive the joins.
func (t *DbtBuildTool) grainOf(ctx context.Context, m authoredModel) string {
	rows, ok := t.scalar(ctx, fmt.Sprintf("SELECT count(*) AS v FROM %s", m.name))
	if !ok {
		return fmt.Sprintf("  %s — not queryable\n", m.name)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "  %s  rows=%s\n", m.name, fmtNum(rows))

	measures := t.numericColumns(ctx, m.name)
	for _, src := range m.sources {
		srows, sok := t.scalar(ctx, fmt.Sprintf("SELECT count(*) AS v FROM %s", src))
		if !sok {
			continue
		}
		line := fmt.Sprintf("    vs %s: rows=%s", src, fmtNum(srows))
		if srows > 0 {
			ratio := rows / srows
			line += fmt.Sprintf("  ratio=%.2f", ratio)
			if r := nearestInt(ratio); r >= 2 && absf(ratio-float64(r)) < dbtRatioEpsilon {
				line += fmt.Sprintf("  ** exactly %dx — join fan-out **", r)
			}
		}
		fmt.Fprintln(&b, line)

		for _, col := range measures {
			mine, mok := t.scalar(ctx, fmt.Sprintf("SELECT sum(%s) AS v FROM %s", col, m.name))
			theirs, tok := t.scalar(ctx, fmt.Sprintf("SELECT sum(%s) AS v FROM %s", col, src))
			if !mok || !tok || theirs == 0 {
				continue
			}
			drift := absf(mine-theirs) / absf(theirs)
			if drift > dbtSumTolerance {
				fmt.Fprintf(&b, "      %s: %s here vs %s in %s  ** %.2fx — not conserved **\n",
					col, fmtNum(mine), fmtNum(theirs), src, mine/theirs)
			}
		}
	}
	return b.String()
}

// numericColumns returns the model's numeric columns, bounded.
func (t *DbtBuildTool) numericColumns(ctx context.Context, table string) []string {
	res, err := t.backend.ExecuteQuery(ctx, fmt.Sprintf(
		`SELECT column_name FROM information_schema.columns
		 WHERE lower(table_name) = lower('%s')
		   AND lower(data_type) SIMILAR TO '%%(int|decimal|numeric|double|float|real|bigint)%%'
		 ORDER BY ordinal_position`, table))
	if err != nil || res == nil {
		return nil
	}
	var out []string
	for _, row := range res.Rows {
		v, ok := row["column_name"]
		if !ok || v == nil {
			continue
		}
		out = append(out, fmt.Sprint(v))
		if len(out) >= 6 {
			break
		}
	}
	return out
}

// scalar runs a one-value query.
func (t *DbtBuildTool) scalar(ctx context.Context, q string) (float64, bool) {
	res, err := t.backend.ExecuteQuery(ctx, q)
	if err != nil || res == nil || len(res.Rows) == 0 {
		return 0, false
	}
	raw, ok := res.Rows[0]["v"]
	if !ok || raw == nil {
		return 0, false
	}
	switch v := raw.(type) {
	case float64:
		return v, true
	case int64:
		return float64(v), true
	case int:
		return float64(v), true
	default:
		f, err := strconv.ParseFloat(strings.TrimSpace(fmt.Sprint(v)), 64)
		if err != nil {
			return 0, false
		}
		return f, true
	}
}

func nearestInt(f float64) int { return int(f + 0.5) }

func absf(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

func fmtNum(f float64) string {
	if f == float64(int64(f)) {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'f', 2, 64)
}
