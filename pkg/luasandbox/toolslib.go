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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	rt "github.com/teradata-labs/loom/third_party/golua/runtime"
)

// previewBytes is how much of an oversized structured result is kept as
// JSON text.
const previewBytes = 2 << 10

func (s *run) installTools(env *rt.Table) {
	tools := rt.NewTable()
	s.register(tools, "call", 2, false, s.toolsCall)
	s.register(tools, "must", 2, false, s.toolsMust)
	s.register(tools, "list", 0, false, s.toolsList)
	s.register(tools, "schema", 1, false, s.toolsSchema)
	s.r.SetEnv(env, "tools", rt.TableValue(tools))

	turn := rt.NewTable()
	s.register(turn, "result", 1, false, s.turnResult)
	s.r.SetEnv(env, "turn", rt.TableValue(turn))
}

// tools.call(name, args) -> {ok, data, text, error, ms, truncated, bytes}
func (s *run) toolsCall(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
	v, _, err := s.invokeTool(t, c, "tools.call")
	if err != nil {
		return nil, err
	}
	return c.PushingNext1(t.Runtime, v), nil
}

// tools.must(name, args) -> data, raising "<name>: <code>: <message>" on
// failure.
func (s *run) toolsMust(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
	v, res, err := s.invokeTool(t, c, "tools.must")
	if err != nil {
		return nil, err
	}
	if !res.OK {
		code, msg := errorParts(res)
		name, _ := c.StringArg(0)
		return nil, fmt.Errorf("%s: %s: %s", name, code, msg)
	}
	return c.PushingNext1(t.Runtime, v.AsTable().Get(rt.StringValue("data"))), nil
}

func errorParts(res *CallResult) (code, msg string) {
	if res.Error == nil {
		return CodeExecutionFailed, "the tool failed without an error message"
	}
	return res.Error.Code, res.Error.Message
}

// invokeTool validates arguments, charges the call budget, calls the host,
// and builds the result table.
func (s *run) invokeTool(t *rt.Thread, c *rt.GoCont, fn string) (rt.Value, *CallResult, error) {
	if err := c.Check1Arg(); err != nil {
		return rt.NilValue, nil, err
	}
	name, err := c.StringArg(0)
	if err != nil || name == "" {
		return rt.NilValue, nil, fmt.Errorf("%s: bad argument #1 (expected a tool name)", fn)
	}
	args := map[string]any{}
	if c.NArgs() >= 2 && !c.Arg(1).IsNil() {
		tbl, ok := c.Arg(1).TryTable()
		if !ok {
			return rt.NilValue, nil, fmt.Errorf("%s: bad argument #2 (expected a table of named arguments, got %s)", fn, c.Arg(1).TypeName())
		}
		conv := newGoConv(s.lim.MaxCallResultBytes, false, s.null).charged(t)
		x, err := conv.convert(rt.TableValue(tbl))
		if err != nil {
			return rt.NilValue, nil, fmt.Errorf("%s: arguments for %s: %v", fn, name, err)
		}
		m, ok := x.(map[string]any)
		if !ok {
			return rt.NilValue, nil, fmt.Errorf("%s: arguments for %s must be a table of named fields, not a list", fn, name)
		}
		args = m
	}
	return s.hostCall(t, name, func(ctx context.Context) (*CallResult, error) {
		return s.host.CallTool(ctx, name, args)
	})
}

// turn.result(message_id) -> same shape as tools.call
func (s *run) turnResult(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
	if err := c.Check1Arg(); err != nil {
		return nil, err
	}
	id, err := c.IntArg(0)
	if err != nil || id < 1 {
		return nil, errors.New("turn.result: bad argument #1 (expected a positive message id)")
	}
	v, _, err := s.hostCall(t, "turn.result", func(ctx context.Context) (*CallResult, error) {
		return s.host.TurnResult(ctx, id)
	})
	if err != nil {
		return nil, err
	}
	return c.PushingNext1(t.Runtime, v), nil
}

// hostCall runs one budgeted nested call. It is the only place nested calls
// reach the host.
func (s *run) hostCall(t *rt.Thread, tool string, call func(context.Context) (*CallResult, error)) (rt.Value, *CallResult, error) {
	s.toolCalls++
	if s.toolCalls > s.lim.MaxToolCalls {
		return rt.NilValue, nil, s.terminate(t, OutcomeBudgetExceeded, LimitToolCalls,
			fmt.Sprintf("tool call limit of %d exceeded", s.lim.MaxToolCalls))
	}
	seq := s.toolCalls
	s.progress(ProgressEvent{Kind: ProgressToolStarted, Tool: tool, Seq: seq, Max: s.lim.MaxToolCalls})

	callCtx, cancel := context.WithTimeout(s.ctx, s.lim.ToolCallTimeout)
	start := time.Now()
	var res *CallResult
	hostErr := s.callHost("CallTool", func() error {
		var err error
		res, err = call(callCtx)
		return err
	})
	timedOut := errors.Is(callCtx.Err(), context.DeadlineExceeded) && s.ctx.Err() == nil
	cancel()
	ms := time.Since(start).Milliseconds()

	// The run itself ending (cancelled, wall budget) wins over the call's
	// own result.
	if err := s.checkCancel(t); err != nil {
		return rt.NilValue, nil, err
	}
	switch {
	case timedOut && (hostErr != nil || res == nil || !res.OK):
		res = &CallResult{Error: &CallError{
			Code:       CodeToolCallTimeout,
			Message:    fmt.Sprintf("%s did not finish within %v", tool, s.lim.ToolCallTimeout),
			Suggestion: "ask for less data, or call the tool directly",
			Retryable:  true,
		}}
	case hostErr != nil:
		s.noteDetail(fmt.Sprintf("host error calling %s: %v", tool, hostErr))
		return rt.NilValue, nil, s.terminate(t, OutcomeHostError, "", "the host failed while calling "+tool)
	case res == nil:
		s.noteDetail("host returned a nil result for " + tool)
		return rt.NilValue, nil, s.terminate(t, OutcomeHostError, "", "the host failed while calling "+tool)
	}

	code := ""
	if !res.OK {
		code, _ = errorParts(res)
	}
	s.calls = append(s.calls, CallRecord{Tool: tool, OK: res.OK, Code: code, Millis: ms, Decision: res.Decision})
	v := s.resultTable(t, res, ms)
	s.progress(ProgressEvent{Kind: ProgressToolCompleted, Tool: tool, OK: res.OK, Code: code, Millis: ms, Seq: seq, Max: s.lim.MaxToolCalls})
	return v, res, nil
}

// resultTable builds {ok, data, text, error, ms, truncated, bytes}.
func (s *run) resultTable(t *rt.Thread, res *CallResult, ms int64) rt.Value {
	conv := &luaConv{t: t, null: rt.UserDataValue(s.null)}
	tbl := conv.newTable()
	conv.set(tbl, "ok", rt.BoolValue(res.OK))
	conv.set(tbl, "ms", rt.IntValue(ms))

	data, text, size, cut := s.prepareData(res.Data)
	if data != nil {
		conv.set(tbl, "data", conv.value(data, 0))
	}
	if text != nil {
		conv.set(tbl, "text", conv.str(*text))
	}
	if cut || conv.cut {
		s.trunc.CallResults = true
		conv.set(tbl, "truncated", rt.BoolValue(true))
		conv.set(tbl, "bytes", rt.IntValue(int64(size)))
	}
	if !res.OK {
		code, msg := errorParts(res)
		e := conv.newTable()
		conv.set(e, "code", conv.str(code))
		conv.set(e, "message", conv.str(msg))
		if res.Error != nil {
			if res.Error.Suggestion != "" {
				conv.set(e, "suggestion", conv.str(res.Error.Suggestion))
			}
			conv.set(e, "retryable", rt.BoolValue(res.Error.Retryable))
		}
		conv.set(tbl, "error", rt.TableValue(e))
	}
	return rt.TableValue(tbl)
}

// prepareData applies the per-call size limit and decodes JSON text.
//
// A string within the limit that holds a JSON object or array is decoded into
// data and kept verbatim in text. An oversized string is cut (head and tail
// kept). An oversized structured value is replaced by a summary with a JSON
// preview.
func (s *run) prepareData(d any) (data any, text *string, size int, cut bool) {
	limit := s.lim.MaxCallResultBytes
	if str, ok := d.(string); ok {
		size = len(str)
		if size > limit {
			cutStr := truncateText(str, limit)
			return cutStr, &cutStr, size, true
		}
		if decoded, ok := decodeJSONContainer(str); ok {
			return decoded, &str, size, false
		}
		return str, &str, size, false
	}
	if d == nil {
		return nil, nil, 0, false
	}
	size, encoded := jsonSize(d)
	if size < 0 {
		d = sanitize(d, 0)
		if str, ok := d.(string); ok {
			return s.prepareData(str)
		}
		if size, encoded = jsonSize(d); size < 0 {
			return s.prepareData(fmt.Sprintf("%v", d))
		}
	}
	if size > limit {
		preview := encoded[:min(previewBytes, len(encoded))]
		return map[string]any{
			"truncated": true,
			"bytes":     size,
			"preview":   string(trimToRuneEnd(preview)),
		}, nil, size, true
	}
	return d, nil, size, false
}

// decodeJSONContainer decodes s when it is exactly one JSON object or array.
func decodeJSONContainer(s string) (any, bool) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" || (trimmed[0] != '{' && trimmed[0] != '[') {
		return nil, false
	}
	dec := json.NewDecoder(strings.NewReader(trimmed))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, false // trailing data: not a single JSON value
	}
	return v, true
}

// tools.list() -> {{name, description}, ...}
func (s *run) toolsList(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
	if s.toolList == nil {
		ctx, cancel := context.WithTimeout(s.ctx, s.lim.ToolCallTimeout)
		var list []ToolInfo
		err := s.callHost("ListTools", func() error {
			var err error
			list, err = s.host.ListTools(ctx)
			return err
		})
		cancel()
		if cerr := s.checkCancel(t); cerr != nil {
			return nil, cerr
		}
		if err != nil {
			s.noteDetail(fmt.Sprintf("host error listing tools: %v", err))
			return nil, s.terminate(t, OutcomeHostError, "", "the host failed while listing tools")
		}
		if list == nil {
			list = []ToolInfo{}
		}
		s.toolList = list
	}
	conv := &luaConv{t: t, null: rt.UserDataValue(s.null)}
	out := conv.newTable()
	for i, ti := range s.toolList {
		e := conv.newTable()
		conv.set(e, "name", conv.str(ti.Name))
		conv.set(e, "description", conv.str(ti.Description))
		conv.setIndex(out, i, rt.TableValue(e))
	}
	return c.PushingNext1(t.Runtime, rt.TableValue(out)), nil
}

// tools.schema(name) -> JSON Schema table, or nil when the tool is not
// visible.
func (s *run) toolsSchema(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
	name, err := c.StringArg(0)
	if err != nil {
		return nil, errors.New("tools.schema: bad argument #1 (expected a tool name)")
	}
	ctx, cancel := context.WithTimeout(s.ctx, s.lim.ToolCallTimeout)
	var schema map[string]any
	herr := s.callHost("ToolSchema", func() error {
		var err error
		schema, err = s.host.ToolSchema(ctx, name)
		return err
	})
	cancel()
	if cerr := s.checkCancel(t); cerr != nil {
		return nil, cerr
	}
	if errors.Is(herr, ErrToolNotVisible) {
		return c.PushingNext1(t.Runtime, rt.NilValue), nil
	}
	if herr != nil {
		s.noteDetail(fmt.Sprintf("host error reading the schema of %s: %v", name, herr))
		return nil, s.terminate(t, OutcomeHostError, "", "the host failed while reading a tool schema")
	}
	conv := &luaConv{t: t, null: rt.UserDataValue(s.null)}
	return c.PushingNext1(t.Runtime, conv.value(schema, 0)), nil
}

// encodeJSON renders v without HTML escaping and without a trailing newline.
func encodeJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
