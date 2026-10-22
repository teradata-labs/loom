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
	"reflect"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
	"unsafe"

	rt "github.com/teradata-labs/loom/third_party/golua/runtime"
)

// cyclicMarker replaces a map or slice that contains itself.
const cyclicMarker = "[cyclic value]"

// depthMarker replaces a value nested deeper than maxValueDepth.
var depthMarker = fmt.Sprintf("[nested more than %d levels deep]", maxValueDepth)

// jsonMeasure walks a host value the way encoding/json would encode it, but
// without building the encoding. It counts bytes until the count passes
// limit and then stops walking, keeps only the first keep bytes (for a
// preview), and replaces cycles, over-deep values and values encoding/json
// rejects with the same markers sanitize uses. So the Go work and memory
// spent on an oversized or hostile host result are bounded by the limit, not
// by the result.
//
// Plain JSON values (maps of string to any, slices of any, strings, numbers,
// booleans, nil) are walked; any other Go type (a struct, a typed slice) is
// encoded with encoding/json one value at a time.
type jsonMeasure struct {
	limit int
	keep  int
	n     int
	out   []byte
	// flawed reports a cycle, an over-deep value, or a value encoding/json
	// rejects: the data must go through sanitize before it reaches Lua.
	flawed bool
	path   map[unsafe.Pointer]struct{}
}

func measureJSON(v any, limit, keep int) *jsonMeasure {
	m := &jsonMeasure{limit: limit, keep: keep, path: map[unsafe.Pointer]struct{}{}}
	m.value(v, 0)
	return m
}

// over reports whether the value is larger than the limit.
func (m *jsonMeasure) over() bool { return m.n > m.limit }

func (m *jsonMeasure) write(s string) {
	m.n += len(s)
	if room := m.keep - len(m.out); room > 0 {
		m.out = append(m.out, s[:min(room, len(s))]...)
	}
}

func (m *jsonMeasure) value(v any, depth int) {
	if m.over() {
		return
	}
	if depth > maxValueDepth {
		m.flawed = true
		m.quoted(depthMarker)
		return
	}
	switch x := v.(type) {
	case nil:
		m.write("null")
	case bool:
		m.write(strconv.FormatBool(x))
	case string:
		m.quoted(x)
	case int:
		m.write(strconv.FormatInt(int64(x), 10))
	case int64:
		m.write(strconv.FormatInt(x, 10))
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			m.flawed = true
			m.quoted(unencodable(x))
			return
		}
		m.leaf(x)
	case map[string]any:
		p := reflect.ValueOf(x).UnsafePointer()
		if !m.enter(p) {
			return
		}
		defer delete(m.path, p)
		m.write("{")
		first := true
		emit := func(k string, e any) {
			if !first {
				m.write(",")
			}
			first = false
			m.quoted(k)
			m.write(":")
			m.value(e, depth+1)
		}
		if len(m.out) < m.keep {
			// Only the preview needs encoding/json's sorted key order.
			for _, k := range slices.Sorted(mapKeys(x)) {
				if m.over() {
					break
				}
				emit(k, x[k])
			}
		} else {
			for k, e := range x {
				if m.over() {
					break
				}
				emit(k, e)
			}
		}
		m.write("}")
	case []any:
		if len(x) > 0 {
			p := unsafe.Pointer(unsafe.SliceData(x))
			if !m.enter(p) {
				return
			}
			defer delete(m.path, p)
		}
		m.write("[")
		for i, e := range x {
			if m.over() {
				break
			}
			if i > 0 {
				m.write(",")
			}
			m.value(e, depth+1)
		}
		m.write("]")
	default:
		m.leaf(v)
	}
}

// enter marks p as on the current path. It reports false, writing the cycle
// marker, when p is already on it.
func (m *jsonMeasure) enter(p unsafe.Pointer) bool {
	if _, seen := m.path[p]; seen {
		m.flawed = true
		m.quoted(cyclicMarker)
		return false
	}
	m.path[p] = struct{}{}
	return true
}

func (m *jsonMeasure) leaf(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		m.flawed = true
		m.quoted(unencodable(v))
		return
	}
	m.write(string(b))
}

// quoted writes s as encoding/json.Marshal does (HTML-escaped).
func (m *jsonMeasure) quoted(s string) {
	size := quotedLen(s)
	room := m.keep - len(m.out)
	if room > 0 {
		// Encode no more of s than the preview can hold; a string always
		// encodes.
		b, _ := json.Marshal(s[:min(len(s), room)])
		m.out = append(m.out, b[:min(len(b), room)]...)
	}
	m.n += size
}

// quotedLen is len(json.Marshal(s)) without allocating it.
func quotedLen(s string) int {
	n := 2
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			switch {
			case c == '"' || c == '\\' || c == '\n' || c == '\r' || c == '\t':
				n += 2
			case c < 0x20 || c == '<' || c == '>' || c == '&':
				n += 6
			default:
				n++
			}
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			n += 6 // �
		case r == ' ' || r == ' ':
			n += 6
		default:
			n += size
		}
		i += size
	}
	return n
}

func mapKeys(m map[string]any) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

// sanitizer replaces the parts of a host value that jsonMeasure flags with
// the same markers, keeping the rest of the structure, so one bad field does
// not turn a whole result into text. It only runs on values jsonMeasure has
// already bounded.
type sanitizer struct {
	path map[unsafe.Pointer]struct{}
}

func sanitize(v any) any {
	return (&sanitizer{path: map[unsafe.Pointer]struct{}{}}).value(v, 0)
}

func (s *sanitizer) value(v any, depth int) any {
	if depth > maxValueDepth {
		return depthMarker
	}
	switch x := v.(type) {
	case map[string]any:
		p := reflect.ValueOf(x).UnsafePointer()
		if _, seen := s.path[p]; seen {
			return cyclicMarker
		}
		s.path[p] = struct{}{}
		defer delete(s.path, p)
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = s.value(e, depth+1)
		}
		return out
	case []any:
		if len(x) > 0 {
			p := unsafe.Pointer(unsafe.SliceData(x))
			if _, seen := s.path[p]; seen {
				return cyclicMarker
			}
			s.path[p] = struct{}{}
			defer delete(s.path, p)
		}
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = s.value(e, depth+1)
		}
		return out
	}
	if _, err := json.Marshal(v); err != nil {
		return unencodable(v)
	}
	return v
}

// errTrailingJSON reports data after the first JSON value.
var errTrailingJSON = errors.New("unexpected data after the first value")

// decodeJSON decodes exactly one JSON value straight into Lua values. Each
// token is charged one CPU tick and each string and table its memory as it
// is built, so a large document stops at the run's budgets instead of first
// becoming a Go tree the budgets never see. Object fields that are null are
// dropped; null array elements become json.null.
func (c *luaConv) decodeJSON(s string) (rt.Value, error) {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	v, err := c.decodeValue(dec, 0)
	if err != nil {
		return rt.NilValue, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return rt.NilValue, errTrailingJSON
	}
	return v, nil
}

func (c *luaConv) decodeValue(dec *json.Decoder, depth int) (rt.Value, error) {
	tok, err := dec.Token()
	if err != nil {
		return rt.NilValue, eofAsSyntax(err)
	}
	c.t.RequireCPU(1)
	if depth > maxValueDepth {
		if err := c.skip(dec, tok); err != nil {
			return rt.NilValue, err
		}
		c.cut = true
		return c.str(depthMarker), nil
	}
	switch x := tok.(type) {
	case json.Delim:
		tbl := c.newTable()
		if x == '{' {
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return rt.NilValue, eofAsSyntax(err)
				}
				key, _ := kt.(string) // the decoder only yields string keys
				v, err := c.decodeValue(dec, depth+1)
				if err != nil {
					return rt.NilValue, err
				}
				if !v.IsNil() {
					c.set(tbl, key, v)
				}
			}
		} else {
			for i := 0; dec.More(); i++ {
				v, err := c.decodeValue(dec, depth+1)
				if err != nil {
					return rt.NilValue, err
				}
				if v.IsNil() {
					v = c.null
				}
				c.setIndex(tbl, i, v)
			}
		}
		if _, err := dec.Token(); err != nil { // the closing delimiter
			return rt.NilValue, eofAsSyntax(err)
		}
		return rt.TableValue(tbl), nil
	case string:
		return c.str(x), nil
	case json.Number:
		return c.value(x, depth), nil
	case bool:
		return rt.BoolValue(x), nil
	}
	return rt.NilValue, nil // null
}

// skip consumes the rest of a value whose first token is tok.
func (c *luaConv) skip(dec *json.Decoder, tok json.Token) error {
	if d, ok := tok.(json.Delim); !ok || (d != '{' && d != '[') {
		return nil
	}
	for open := 1; open > 0; {
		tok, err := dec.Token()
		if err != nil {
			return eofAsSyntax(err)
		}
		c.t.RequireCPU(1)
		if d, ok := tok.(json.Delim); ok {
			if d == '{' || d == '[' {
				open++
			} else {
				open--
			}
		}
	}
	return nil
}

// eofAsSyntax reports a document that ends inside a value as a syntax error,
// as encoding/json's Decode does.
func eofAsSyntax(err error) error {
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}
