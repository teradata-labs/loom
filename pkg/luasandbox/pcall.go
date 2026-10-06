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
	"strings"

	rt "github.com/arnodel/golua/runtime"
)

// golua runs each pcall in a child resource context. A termination raised
// inside it (a budget kill, or our own cancellation) only kills the child, so
// the stock pcall returns false and the script carries on. Measured: three
// nested pcalls that swallow errors kept a cancelled run spinning until its
// wall limit. These replacements are golua's pcall/xpcall plus one step:
// after the protected call returns, any termination is raised again in the
// parent, and so on up to the root. Ordinary Lua errors still return false.

func (s *run) pcall(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
	if err := c.Check1Arg(); err != nil {
		return nil, err
	}
	return s.protected(t, c, c.Arg(0), c.Etc(), nil)
}

func (s *run) xpcall(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
	if err := c.CheckNArgs(2); err != nil {
		return nil, err
	}
	var handler rt.Callable
	if !c.Arg(1).IsNil() {
		h, err := c.CallableArg(1)
		if err != nil {
			return nil, err
		}
		handler = h
	}
	return s.protected(t, c, c.Arg(0), c.Etc(), handler)
}

func (s *run) protected(t *rt.Thread, c *rt.GoCont, f rt.Value, args []rt.Value, handler rt.Callable) (rt.Cont, error) {
	next := c.Next()
	res := rt.NewTerminationWith(c, 0, true)
	child, err := t.CallContext(rt.RuntimeContextDef{MessageHandler: handler}, func() error {
		return rt.Call(t, f, args, res)
	})
	if termErr := s.afterProtected(t, child, err); termErr != nil {
		return nil, termErr
	}
	if err != nil {
		t.Push1(next, rt.BoolValue(false))
		t.Push1(next, rt.ErrorValue(err))
	} else {
		t.Push1(next, rt.BoolValue(true))
		t.Push(next, res.Etc()...)
	}
	return next, nil
}

// afterProtected raises again, in the current (parent) context, any
// termination that happened inside a protected call.
func (s *run) afterProtected(t *rt.Thread, child rt.RuntimeContext, err error) error {
	if s.term.outcome != "" {
		return s.terminate(t, s.term.outcome, s.term.limit, s.term.msg)
	}
	if child != nil && child.Status() == rt.StatusKilled {
		msg := "killed"
		if err != nil {
			msg = err.Error()
		}
		return s.terminate(t, OutcomeBudgetExceeded, limitFromMessage(msg), msg)
	}
	return s.checkCancel(t)
}

// limitFromMessage maps golua's limit messages ("memory limit of N
// exceeded", "CPU limit of N exceeded", "time limit of N exceeded") to budget
// names. A test pins these formats so an interpreter upgrade cannot change
// them silently.
func limitFromMessage(msg string) string {
	switch {
	case strings.Contains(msg, "memory limit"):
		return LimitMemory
	case strings.Contains(msg, "CPU limit"):
		return LimitCPU
	case strings.Contains(msg, "time limit"):
		return LimitWall
	}
	return ""
}
