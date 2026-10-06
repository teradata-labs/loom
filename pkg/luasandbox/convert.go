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
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"

	rt "github.com/arnodel/golua/runtime"
)

// maxValueDepth bounds nesting in both conversion directions. It also stops
// a table that contains itself.
const maxValueDepth = 32

// tableOverheadBytes is charged for every table this package creates; golua
// does not charge tables built from Go.
const tableOverheadBytes = 80

// maxSafeInt is the largest integer a float64 holds exactly.
const maxSafeInt = 1 << 53

// ---------------------------------------------------------------------------
// Lua -> Go
// ---------------------------------------------------------------------------

// goConv converts Lua values to JSON-compatible Go values. It reads tables
// raw, so no metamethod (and therefore no Lua code) runs during conversion.
//
// Size is approximated as JSON bytes. In strict mode exceeding the budget is
// an error; in truncate mode conversion keeps what fit and sets cut.
type goConv struct {
	budget    int
	limit     int
	truncate  bool
	cut       bool
	nonFinite int
	null      *rt.UserData
}

func newGoConv(limit int, truncate bool, null *rt.UserData) *goConv {
	return &goConv{budget: limit, limit: limit, truncate: truncate, null: null}
}

func (c *goConv) tooLarge() error {
	return fmt.Errorf("value is larger than %d bytes", c.limit)
}

// spend charges n bytes. It reports false when the budget is exhausted.
func (c *goConv) spend(n int) bool {
	c.budget -= n
	return c.budget >= 0
}

// convert converts one top-level value.
func (c *goConv) convert(v rt.Value) (any, error) {
	x, keep, err := c.value(v, 0)
	if err != nil {
		return nil, err
	}
	if !keep {
		c.cut = true
		return nil, nil
	}
	return x, nil
}

// value converts v. keep is false when v did not fit (truncate mode only).
func (c *goConv) value(v rt.Value, depth int) (x any, keep bool, err error) {
	switch v.Type() {
	case rt.NilType:
		return c.scalar(nil, 4)
	case rt.BoolType:
		return c.scalar(v.AsBool(), 5)
	case rt.IntType:
		return c.scalar(v.AsInt(), 20)
	case rt.FloatType:
		f := v.AsFloat()
		if math.IsNaN(f) || math.IsInf(f, 0) {
			c.nonFinite++
			return c.scalar(nil, 4)
		}
		return c.scalar(f, 24)
	case rt.StringType:
		return c.str(v.AsString())
	case rt.TableType:
		return c.table(v.AsTable(), depth+1)
	case rt.UserDataType:
		if u, ok := v.TryUserData(); ok && c.null != nil && u == c.null {
			return c.scalar(nil, 4)
		}
	}
	return nil, false, fmt.Errorf("cannot convert a %s to JSON", v.TypeName())
}

func (c *goConv) scalar(x any, size int) (any, bool, error) {
	if c.spend(size) {
		return x, true, nil
	}
	if c.truncate {
		return nil, false, nil
	}
	return nil, false, c.tooLarge()
}

func (c *goConv) str(s string) (any, bool, error) {
	room := c.budget
	if c.spend(len(s) + 2) {
		return s, true, nil
	}
	if !c.truncate {
		return nil, false, c.tooLarge()
	}
	// Keep a cut copy of a large string when there is real room for one.
	if room > 256 {
		c.cut = true
		c.budget = 0
		return truncateText(s, room-2), true, nil
	}
	return nil, false, nil
}

func (c *goConv) table(t *rt.Table, depth int) (any, bool, error) {
	if depth > maxValueDepth {
		return nil, false, fmt.Errorf("value is nested more than %d levels deep (does a table contain itself?)", maxValueDepth)
	}
	if !c.spend(2) {
		if c.truncate {
			return nil, false, nil
		}
		return nil, false, c.tooLarge()
	}
	count, maxIndex, sequence := 0, int64(0), true
	for k, _, ok := t.Next(rt.NilValue); ok && !k.IsNil(); k, _, ok = t.Next(k) {
		count++
		if i, isInt := k.TryInt(); isInt && i >= 1 {
			maxIndex = max(maxIndex, i)
		} else {
			sequence = false
		}
	}
	if count == 0 {
		return map[string]any{}, true, nil
	}
	if sequence && int64(count) == maxIndex {
		arr := make([]any, 0, count)
		for i := int64(1); i <= maxIndex; i++ {
			x, keep, err := c.value(t.Get(rt.IntValue(i)), depth)
			if err != nil {
				return nil, false, err
			}
			if !keep || !c.spend(1) {
				c.cut = true
				break
			}
			arr = append(arr, x)
		}
		return arr, true, nil
	}
	type entry struct {
		key string
		val rt.Value
	}
	entries := make([]entry, 0, count)
	for k, v, ok := t.Next(rt.NilValue); ok && !k.IsNil(); k, v, ok = t.Next(k) {
		ks, err := keyString(k)
		if err != nil {
			return nil, false, err
		}
		entries = append(entries, entry{ks, v})
	}
	// Sorted so truncation keeps the same keys on every run.
	slices.SortFunc(entries, func(a, b entry) int {
		switch {
		case a.key < b.key:
			return -1
		case a.key > b.key:
			return 1
		}
		return 0
	})
	obj := make(map[string]any, len(entries))
	for _, e := range entries {
		if !c.spend(len(e.key) + 4) {
			if !c.truncate {
				return nil, false, c.tooLarge()
			}
			c.cut = true
			break
		}
		x, keep, err := c.value(e.val, depth)
		if err != nil {
			return nil, false, err
		}
		if !keep {
			c.cut = true
			break
		}
		obj[e.key] = x
	}
	return obj, true, nil
}

func keyString(k rt.Value) (string, error) {
	switch k.Type() {
	case rt.StringType:
		return k.AsString(), nil
	case rt.IntType:
		return strconv.FormatInt(k.AsInt(), 10), nil
	case rt.FloatType:
		return strconv.FormatFloat(k.AsFloat(), 'g', -1, 64), nil
	case rt.BoolType:
		return strconv.FormatBool(k.AsBool()), nil
	}
	return "", fmt.Errorf("cannot use a %s as a JSON object key", k.TypeName())
}

// ---------------------------------------------------------------------------
// Go -> Lua
// ---------------------------------------------------------------------------

// luaConv converts JSON-compatible Go values to Lua values, charging every
// string and table to the running context's memory budget first. When the
// budget runs out golua terminates the run from inside the charge.
//
// It must only run on the VM goroutine, inside the run's context.
type luaConv struct {
	t    *rt.Thread
	null rt.Value
	cut  bool
}

func (c *luaConv) str(s string) rt.Value {
	c.t.RequireBytes(len(s))
	c.t.RequireCPU(uint64(len(s)/64) + 1)
	return rt.StringValue(s)
}

func (c *luaConv) newTable() *rt.Table {
	c.t.RequireBytes(tableOverheadBytes)
	return rt.NewTable()
}

func (c *luaConv) set(t *rt.Table, k string, v rt.Value) {
	c.t.SetTable(t, c.str(k), v)
}

func number(f float64) rt.Value {
	if f == math.Trunc(f) && math.Abs(f) <= maxSafeInt {
		return rt.IntValue(int64(f))
	}
	return rt.FloatValue(f)
}

// value converts v. Objects drop nil fields; arrays keep their positions
// with json.null.
func (c *luaConv) value(v any, depth int) rt.Value {
	if depth > maxValueDepth {
		c.cut = true
		return c.str(fmt.Sprintf("[nested more than %d levels deep]", maxValueDepth))
	}
	switch x := v.(type) {
	case nil:
		return rt.NilValue
	case bool:
		return rt.BoolValue(x)
	case string:
		return c.str(x)
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return rt.IntValue(i)
		}
		f, err := x.Float64()
		if err != nil {
			return c.str(x.String())
		}
		return number(f)
	case float64:
		return number(x)
	case float32:
		return number(float64(x))
	case int:
		return rt.IntValue(int64(x))
	case int8:
		return rt.IntValue(int64(x))
	case int16:
		return rt.IntValue(int64(x))
	case int32:
		return rt.IntValue(int64(x))
	case int64:
		return rt.IntValue(x)
	case uint8:
		return rt.IntValue(int64(x))
	case uint16:
		return rt.IntValue(int64(x))
	case uint32:
		return rt.IntValue(int64(x))
	case uint:
		return uintValue(uint64(x))
	case uint64:
		return uintValue(x)
	case []any:
		t := c.newTable()
		for i, e := range x {
			c.setIndex(t, i, c.elem(e, depth))
		}
		return rt.TableValue(t)
	case []map[string]any:
		t := c.newTable()
		for i, e := range x {
			c.setIndex(t, i, c.value(e, depth+1))
		}
		return rt.TableValue(t)
	case []string:
		t := c.newTable()
		for i, e := range x {
			c.setIndex(t, i, c.str(e))
		}
		return rt.TableValue(t)
	case map[string]any:
		t := c.newTable()
		for k, e := range x {
			if e == nil {
				continue
			}
			c.set(t, k, c.value(e, depth+1))
		}
		return rt.TableValue(t)
	case map[string]string:
		t := c.newTable()
		for k, e := range x {
			c.set(t, k, c.str(e))
		}
		return rt.TableValue(t)
	}
	return c.value(normalizeJSON(v), depth)
}

// elem converts an array element, keeping nil as json.null so positions
// survive.
func (c *luaConv) elem(e any, depth int) rt.Value {
	if e == nil {
		return c.null
	}
	return c.value(e, depth+1)
}

func (c *luaConv) setIndex(t *rt.Table, i int, v rt.Value) {
	c.t.SetTable(t, rt.IntValue(int64(i+1)), v)
}

func uintValue(u uint64) rt.Value {
	if u > math.MaxInt64 {
		return rt.FloatValue(float64(u))
	}
	return rt.IntValue(int64(u))
}

// normalizeJSON turns any Go value into plain JSON values by round-tripping
// it through encoding/json. Values encoding/json rejects become their %v
// text.
func normalizeJSON(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var out any
	if err := dec.Decode(&out); err != nil {
		return string(b)
	}
	return out
}

// sanitize replaces the leaves of v that encoding/json rejects (channels,
// functions, cyclic values) with their %v text, keeping the rest of the
// structure, so one bad field does not turn a whole result into text.
func sanitize(v any, depth int) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = sanitize(e, depth+1)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = sanitize(e, depth+1)
		}
		return out
	}
	if depth > maxValueDepth {
		return fmt.Sprintf("[nested more than %d levels deep]", maxValueDepth)
	}
	if _, err := json.Marshal(v); err != nil {
		return fmt.Sprintf("%v", v)
	}
	return v
}

// jsonSize returns the encoded size of a Go value, or -1 when it cannot be
// encoded.
func jsonSize(v any) (int, []byte) {
	if s, ok := v.(string); ok {
		return len(s), nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return -1, nil
	}
	return len(b), b
}
