// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package builtin

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Batch mode for shell_execute: up to batchMaxCommands commands in one call,
// each in its own shell, all running in PARALLEL. The rendered text carries a
// section per command in submission order — always submission order, never
// completion order, so the same batch renders the same bytes every time and a
// cached prefix stays valid.
//
// Each command owns its own wall-clock budget (perCommandTimeoutSecs). Because
// they run concurrently the call itself lasts about as long as the slowest one,
// which is what keeps a batch clear of the conversation-cache lifetime. A
// command that overruns is killed on its own, the others are unaffected, and
// whatever it printed before the kill is still rendered.
//
// Every command is guaranteed a byte budget for its output; unspent bytes flow
// forward to later sections. A truncated section says how much was cut and how
// to narrow.

const (
	// defaultCommandBudget is the rendered-output guarantee per command. Large
	// enough to hold a whole source file of ordinary size; the fold is the
	// size control for the context, not the tool.
	defaultCommandBudget = 64 * 1024
	// batchMaxCommands is the parallelism of one call. Commands past this are
	// not run; the rendered text has a section only for those that were, which
	// is how the model sees what it got.
	batchMaxCommands = 5
	// perCommandTimeoutSecs is each command's own wall clock. Sized so that a
	// full batch still finishes inside the conversation-cache lifetime.
	perCommandTimeoutSecs = 270
	// exit codes GNU timeout reports when it stops a command: 124 after the
	// TERM, 137 (128+9) when the follow-up KILL was needed.
	exitTimedOutTerm = 124
	exitTimedOutKill = 137
	// batch markers: written by the driver around each command, on both
	// streams, and never seen by the model.
	batchMarkStart = "\x1e__LOOM_S__ "
	batchMarkEnd   = "\x1e__LOOM_E__ "
	batchMarkSkip  = "\x1e__LOOM_K__ "
)

type batchCmd struct {
	command string
	label   string
	budget  int
}

type batchSection struct {
	stdout  []string
	stderr  []string
	exit    int
	seen    bool
	skipped bool
}

// parseBatchCommands reads params["commands"]. Accepts an array of strings,
// an array of {command, label?, max_output_bytes?} objects, a single object,
// or a JSON string of either — trained habits are absorbed, not refused.
// More than batchMaxCommands entries are cut to the first batchMaxCommands
// rather than refused: the model reads which commands ran off the sections it
// gets back, so a short render is the whole message.
// Returns isBatch=false when the parameter is absent.
func parseBatchCommands(params map[string]interface{}, defaultBudget int) (cmds []batchCmd, isBatch bool, err error) {
	raw, ok := params["commands"]
	if !ok || raw == nil {
		return nil, false, nil
	}
	if s, isStr := raw.(string); isStr {
		trimmed := strings.TrimSpace(s)
		if strings.HasPrefix(trimmed, "[") || strings.HasPrefix(trimmed, "{") {
			var decoded interface{}
			if jerr := json.Unmarshal([]byte(trimmed), &decoded); jerr == nil {
				raw = decoded
			} else {
				raw = []interface{}{s}
			}
		} else {
			raw = []interface{}{s}
		}
	}
	if m, isMap := raw.(map[string]interface{}); isMap {
		raw = []interface{}{m}
	}
	items, isList := raw.([]interface{})
	if !isList || len(items) == 0 {
		return nil, true, fmt.Errorf("'commands' must be a non-empty array of shell command strings or {\"command\": ..., \"label\"?, \"max_output_bytes\"?} objects")
	}
	if len(items) > batchMaxCommands {
		items = items[:batchMaxCommands]
	}
	for i, it := range items {
		switch v := it.(type) {
		case string:
			cmds = append(cmds, batchCmd{command: v, label: fmt.Sprintf("command %d", i+1), budget: defaultBudget})
		case map[string]interface{}:
			c, _ := v["command"].(string)
			if c == "" {
				c, _ = v["cmd"].(string)
			}
			label, _ := v["label"].(string)
			if label == "" {
				label = fmt.Sprintf("command %d", i+1)
			}
			b := defaultBudget
			if mob, ok := v["max_output_bytes"].(float64); ok && mob > 0 {
				b = int(mob)
			}
			cmds = append(cmds, batchCmd{command: c, label: label, budget: b})
		default:
			return nil, true, fmt.Errorf("commands[%d] must be a shell command string or an object {\"command\": \"...\", \"label\"?: str, \"max_output_bytes\"?: int}", i)
		}
	}
	return cmds, true, nil
}

// buildBatchDriver renders the bash program that runs the batch. Every command
// is launched concurrently in its own shell, each writing to its own pair of
// files under a scratch directory, each wrapped in its own timeout so that one
// overrun cannot end the others. Once all have finished the driver replays them
// to the real streams in submission order, bracketed by the markers with the
// exit code — so the rendered bytes never depend on who finished first.
//
// A command's stdout and stderr are replayed to stdout and stderr respectively,
// which is what lets parseBatchOutput attribute each stream independently.
func buildBatchDriver(cmds []batchCmd) string {
	var b strings.Builder
	// The marker's leading control byte (0x1e) is written by printf's own
	// escape (\036) so it never passes through bash quoting.
	b.WriteString("__loom_mark() { printf '\\n\\036%s %s\\n' \"$1\" \"$2\"; printf '\\n\\036%s %s\\n' \"$1\" \"$2\" >&2; }\n")
	// Run each command under the interpreter we are already in, so bash-only
	// syntax keeps working; fall back to sh when $BASH is unset.
	b.WriteString("__loom_sh=${BASH:-sh}\n")
	// timeout(1) is coreutils and effectively always present; if it is not,
	// commands run unbounded rather than not at all.
	fmt.Fprintf(&b, "if command -v timeout >/dev/null 2>&1; then __loom_to=\"timeout -k 5 %d\"; else __loom_to=\"\"; fi\n", perCommandTimeoutSecs)
	b.WriteString("__loom_d=$(mktemp -d 2>/dev/null || echo \"/tmp/loom.$$\")\n")
	b.WriteString("mkdir -p \"$__loom_d\"\n")
	for i, c := range cmds {
		idx := strconv.Itoa(i)
		q := "'" + strings.ReplaceAll(c.command, "'", `'\''`) + "'"
		fmt.Fprintf(&b, "( $__loom_to \"$__loom_sh\" -c %s >\"$__loom_d/%s.o\" 2>\"$__loom_d/%s.e\"; printf %%s \"$?\" >\"$__loom_d/%s.r\" ) &\n",
			q, idx, idx, idx)
	}
	b.WriteString("wait\n")
	for i := range cmds {
		idx := strconv.Itoa(i)
		fmt.Fprintf(&b, "__loom_mark __LOOM_S__ %s\n", idx)
		fmt.Fprintf(&b, "cat \"$__loom_d/%s.o\" 2>/dev/null\n", idx)
		fmt.Fprintf(&b, "cat \"$__loom_d/%s.e\" >&2 2>/dev/null\n", idx)
		fmt.Fprintf(&b, "__loom_rc=$(cat \"$__loom_d/%s.r\" 2>/dev/null); [ -n \"$__loom_rc\" ] || __loom_rc=-1\n", idx)
		fmt.Fprintf(&b, "__loom_mark __LOOM_E__ \"%s $__loom_rc\"\n", idx)
	}
	b.WriteString("rm -rf \"$__loom_d\" 2>/dev/null\n")
	return b.String()
}

// parseBatchOutput splits the two captured streams into per-command sections
// by the driver's markers. A command with a start marker and no end marker
// (timeout, kill) is reported with exit -1.
func parseBatchOutput(stdout, stderr []string, n int) []batchSection {
	secs := make([]batchSection, n)
	feed := func(lines []string, toErr bool) {
		cur := -1
		for _, ln := range lines {
			switch {
			case strings.HasPrefix(ln, batchMarkStart):
				if i, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(ln, batchMarkStart))); err == nil && i >= 0 && i < n {
					cur = i
					secs[i].seen = true
					if !toErr {
						secs[i].exit = -1 // until the end marker says otherwise
					}
				}
			case strings.HasPrefix(ln, batchMarkEnd):
				f := strings.Fields(strings.TrimPrefix(ln, batchMarkEnd))
				if len(f) == 2 {
					if i, err := strconv.Atoi(f[0]); err == nil && i >= 0 && i < n {
						if rc, err2 := strconv.Atoi(f[1]); err2 == nil && !toErr {
							secs[i].exit = rc
						}
					}
				}
				cur = -1
			case strings.HasPrefix(ln, batchMarkSkip):
				if i, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(ln, batchMarkSkip))); err == nil && i >= 0 && i < n {
					secs[i].skipped = true
					secs[i].seen = true
				}
				cur = -1
			default:
				if cur >= 0 {
					if toErr {
						secs[cur].stderr = append(secs[cur].stderr, ln)
					} else {
						secs[cur].stdout = append(secs[cur].stdout, ln)
					}
				}
			}
		}
	}
	feed(stdout, false)
	feed(stderr, true)
	return secs
}

// trimMarkerBlank drops the blank line the markers add around a stream.
func trimMarkerBlank(lines []string) []string {
	for len(lines) > 0 && lines[0] == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// budgetOutput keeps the first budget bytes of s and, if anything was cut,
// says how much and how to narrow. Whole lines only.
func budgetOutput(s string, budget int) (string, int) {
	if budget <= 0 || len(s) <= budget {
		return s, 0
	}
	// The cut lands on a line end when one fits, otherwise on a rune
	// boundary: a byte-offset slice through a multi-byte rune puts invalid
	// UTF-8 into the result, which every downstream encoder then has to repair.
	end := budget
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	cut := s[:end]
	if i := strings.LastIndex(cut, "\n"); i > 0 {
		cut = cut[:i]
	}
	elided := len(s) - len(cut)
	return cut + fmt.Sprintf("\n[... %d bytes cut at a %d-byte budget — narrow the command: sed -n 'START,ENDp' FILE, grep -n PATTERN FILE, or raise max_output_bytes ...]", elided, budget), elided
}

// timedOutAdvice is the only place the tool talks about time. It is written for
// the moment a command has just been stopped, and it names the remedies in the
// order they should be tried: split the work first, and only raise the limit
// when the step genuinely cannot be split. afterSecs is the budget the command
// actually had — the batch's own per-command budget, or whatever a caller that
// raised timeout_seconds asked for — so the text never states a limit that did
// not apply.
func timedOutAdvice(afterSecs int) string {
	return fmt.Sprintf("TIMED OUT after %ds — this command was stopped; any output it had already"+
		" produced is above.\nSplit it into smaller steps and send them as one batch (up to %d run in"+
		" parallel, so several small steps cost no more than the slowest).\nIf it genuinely cannot be"+
		" split, run it as the only command in a call with timeout_seconds: <seconds>.",
		afterSecs, batchMaxCommands)
}

// renderBatch builds the one text the model reads: a section per command in
// submission order. Unspent budget flows forward to later commands.
func renderBatch(cmds []batchCmd, secs []batchSection, timedOut bool) (text string, failed int, firstExit int) {
	var b strings.Builder
	carry := 0
	for i, c := range cmds {
		if i > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "== %s ==\n", c.label)
		s := secs[i]
		if s.skipped {
			b.WriteString("SKIPPED")
			carry += c.budget
			continue
		}
		if !s.seen {
			if timedOut {
				b.WriteString("NOT RUN: the call was stopped before this command was replayed")
			} else {
				b.WriteString("NOT RUN")
			}
			carry += c.budget
			continue
		}
		budget := c.budget + carry
		out := strings.Join(trimMarkerBlank(s.stdout), "\n")
		out, cutBytes := budgetOutput(out, budget)
		used := len(out)
		if cutBytes == 0 && used < budget {
			carry = budget - used
		} else {
			carry = 0
		}
		wrote := out != ""
		b.WriteString(out)
		if errText := strings.Join(trimMarkerBlank(s.stderr), "\n"); errText != "" {
			errText, _ = budgetOutput(errText, 4*1024)
			if wrote {
				b.WriteString("\n")
			}
			b.WriteString("stderr:\n" + errText)
			wrote = true
		}
		if s.exit != 0 {
			failed++
			if firstExit == 0 {
				firstExit = s.exit
			}
			if wrote {
				b.WriteString("\n")
			}
			switch s.exit {
			case exitTimedOutTerm, exitTimedOutKill:
				b.WriteString(timedOutAdvice(perCommandTimeoutSecs))
			case -1:
				b.WriteString("ERROR: did not finish (stopped before it reported an exit code)")
			default:
				fmt.Fprintf(&b, "ERROR: exit %d", s.exit)
			}
		}
	}
	return b.String(), failed, firstExit
}
