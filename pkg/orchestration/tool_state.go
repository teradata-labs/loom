// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// Reserved parameters the engine supplies to every tool call it makes, so a
// tool knows where it is running and what the state before it handed forward.
const (
	ParamDir     = "loom_dir"
	ParamState   = "loom_state"
	ParamSignal  = "loom_signal"
	ParamMessage = "loom_message"
	// ParamRun is the id of the run a state belongs to. Every conversation of a
	// run is named from it (<run>-<agent>), so a state that has to name one — to
	// say which conversation another seat should start from — can.
	ParamRun = "loom_run"
)

// ForkVarPrefix names the run variable that tells a seat where to start from.
// `fork:<agent>` holds a session id; the agent's first turn in this run copies
// that conversation and continues it as its own. It is a variable, not a field
// of the spec, because the parent is only known at run time — it is a
// conversation some earlier state, or some earlier run, actually had.
const ForkVarPrefix = "fork:"

// SessionVarPrefix names the run variable that seats an agent in a
// conversation that already exists. `session:<agent>` holds a session id; the
// agent takes every turn of this run there, as itself — not a copy. It is how a
// loop's manager is the agent that planned its work: the one the person talked
// to, still holding that conversation, and still the one they are connected to
// when the loop needs them.
const SessionVarPrefix = "session:"

// ToolExecutor runs a tool by name. It is deliberately the shape of
// shuttle.Executor.Execute: a state machine's tool state is the same
// invocation an agent makes, minus the model that asked for it.
//
// Going through this rather than shelling out directly is what keeps one idea
// of what is permitted. A second execution path would be invisible to the
// permission chain, unsandboxed and unaudited — fine in a test, a hole in
// production.
type ToolExecutor interface {
	Execute(ctx context.Context, toolName string, params map[string]interface{}) (*shuttle.Result, error)
}

// runTool invokes a tool state or an edge's hook and returns its signal and
// output. The signal is read from the tool's output exactly as an agent's is,
// so a deterministic body is not a trusted one: an unexpected signal from a
// tool is unroutable in the same way.
func (e *StateMachineExecutor) runTool(ctx context.Context, t *loomv1.ToolState, stateName, signal, message string, vars map[string]string) (string, string, error) {
	if e.orchestrator.toolExecutor == nil {
		return "", "", fmt.Errorf("state %q calls tool %q but no ToolExecutor is configured", stateName, t.GetTool())
	}

	reserved := map[string]string{
		ParamDir:     e.dir,
		ParamState:   stateName,
		ParamSignal:  signal,
		ParamMessage: message,
		ParamRun:     e.workflowID,
	}

	params := map[string]interface{}{}
	for k, v := range reserved {
		params[k] = v
	}
	// A tool's arguments interpolate against the run: where it is working, and
	// whatever it has worked out so far. Without this a command cannot name the
	// directory it is meant to act on, which is most of what a process does.
	for k, v := range t.GetParams() {
		params[k] = interpolateRun(v, reserved, vars)
	}

	result, err := e.orchestrator.toolExecutor.Execute(ctx, t.GetTool(), params)
	if err != nil {
		return "", "", fmt.Errorf("state %q tool %q failed: %w", stateName, t.GetTool(), err)
	}
	if result != nil && !result.Success {
		return "", "", fmt.Errorf("state %q tool %q reported failure: %s", stateName, t.GetTool(), toolError(result))
	}

	return signalOf(toolOutput(result)), toolOutput(result), nil
}

// toolOutput is what a state produced, as text.
//
// A tool's result is not always the text it produced: a shell tool reports an
// envelope — exit code, stderr, working directory — with the command's own
// output inside it. Reading the envelope would make the signal the JSON of the
// wrapper, and every shell state would look like it emitted nothing usable.
// So the text a state produced is taken from the result's own output field
// when it has one, and only otherwise from the whole result.
func toolOutput(r *shuttle.Result) string {
	if r == nil || r.Data == nil {
		return ""
	}
	if s, ok := r.Data.(string); ok {
		return s
	}
	if m, ok := r.Data.(map[string]interface{}); ok {
		for _, field := range []string{"stdout", "output", "content", "text", "result"} {
			if v, ok := m[field].(string); ok && strings.TrimSpace(v) != "" {
				return v
			}
		}
	}
	if b, err := json.Marshal(r.Data); err == nil {
		return string(b)
	}
	return fmt.Sprintf("%v", r.Data)
}

// interpolateRun replaces {name} with the run's own values: the reserved ones
// the engine supplies, then anything the run has recorded for itself.
func interpolateRun(value string, reserved, vars map[string]string) string {
	for k, v := range reserved {
		value = strings.ReplaceAll(value, "{"+k+"}", v)
	}
	for k, v := range vars {
		value = strings.ReplaceAll(value, "{"+k+"}", v)
	}
	return value
}

// toolError is what a failed tool state parks with. A shell that exits non-zero
// says why on stderr; a park that keeps only the exit code sends whoever
// resumes it back to re-run the command just to read it.
func toolError(r *shuttle.Result) string {
	msg := "no error detail"
	if r.Error != nil {
		msg = r.Error.Message
	}
	if m, ok := r.Data.(map[string]interface{}); ok {
		for _, field := range []string{"stderr", "stdout"} {
			if v, ok := m[field].(string); ok && strings.TrimSpace(v) != "" {
				return msg + "\n" + field + ":\n" + tailOf(strings.TrimSpace(v), 1200)
			}
		}
	}
	return msg
}

// tailOf keeps the end of a text, where a failing command says what stopped it.
func tailOf(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

// validateToolCoverage reports the first tool a pattern names that cannot run.
// A machine that names no tools needs no executor at all.
func validateToolCoverage(sm *loomv1.StateMachinePattern, exec ToolExecutor) error {
	if exec != nil {
		return nil
	}
	for name, st := range sm.GetStates() {
		if t := st.GetTool(); t != nil {
			return fmt.Errorf("state %q calls tool %q but no ToolExecutor is configured", name, t.GetTool())
		}
		for signal, tr := range st.GetRoute() {
			if t := tr.GetNextTool(); t != nil {
				return fmt.Errorf("state %q signal %q computes its destination with tool %q but no ToolExecutor is configured", name, signal, t.GetTool())
			}
			if t := tr.GetHook(); t != nil {
				return fmt.Errorf("state %q signal %q fires tool %q but no ToolExecutor is configured", name, signal, t.GetTool())
			}
		}
	}
	return nil
}
