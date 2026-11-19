package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Additional check kinds. Deterministic kinds are evaluated in the run.
// checkJevNotDefective is deferred to the judge pass, which runs after all
// arm runs so decider traffic never overlaps a latency measurement.
const (
	checkNotRegex             = "not_regex"               // final answer does not match Value
	checkToolInputNotContains = "tool_input_not_contains" // no tool call input contains Value
	checkLabelAccuracy        = "label_accuracy"          // Value "T-01=P1,..." meets Path (fraction)
	checkTimeRangeWithin      = "time_range_within"       // Value "start|end|minutes" (HH:MM)
	checkAllocValid           = "alloc_valid"             // Value "A:min:max,...|total"
	checkJevNotDefective      = "jev_not_defective"       // Value criterion name, Path source file; deferred
)

// isDeferred reports whether a check is scored by the judge pass, not in the run.
func isDeferred(kind string) bool { return kind == checkJevNotDefective }

// evalExtra evaluates the additional deterministic kinds.
func evalExtra(c check, ws, final string, rec *runRecord) (bool, string) {
	switch c.Kind {
	case checkNotRegex:
		re, err := regexp.Compile(c.Value)
		if err != nil {
			return false, "bad regex: " + err.Error()
		}
		return !re.MatchString(final), ""
	case checkToolInputNotContains:
		for _, tc := range rec.ToolCalls {
			b, err := json.Marshal(tc.Input)
			if err != nil {
				continue
			}
			if strings.Contains(string(b), c.Value) {
				return false, fmt.Sprintf("tool %s input contains %q", tc.Name, c.Value)
			}
		}
		return true, ""
	case checkLabelAccuracy:
		return labelAccuracy(c.Value, c.Path, final)
	case checkTimeRangeWithin:
		return timeRangeWithin(c.Value, final)
	case checkAllocValid:
		return allocValid(c.Value, final)
	case checkJevNotDefective:
		return false, "deferred to judge pass"
	}
	return false, "unknown check kind " + c.Kind
}

// labelAccuracy compares "T-01: P1" style lines with expected labels.
func labelAccuracy(spec, threshold, final string) (bool, string) {
	min, err := strconv.ParseFloat(threshold, 64)
	if err != nil {
		return false, "bad threshold: " + err.Error()
	}
	pairs := strings.Split(spec, ",")
	correct := 0
	for _, p := range pairs {
		id, want, ok := strings.Cut(p, "=")
		if !ok {
			return false, "bad label spec: " + p
		}
		re := regexp.MustCompile(`(?m)^.*\b` + regexp.QuoteMeta(id) + `\b.*$`)
		line := re.FindString(final)
		if line == "" {
			continue
		}
		m := regexp.MustCompile(`\bP([123])\b`).FindStringSubmatch(line)
		if m != nil && "P"+m[1] == want {
			correct++
		}
	}
	acc := float64(correct) / float64(len(pairs))
	return acc >= min, fmt.Sprintf("%d of %d correct (%.2f, gate %.2f)", correct, len(pairs), acc, min)
}

var timeRangeRe = regexp.MustCompile(`(\d{1,2}):(\d{2})\s*(?:-|–|—|to)\s*(\d{1,2}):(\d{2})`)

// timeRangeWithin checks the first "HH:MM-HH:MM" in the answer lies inside a
// window and lasts at least a minimum length.
func timeRangeWithin(spec, final string) (bool, string) {
	parts := strings.Split(spec, "|")
	if len(parts) != 3 {
		return false, "bad spec"
	}
	winStart, winEnd := hm(parts[0]), hm(parts[1])
	minLen, err := strconv.Atoi(parts[2])
	if err != nil {
		return false, "bad minutes: " + err.Error()
	}
	m := timeRangeRe.FindStringSubmatch(final)
	if m == nil {
		return false, "no time range in answer"
	}
	start := clock(m[1], m[2])
	end := clock(m[3], m[4])
	ok := start >= winStart && end <= winEnd && end-start >= minLen
	return ok, fmt.Sprintf("answer %02d:%02d-%02d:%02d, window %d-%d min", start/60, start%60, end/60, end%60, winStart, winEnd)
}

func hm(s string) int {
	h, mi, _ := strings.Cut(s, ":")
	return clock(h, mi)
}

func clock(h, m string) int {
	hh, _ := strconv.Atoi(h)
	mm, _ := strconv.Atoi(m)
	return hh*60 + mm
}

var allocLineRe = regexp.MustCompile(`(?m)^\s*[-*]?\s*(?:Team\s+)?([A-D])\b\D*?\$?(\d[\d,]*)`)

// allocValid checks one amount per team lies within its bounds and the sum
// does not exceed the total.
func allocValid(spec, final string) (bool, string) {
	halves := strings.Split(spec, "|")
	if len(halves) != 2 {
		return false, "bad spec"
	}
	total, err := strconv.Atoi(halves[1])
	if err != nil {
		return false, "bad total: " + err.Error()
	}
	type bound struct{ min, max int }
	bounds := map[string]bound{}
	var order []string
	for _, t := range strings.Split(halves[0], ",") {
		f := strings.Split(t, ":")
		if len(f) != 3 {
			return false, "bad team spec: " + t
		}
		lo, _ := strconv.Atoi(f[1])
		hi, _ := strconv.Atoi(f[2])
		bounds[f[0]] = bound{lo, hi}
		order = append(order, f[0])
	}

	got := map[string]int{}
	for _, m := range allocLineRe.FindAllStringSubmatch(final, -1) {
		if _, seen := got[m[1]]; seen {
			continue
		}
		v, err := strconv.Atoi(strings.ReplaceAll(m[2], ",", ""))
		if err != nil {
			continue
		}
		got[m[1]] = v
	}

	sum := 0
	var problems []string
	for _, team := range order {
		v, ok := got[team]
		if !ok {
			problems = append(problems, team+" missing")
			continue
		}
		sum += v
		b := bounds[team]
		if v < b.min || v > b.max {
			problems = append(problems, fmt.Sprintf("%s=%d outside [%d,%d]", team, v, b.min, b.max))
		}
	}
	if sum > total {
		problems = append(problems, fmt.Sprintf("sum %d exceeds %d", sum, total))
	}
	if len(problems) > 0 {
		return false, strings.Join(problems, "; ")
	}
	return true, fmt.Sprintf("sum %d of %d", sum, total)
}

// readWorkspaceFile returns a file from the run's workspace.
func readWorkspaceFile(ws, rel string) (string, error) {
	b, err := os.ReadFile(filepath.Join(ws, rel))
	return string(b), err
}
