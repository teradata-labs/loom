// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package luasandbox

import (
	"context"
	"fmt"
	"regexp"
	"runtime/debug"
	"strings"
	"time"

	rt "github.com/arnodel/golua/runtime"
)

// termReason records why the package itself ended a run.
type termReason struct {
	outcome Outcome
	limit   string
	msg     string
}

// run is the state of one execution. It is used by exactly one goroutine.
type run struct {
	parent context.Context // the caller's context
	ctx    context.Context // parent plus the wall deadline
	// parentDeadlineBinds is true when the caller's deadline comes before
	// the wall budget, so an expired deadline means "cancelled" rather than
	// "budget exceeded".
	parentDeadlineBinds bool
	lim                 Limits
	host                Host
	chunk               string

	r         *rt.Runtime
	out       *cappedWriter
	null      *rt.UserData
	toolList  []ToolInfo
	toolCalls int
	calls     []CallRecord
	trunc     Truncation
	term      termReason
	detail    string
}

// Run executes p against h within lim and returns how it went.
//
// Run executes on the caller's goroutine and returns when the script
// finishes, a limit fires, or ctx ends. It never panics and never returns
// nil. Cancellation of ctx is observed at the script's next host call or
// pcall return, and the CPU and wall budgets bound everything else.
func Run(ctx context.Context, p Program, lim Limits, h Host) (res *RunResult) {
	start := time.Now()
	lim = lim.Normalize()
	defer func() {
		if rec := recover(); rec != nil {
			// A Go panic inside the interpreter or a library function the
			// script called (golua's string.format "%p" without an argument
			// is one). The process survives; the run does not.
			res = &RunResult{
				Outcome: OutcomeEngineError,
				Error:   "the script triggered an internal error in the Lua interpreter",
				Detail:  fmt.Sprintf("luasandbox: panic: %v\n%s", rec, debug.Stack()),
			}
		}
		res.Error = boundError(res.Error)
		res.Used.WallMillis = time.Since(start).Milliseconds()
	}()

	chunk := p.Name
	if chunk == "" {
		chunk = "inline"
	}
	switch {
	case h == nil:
		return &RunResult{Outcome: OutcomeEngineError, Error: "internal error in the script engine", Detail: "luasandbox: nil Host"}
	case ctx.Err() != nil:
		return &RunResult{Outcome: OutcomeCancelled, Error: "run cancelled before it started: " + ctx.Err().Error()}
	case len(p.Source) > lim.MaxSourceBytes:
		return &RunResult{Outcome: OutcomeScriptError, Error: fmt.Sprintf("the script is %d bytes; the limit is %d", len(p.Source), lim.MaxSourceBytes)}
	}
	if err := checkSourceShape(chunk, p.Source); err != nil {
		return &RunResult{Outcome: OutcomeScriptError, Error: err.Error()}
	}

	runCtx, cancel := context.WithTimeout(ctx, lim.Wall)
	defer cancel()
	s := &run{parent: ctx, ctx: runCtx, lim: lim, host: h, chunk: chunk, out: newCappedWriter(lim.MaxOutputBytes)}
	if d, ok := ctx.Deadline(); ok && d.Before(start.Add(lim.Wall)) {
		s.parentDeadlineBinds = true
	}
	return s.execute(p)
}

func (s *run) execute(p Program) *RunResult {
	r, cleanup := s.newRuntime()
	s.r = r
	defer func() {
		cleanup()
		r.Close(nil)
	}()
	s.installGlobals()

	chunk, err := r.CompileAndLoadLuaChunk(s.chunk, []byte(p.Source), rt.TableValue(r.GlobalEnv()))
	if err != nil {
		return s.result(&RunResult{Outcome: OutcomeScriptError, Error: cleanError(err)})
	}

	// The run context carries the sooner of the caller's deadline and the
	// wall budget; golua enforces the same instant from inside the VM.
	deadline, _ := s.ctx.Deadline()
	wallMillis := uint64(max(time.Until(deadline).Milliseconds(), 1))
	thread := r.MainThread()
	var ret rt.Value
	ctx, err := thread.CallContext(rt.RuntimeContextDef{HardLimits: rt.RuntimeResources{
		Cpu:    s.lim.CPUTicks,
		Memory: s.lim.MemoryBytes,
		Millis: wallMillis,
	}}, func() error {
		s.installProgram(thread, p)
		v, err := rt.Call1(thread, rt.FunctionValue(chunk))
		ret = v
		return err
	})
	res := &RunResult{}
	if ctx != nil {
		used := ctx.UsedResources()
		res.Used = Usage{CPUTicks: used.Cpu, MemoryBytes: used.Memory}
	}

	switch {
	case s.term.outcome != "":
		// The engine ended the run. That stands even if golua reported a
		// clean return because its context had already closed.
		res.Outcome, res.Limit, res.Error = s.term.outcome, s.term.limit, s.term.msg
	case err == nil:
		res.Outcome = OutcomeOK
		conv := newGoConv(s.lim.MaxResultBytes, true, s.null)
		v, cerr := conv.convert(ret)
		if cerr != nil {
			res.Outcome = OutcomeScriptError
			res.Error = "cannot return this value: " + cerr.Error()
			break
		}
		res.Value = v
		res.Truncated.Value = conv.cut
		if conv.nonFinite > 0 {
			_, _ = fmt.Fprintf(s.out, "warning: %d NaN or infinite numbers in the return value became null\n", conv.nonFinite)
		}
	case ctx != nil && ctx.Status() == rt.StatusKilled:
		res.Outcome, res.Error = OutcomeBudgetExceeded, cleanError(err)
		res.Limit = limitFromMessage(res.Error)
		if res.Limit == LimitWall && s.parentDeadlineBinds {
			res.Outcome, res.Limit, res.Error = OutcomeCancelled, "", s.cancelMessage()
		}
	default:
		res.Outcome, res.Error = OutcomeScriptError, cleanError(err)
	}
	return s.result(res)
}

// result fills the fields every outcome shares.
func (s *run) result(res *RunResult) *RunResult {
	res.Output, res.Truncated.Output = s.out.String()
	res.Calls = s.calls
	res.Truncated.CallResults = s.trunc.CallResults
	res.Detail = s.detail
	return res
}

// cleanError drops golua's "error: " prefix.
func cleanError(err error) string {
	if err == nil {
		return ""
	}
	return strings.TrimPrefix(err.Error(), "error: ")
}

// maxErrorBytes bounds RunResult.Error. A script controls its error text
// (error(string.rep("x", 5e7)) is 50 MB) and hosts hand it to models and logs.
const maxErrorBytes = 2 << 10

// heapAddress matches the addresses golua prints for reference values.
var heapAddress = regexp.MustCompile(`\b(table|function|userdata|thread): 0x[0-9a-fA-F]+`)

// boundError removes heap addresses and caps the length.
func boundError(s string) string {
	s = heapAddress.ReplaceAllString(s, "$1")
	if len(s) <= maxErrorBytes {
		return s
	}
	return string(trimToRuneEnd([]byte(s[:maxErrorBytes]))) + fmt.Sprintf(" [... %d bytes elided]", len(s)-maxErrorBytes)
}
