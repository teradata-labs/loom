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
package shuttle

import (
	"fmt"
	"regexp"
	"strings"
)

// MatcherSpec is the raw matcher a binding carries (the HookBinding.Matcher
// field). ParamPath addresses the param to test and may traverse nested maps
// (a dispatch tool's carried op) using dot-separated segments. Op is the
// comparison against Value: "equals", "regex", or "contains".
type MatcherSpec struct {
	ParamPath string `mapstructure:"param_path" json:"param_path"`
	Op        string `mapstructure:"op" json:"op"`
	Value     string `mapstructure:"value" json:"value"`
}

// Matcher is a compiled param selector: a resolved param path plus a comparison
// op over Value. A Matcher with no path matches every call (an unfiltered
// binding governs every call to its scoped tools).
type Matcher struct {
	path       []string
	op         string
	value      string
	re         *regexp.Regexp
	matchesAll bool
}

// Compile turns a MatcherSpec into a Matcher, validating at serve. An empty
// spec compiles to a match-all Matcher. A non-empty spec requires a ParamPath
// and a known Op; a "regex" op compiles Value and a bad pattern is an error
// (fail-closed, L-Cfg-1).
func (s MatcherSpec) Compile() (Matcher, error) {
	if s.ParamPath == "" && s.Op == "" && s.Value == "" {
		return Matcher{matchesAll: true}, nil
	}
	if s.ParamPath == "" {
		return Matcher{}, fmt.Errorf("hook matcher: param_path is required")
	}
	m := Matcher{
		path:  strings.Split(s.ParamPath, "."),
		op:    s.Op,
		value: s.Value,
	}
	switch s.Op {
	case "equals", "contains":
	case "regex":
		re, err := regexp.Compile(s.Value)
		if err != nil {
			return Matcher{}, fmt.Errorf("hook matcher: invalid regex %q: %w", s.Value, err)
		}
		m.re = re
	case "":
		return Matcher{}, fmt.Errorf("hook matcher: op is required (want equals|regex|contains)")
	default:
		return Matcher{}, fmt.Errorf("hook matcher: unknown op %q (want equals|regex|contains)", s.Op)
	}
	return m, nil
}

// MatchesParams reports whether the call's params satisfy the matcher. A
// match-all matcher always matches. Otherwise the path is resolved through
// nested maps to a value that is compared, as a string, under the op; an
// unresolved path does not match.
func (m Matcher) MatchesParams(params map[string]interface{}) bool {
	if m.matchesAll {
		return true
	}
	v, ok := valueAtPath(params, m.path)
	if !ok {
		return false
	}
	s := valueToString(v)
	switch m.op {
	case "equals":
		return s == m.value
	case "contains":
		return strings.Contains(s, m.value)
	case "regex":
		return m.re != nil && m.re.MatchString(s)
	default:
		return false
	}
}

// valueAtPath walks path segments through nested string-keyed maps and returns
// the leaf value. Any segment whose container is not a string-keyed map, or a
// missing key, resolves to not-found.
func valueAtPath(params map[string]interface{}, path []string) (interface{}, bool) {
	if len(path) == 0 {
		return nil, false
	}
	var cur interface{} = params
	for _, seg := range path {
		m, ok := asStringMap(cur)
		if !ok {
			return nil, false
		}
		cur, ok = m[seg]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// asStringMap adapts the common nested-map shapes a decoded params tree takes
// (map[string]interface{} and map[string]string) to a uniform lookup.
func asStringMap(v interface{}) (map[string]interface{}, bool) {
	switch m := v.(type) {
	case map[string]interface{}:
		return m, true
	case map[string]string:
		out := make(map[string]interface{}, len(m))
		for k, val := range m {
			out[k] = val
		}
		return out, true
	default:
		return nil, false
	}
}

// valueToString renders a resolved leaf for comparison. A string is used as-is;
// any other scalar is formatted by its default representation.
func valueToString(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}
