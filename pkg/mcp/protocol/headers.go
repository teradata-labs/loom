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
// This file implements the SEP-2243 request-metadata header rules of the
// 2026-07-28 revision: x-mcp-header tool-parameter annotations and the
// header value encoding shared by Mcp-Name and Mcp-Param-* headers.
package protocol

import (
	"encoding/base64"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// HeaderParamPrefix is the wire prefix for headers mirrored from tool
// parameters: an x-mcp-header value of "Region" produces "Mcp-Param-Region".
const HeaderParamPrefix = "Mcp-Param-"

// HeaderParam describes one tool parameter annotated with x-mcp-header.
type HeaderParam struct {
	// Path is the chain of property names from the schema root to the
	// annotated property. The specification only permits chains that pass
	// exclusively through "properties" keys.
	Path []string
	// Name is the x-mcp-header value; the wire header is HeaderParamPrefix+Name.
	Name string
}

// ToolHeaderParams extracts the x-mcp-header annotations from a tool's
// inputSchema and validates them against the SEP-2243 constraints. A non-nil
// error means the tool definition is invalid and, per the specification,
// clients on the Streamable HTTP transport MUST exclude the tool from
// tools/list results.
func ToolHeaderParams(tool Tool) ([]HeaderParam, error) {
	if tool.InputSchema == nil {
		return nil, nil
	}
	var out []HeaderParam
	seen := map[string]bool{}
	if err := scanHeaderAnnotations(tool.InputSchema, nil, true, &out, seen); err != nil {
		return nil, err
	}
	return out, nil
}

// scanHeaderAnnotations walks the whole schema tree. onChain is true only
// while the walk has passed exclusively through "properties" keys from the
// root; an x-mcp-header found anywhere else invalidates the tool definition.
func scanHeaderAnnotations(node map[string]interface{}, path []string, onChain bool, out *[]HeaderParam, seen map[string]bool) error {
	if raw, ok := node["x-mcp-header"]; ok {
		name, isStr := raw.(string)
		if !isStr || name == "" {
			return fmt.Errorf("x-mcp-header must be a non-empty string, got %v", raw)
		}
		if !onChain || len(path) == 0 {
			return fmt.Errorf("x-mcp-header %q is not statically reachable via a properties chain", name)
		}
		if !isHeaderToken(name) {
			return fmt.Errorf("x-mcp-header %q is not a valid HTTP field-name token", name)
		}
		lower := strings.ToLower(name)
		if seen[lower] {
			return fmt.Errorf("x-mcp-header %q is not case-insensitively unique", name)
		}
		seen[lower] = true
		switch node["type"] {
		case "string", "integer", "boolean":
		default:
			return fmt.Errorf("x-mcp-header %q on non-primitive type %v (string, integer, boolean permitted)", name, node["type"])
		}
		*out = append(*out, HeaderParam{Path: append([]string(nil), path...), Name: name})
	}

	for key, val := range node {
		switch v := val.(type) {
		case map[string]interface{}:
			if key == "properties" {
				for propName, propVal := range v {
					child, isObj := propVal.(map[string]interface{})
					if !isObj {
						continue
					}
					childPath := append(append([]string(nil), path...), propName)
					if err := scanHeaderAnnotations(child, childPath, onChain, out, seen); err != nil {
						return err
					}
				}
				continue
			}
			// Any other object-valued keyword (items, oneOf entries via
			// arrays below, if/then/else, $defs, ...) breaks the chain.
			if err := scanHeaderAnnotations(v, nil, false, out, seen); err != nil {
				return err
			}
		case []interface{}:
			for _, item := range v {
				if child, isObj := item.(map[string]interface{}); isObj {
					if err := scanHeaderAnnotations(child, nil, false, out, seen); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

// jsMaxSafeInteger bounds integer header values per SEP-2243 (JavaScript's
// safe integer range).
const jsMaxSafeInteger = 1<<53 - 1

// HeaderValuesForCall computes the Mcp-Param-* headers for one tools/call
// from the tool's validated annotations and the call arguments. Absent and
// null values omit the header, as the specification requires. Values are
// encoded per EncodeHeaderValue.
func HeaderValuesForCall(params []HeaderParam, args map[string]interface{}) (map[string]string, error) {
	if len(params) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(params))
	for _, p := range params {
		val, present := lookupPath(args, p.Path)
		if !present || val == nil {
			continue
		}
		enc, err := encodeHeaderParamValue(val)
		if err != nil {
			return nil, fmt.Errorf("header parameter %q (path %s): %w", p.Name, strings.Join(p.Path, "."), err)
		}
		out[HeaderParamPrefix+p.Name] = enc
	}
	return out, nil
}

func lookupPath(args map[string]interface{}, path []string) (interface{}, bool) {
	var cur interface{} = args
	for _, seg := range path {
		m, isMap := cur.(map[string]interface{})
		if !isMap {
			return nil, false
		}
		next, exists := m[seg]
		if !exists {
			return nil, false
		}
		cur = next
	}
	return cur, true
}

func encodeHeaderParamValue(val interface{}) (string, error) {
	switch v := val.(type) {
	case string:
		return EncodeHeaderValue(v), nil
	case bool:
		if v {
			return "true", nil
		}
		return "false", nil
	case float64: // JSON numbers decode to float64
		if v != math.Trunc(v) || v > jsMaxSafeInteger || v < -jsMaxSafeInteger {
			return "", fmt.Errorf("integer value %v is fractional or outside the JavaScript safe range", v)
		}
		return strconv.FormatInt(int64(v), 10), nil
	case int:
		return strconv.Itoa(v), nil
	case int64:
		if v > jsMaxSafeInteger || v < -jsMaxSafeInteger {
			return "", fmt.Errorf("integer value %d outside the JavaScript safe range", v)
		}
		return strconv.FormatInt(v, 10), nil
	default:
		return "", fmt.Errorf("non-primitive value of type %T", val)
	}
}

const base64SentinelPrefix = "=?base64?"
const base64SentinelSuffix = "?="

// EncodeHeaderValue prepares a string for transmission as an HTTP header
// value per SEP-2243: values that are safe ASCII travel as-is; anything else
// (non-ASCII, control characters, leading/trailing whitespace, or a value
// that itself matches the Base64 sentinel pattern) is carried as
// =?base64?<encoded>?=.
func EncodeHeaderValue(s string) string {
	if headerValueSafe(s) && !matchesSentinel(s) {
		return s
	}
	return base64SentinelPrefix + base64.StdEncoding.EncodeToString([]byte(s)) + base64SentinelSuffix
}

func matchesSentinel(s string) bool {
	return strings.HasPrefix(s, base64SentinelPrefix) && strings.HasSuffix(s, base64SentinelSuffix)
}

// headerValueSafe reports whether s consists solely of visible ASCII, space,
// and tab, with no leading or trailing whitespace (RFC 9110 field values;
// edge whitespace is not round-trippable and therefore encoded).
func headerValueSafe(s string) bool {
	if s != strings.TrimSpace(s) {
		return false
	}
	for i := 0; i < len(s); i++ {
		b := s[i]
		if b == 0x09 || b == 0x20 {
			continue
		}
		if b < 0x21 || b > 0x7E {
			return false
		}
	}
	return true
}

// isHeaderToken reports whether s matches RFC 9110 field-name token syntax
// (1*tchar).
func isHeaderToken(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		b := s[i]
		switch {
		case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		case b == '!' || b == '#' || b == '$' || b == '%' || b == '&' || b == '\'' ||
			b == '*' || b == '+' || b == '-' || b == '.' || b == '^' || b == '_' ||
			b == '`' || b == '|' || b == '~':
		default:
			return false
		}
	}
	return true
}

// DecodeHeaderValue reverses EncodeHeaderValue: a Base64-sentinel value
// (=?base64?...?=) is decoded to its original UTF-8 string; anything else is
// returned as-is. Servers MUST decode encoded Mcp-Name and Mcp-Param values
// before comparing them to request-body values during server validation.
func DecodeHeaderValue(s string) (string, error) {
	if !matchesSentinel(s) {
		return s, nil
	}
	inner := strings.TrimSuffix(strings.TrimPrefix(s, base64SentinelPrefix), base64SentinelSuffix)
	decoded, err := base64.StdEncoding.DecodeString(inner)
	if err != nil {
		return "", fmt.Errorf("invalid Base64 sentinel header value: %w", err)
	}
	return string(decoded), nil
}
