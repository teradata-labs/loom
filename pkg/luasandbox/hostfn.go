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
	"errors"
	"fmt"
	"runtime/debug"

	rt "github.com/arnodel/golua/runtime"
)

type hostImpl func(t *rt.Thread, c *rt.GoCont) (rt.Cont, error)

// allCompliance is declared by every host function. golua refuses to call a
// Go function under a time, CPU or memory limit unless it declares the
// matching flag. The declaration is truthful: every host function honours the
// run's context, charges what it creates, and does no I/O except through the
// host.
const allCompliance = rt.ComplyCpuSafe | rt.ComplyMemSafe | rt.ComplyTimeSafe | rt.ComplyIoSafe

// register installs a host function on tbl.
func (s *run) register(tbl *rt.Table, name string, nArgs int, hasEtc bool, impl hostImpl) {
	f := s.r.SetEnvGoFunc(tbl, name, s.wrap(name, impl), nArgs, hasEtc)
	f.SolemnlyDeclareCompliance(allCompliance)
}

// wrap adds the checks every host function needs:
//
//   - a cancelled or expired run ends before the function does any work;
//   - golua's own termination panics pass through untouched;
//   - any other panic ends the run with OutcomeHostError instead of crashing
//     the process, and its value and stack go to RunResult.Detail.
func (s *run) wrap(name string, impl hostImpl) rt.GoFunctionFunc {
	return func(t *rt.Thread, c *rt.GoCont) (next rt.Cont, err error) {
		if err := s.checkCancel(t); err != nil {
			return nil, err
		}
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			if _, isTermination := rec.(rt.ContextTerminationError); isTermination {
				panic(rec)
			}
			s.noteDetail(fmt.Sprintf("%s panicked: %v\n%s", name, rec, debug.Stack()))
			next, err = nil, s.terminate(t, OutcomeHostError, "", name+": internal error")
		}()
		return impl(t, c)
	}
}

// terminate ends the run from the VM goroutine. It records why first, so the
// outcome never depends on parsing golua's message text, then raises golua's
// termination. pcall and xpcall re-raise it at every level.
//
// golua returns without raising when the current context is no longer live;
// callers must then return the error this function returns.
func (s *run) terminate(t *rt.Thread, outcome Outcome, limit, msg string) error {
	if s.term.outcome == "" {
		s.term = termReason{outcome: outcome, limit: limit, msg: msg}
	}
	t.Runtime.TerminateContext("%s", msg)
	return errors.New(msg)
}

// checkCancel ends the run when the caller cancelled it or its wall budget is
// spent. It returns nil when the run may continue.
func (s *run) checkCancel(t *rt.Thread) error {
	if s.ctx.Err() == nil {
		return nil
	}
	if s.parent.Err() != nil || s.parentDeadlineBinds {
		return s.terminate(t, OutcomeCancelled, "", s.cancelMessage())
	}
	return s.terminate(t, OutcomeBudgetExceeded, LimitWall, fmt.Sprintf("wall time limit of %v exceeded", s.lim.Wall))
}

func (s *run) cancelMessage() string {
	if cause := s.parent.Err(); cause != nil {
		return "run cancelled: " + cause.Error()
	}
	return "run cancelled: the caller's deadline passed"
}

func (s *run) noteDetail(msg string) {
	if s.detail != "" {
		s.detail += "\n"
	}
	s.detail += msg
}

// progress forwards an event to the host. A panicking Progress is recorded
// and otherwise ignored: progress is advisory and must not change a run's
// outcome.
func (s *run) progress(ev ProgressEvent) {
	defer func() {
		if rec := recover(); rec != nil {
			s.noteDetail(fmt.Sprintf("Host.Progress panicked: %v", rec))
		}
	}()
	s.host.Progress(ev)
}

// callHost runs one host method, converting a panic into an error.
func (s *run) callHost(method string, f func() error) (err error) {
	defer func() {
		if rec := recover(); rec != nil {
			s.noteDetail(fmt.Sprintf("Host.%s panicked: %v\n%s", method, rec, debug.Stack()))
			err = fmt.Errorf("host %s failed", method)
		}
	}()
	return f()
}
