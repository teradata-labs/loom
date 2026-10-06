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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/arnodel/golua/lib/base"
	"github.com/arnodel/golua/lib/mathlib"
	"github.com/arnodel/golua/lib/packagelib"
	"github.com/arnodel/golua/lib/stringlib"
	"github.com/arnodel/golua/lib/tablelib"
	"github.com/arnodel/golua/lib/utf8lib"
	rt "github.com/arnodel/golua/runtime"
)

// baseGlobals are the base-library functions a script gets. pcall and xpcall
// are replaced (pcall.go). Left out on purpose: load, dofile, loadfile
// (loading code and files), collectgarbage (steering the collector) and warn.
var baseGlobals = []string{
	"_VERSION", "assert", "error", "getmetatable", "ipairs", "next", "pairs",
	"print", "rawequal", "rawget", "rawlen", "rawset", "select",
	"setmetatable", "tonumber", "tostring", "type",
}

// libraries are loaded fresh into every runtime: their loaders create new
// function values each time and keep their state in the runtime.
var libraries = []packagelib.Loader{stringlib.LibLoader, tablelib.LibLoader, mathlib.LibLoader, utf8lib.LibLoader}

var (
	baseOnce   sync.Once
	baseValues map[string]rt.Value
)

// sharedBase loads golua's base library once and returns its functions.
//
// base.Load marks package-level function values shared by every runtime
// (next, the ipairs iterator) each time it runs. Loading it per runtime is a
// data race whenever two runs start at once, or one starts while another
// iterates a table. Loading it once, then copying the function values into
// each runtime, writes those flags exactly once. The values are safe to share:
// each receives its thread, and so its runtime, as an argument.
func sharedBase() map[string]rt.Value {
	baseOnce.Do(func() {
		tmpl := rt.New(io.Discard)
		_, _ = base.Load(tmpl)
		env := tmpl.GlobalEnv()
		baseValues = make(map[string]rt.Value, len(baseGlobals))
		for _, name := range baseGlobals {
			baseValues[name] = env.Get(rt.StringValue(name))
		}
	})
	return baseValues
}

// newRuntime builds a fresh interpreter for one run. Only pure libraries are
// present: base, string, table, math and utf8. coroutine is never loaded
// (golua runs each coroutine on its own goroutine, and suspended ones survive
// Runtime.Close), nor are package/require, io, os, debug, the Go bridge or
// runtime control.
func (s *run) newRuntime() (*rt.Runtime, func()) {
	r := rt.New(s.out)
	r.SetWarner(warner{s.out})
	env := r.GlobalEnv()
	for name, v := range sharedBase() {
		r.SetEnv(env, name, v)
	}
	r.SetEnv(env, "_G", rt.TableValue(env))
	var cleanups []func()
	for _, l := range libraries {
		pkg, cleanup := l.Load(r)
		r.SetEnv(env, l.Name, pkg)
		if cleanup != nil {
			cleanups = append(cleanups, cleanup)
		}
	}
	if str, ok := env.Get(rt.StringValue("string")).TryTable(); ok {
		r.SetEnv(str, "dump", rt.NilValue) // bytecode is useless without load
	}
	return r, func() {
		for _, c := range cleanups {
			c()
		}
	}
}

// installGlobals adds the host-provided API. It runs before the run's
// context opens; values that depend on the program (args, script) are set
// inside it so they are charged to the budget.
func (s *run) installGlobals() {
	r := s.r
	env := r.GlobalEnv()

	s.register(env, "pcall", 1, true, s.pcall)
	s.register(env, "xpcall", 2, true, s.xpcall)
	r.SetEnv(env, "log", env.Get(rt.StringValue("print")))

	s.installTools(env)

	meta := rt.NewTable()
	nullStr := r.SetEnvGoFunc(meta, "__tostring", func(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
		return c.PushingNext1(t.Runtime, rt.StringValue("null")), nil
	}, 1, false)
	nullStr.SolemnlyDeclareCompliance(allCompliance)
	r.SetEnv(meta, "__name", rt.StringValue("json.null"))
	s.null = rt.NewUserData(struct{}{}, meta)

	jsonTbl := rt.NewTable()
	s.register(jsonTbl, "encode", 1, false, s.jsonEncode)
	s.register(jsonTbl, "decode", 1, false, s.jsonDecode)
	r.SetEnv(jsonTbl, "null", rt.UserDataValue(s.null))
	r.SetEnv(env, "json", rt.TableValue(jsonTbl))

	timeTbl := rt.NewTable()
	s.register(timeTbl, "now", 0, false, s.timeNow)
	s.register(timeTbl, "unix", 0, false, s.timeUnix)
	s.register(timeTbl, "sleep", 1, false, s.timeSleep)
	r.SetEnv(env, "time", rt.TableValue(timeTbl))
}

// installProgram sets the globals that carry the program's inputs. It must run
// inside the run's context.
func (s *run) installProgram(t *rt.Thread, p Program) {
	conv := &luaConv{t: t, null: rt.UserDataValue(s.null)}
	args := p.Args
	if args == nil {
		args = map[string]any{}
	}
	t.SetEnv(s.r.GlobalEnv(), "args", conv.value(args, 0))

	info := map[string]any{}
	for k, v := range p.Info {
		info[k] = v
	}
	info["name"] = s.chunk
	t.SetEnv(s.r.GlobalEnv(), "script", conv.value(info, 0))
}

// json.encode(value) -> string
func (s *run) jsonEncode(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
	if err := c.Check1Arg(); err != nil {
		return nil, err
	}
	conv := newGoConv(s.lim.MaxCallResultBytes, false, s.null)
	v, err := conv.convert(c.Arg(0))
	if err != nil {
		return nil, fmt.Errorf("json.encode: %v", err)
	}
	b, err := encodeJSON(v)
	if err != nil {
		return nil, fmt.Errorf("json.encode: %v", err)
	}
	t.RequireBytes(len(b))
	t.RequireCPU(uint64(len(b)/8) + 1)
	return c.PushingNext1(t.Runtime, rt.StringValue(string(b))), nil
}

// json.decode(string) -> value. Object fields that are null are dropped;
// null array elements become json.null so positions survive.
func (s *run) jsonDecode(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
	str, err := c.StringArg(0)
	if err != nil {
		return nil, errors.New("json.decode: bad argument #1 (expected a string)")
	}
	t.RequireCPU(uint64(len(str)/8) + 1)
	dec := json.NewDecoder(strings.NewReader(str))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("json.decode: invalid JSON: %v", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("json.decode: invalid JSON: unexpected data after the first value")
	}
	conv := &luaConv{t: t, null: rt.UserDataValue(s.null)}
	return c.PushingNext1(t.Runtime, conv.value(v, 0)), nil
}

// time.now() -> RFC 3339 UTC timestamp with milliseconds
func (s *run) timeNow(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
	return c.PushingNext1(t.Runtime, rt.StringValue(time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00"))), nil
}

// time.unix() -> seconds since the epoch, with millisecond precision
func (s *run) timeUnix(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
	return c.PushingNext1(t.Runtime, rt.FloatValue(float64(time.Now().UnixMilli())/1000)), nil
}

// time.sleep(ms) pauses for up to Limits.MaxSleep. It ends early, ending the
// run, when the run is cancelled or its wall budget runs out.
func (s *run) timeSleep(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
	ms, err := c.FloatArg(0)
	if err != nil || math.IsNaN(ms) || ms < 0 {
		return nil, errors.New("time.sleep: bad argument #1 (expected milliseconds >= 0)")
	}
	d := s.lim.MaxSleep
	if ms < float64(d/time.Millisecond) {
		d = time.Duration(ms * float64(time.Millisecond))
	}
	if d > 0 {
		timer := time.NewTimer(d)
		select {
		case <-timer.C:
		case <-s.ctx.Done():
			timer.Stop()
			if err := s.checkCancel(t); err != nil {
				return nil, err
			}
		}
	}
	s.progress(ProgressEvent{Kind: ProgressSleep, Millis: d.Milliseconds()})
	return c.Next(), nil
}
