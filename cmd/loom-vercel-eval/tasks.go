package main

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// task is one entry of the task set in docs/plans/vercel-gateway-eval-tasks.md.
// Files is the fixture. Setup commands run in the workspace before the agent
// starts (harness side, not the agent). Expected values are computed in Go,
// never typed by hand, so a checker cannot disagree with its own fixture.
type task struct {
	ID       string
	Category string
	Summary  string
	Setup    []string
	Turns    []string
	Files    map[string]string
	Checks   []check
}

// allTasks returns the task set in plan order. Each category lives in its own
// file so the set stays readable.
func allTasks() []task {
	var ts []task
	ts = append(ts, codingTasks()...)
	ts = append(ts, dataTasks()...)
	ts = append(ts, memoryTasks()...)
	ts = append(ts, extractionTasks()...)
	ts = append(ts, reasoningTasks()...)
	ts = append(ts, robustnessTasks()...)
	return ts
}

// numberPattern matches want as a standalone number, with or without
// thousands separators: 2280 matches 2280 and 2,280.
func numberPattern(want string) string {
	var b strings.Builder
	for i, r := range want {
		if i > 0 && (len(want)-i)%3 == 0 {
			b.WriteString(",?")
		}
		b.WriteString(regexp.QuoteMeta(string(r)))
	}
	return `(^|[^0-9.,])` + b.String() + `([^0-9]|$)`
}

// numberCheck requires the final answer to contain want as a standalone number.
func numberCheck(want string) check {
	return check{Kind: checkRegex, Value: numberPattern(want)}
}

// notNumberCheck requires the final answer not to contain want as a standalone number.
func notNumberCheck(want string) check {
	return check{Kind: checkNotRegex, Value: numberPattern(want)}
}

// businessDays counts Monday–Friday days in [from, to] that are not holidays.
func businessDays(from, to time.Time, holidays map[string]bool) int {
	n := 0
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		if d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
			continue
		}
		if holidays[d.Format("2006-01-02")] {
			continue
		}
		n++
	}
	return n
}

func mustDate(s string) time.Time {
	t, err := time.Parse("2006-01-02", strings.TrimSpace(s))
	if err != nil {
		panic(err)
	}
	return t
}

func fmtInt(v int) string { return fmt.Sprint(v) }
