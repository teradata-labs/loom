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
package protocol

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func schemaTool(schema map[string]interface{}) Tool {
	return Tool{Name: "t", InputSchema: schema}
}

func TestToolHeaderParams(t *testing.T) {
	tests := []struct {
		name      string
		schema    map[string]interface{}
		wantErr   bool
		wantCount int
	}{
		{
			name: "valid top-level annotation",
			schema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"region": map[string]interface{}{"type": "string", "x-mcp-header": "Region"},
					"query":  map[string]interface{}{"type": "string"},
				},
			},
			wantCount: 1,
		},
		{
			name: "valid nested properties chain",
			schema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"target": map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"region": map[string]interface{}{"type": "string", "x-mcp-header": "Region"},
						},
					},
				},
			},
			wantCount: 1,
		},
		{
			name: "integer and boolean types permitted",
			schema: map[string]interface{}{
				"properties": map[string]interface{}{
					"count": map[string]interface{}{"type": "integer", "x-mcp-header": "Count"},
					"flag":  map[string]interface{}{"type": "boolean", "x-mcp-header": "Flag"},
				},
			},
			wantCount: 2,
		},
		{
			name: "number type rejected",
			schema: map[string]interface{}{
				"properties": map[string]interface{}{
					"ratio": map[string]interface{}{"type": "number", "x-mcp-header": "Ratio"},
				},
			},
			wantErr: true,
		},
		{
			name: "annotation under items is not statically reachable",
			schema: map[string]interface{}{
				"properties": map[string]interface{}{
					"list": map[string]interface{}{
						"type": "array",
						"items": map[string]interface{}{
							"type": "string", "x-mcp-header": "Item",
						},
					},
				},
			},
			wantErr: true,
		},
		{
			name: "annotation under oneOf is not statically reachable",
			schema: map[string]interface{}{
				"properties": map[string]interface{}{
					"choice": map[string]interface{}{
						"oneOf": []interface{}{
							map[string]interface{}{"type": "string", "x-mcp-header": "Choice"},
						},
					},
				},
			},
			wantErr: true,
		},
		{
			name: "case-insensitive duplicate rejected",
			schema: map[string]interface{}{
				"properties": map[string]interface{}{
					"a": map[string]interface{}{"type": "string", "x-mcp-header": "Region"},
					"b": map[string]interface{}{"type": "string", "x-mcp-header": "region"},
				},
			},
			wantErr: true,
		},
		{
			name: "invalid token rejected",
			schema: map[string]interface{}{
				"properties": map[string]interface{}{
					"a": map[string]interface{}{"type": "string", "x-mcp-header": "Bad Header"},
				},
			},
			wantErr: true,
		},
		{
			name: "empty annotation rejected",
			schema: map[string]interface{}{
				"properties": map[string]interface{}{
					"a": map[string]interface{}{"type": "string", "x-mcp-header": ""},
				},
			},
			wantErr: true,
		},
		{
			name: "non-string annotation rejected",
			schema: map[string]interface{}{
				"properties": map[string]interface{}{
					"a": map[string]interface{}{"type": "string", "x-mcp-header": 7},
				},
			},
			wantErr: true,
		},
		{
			name:      "no annotations",
			schema:    map[string]interface{}{"type": "object"},
			wantCount: 0,
		},
		{
			name:      "nil schema",
			schema:    nil,
			wantCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ToolHeaderParams(schemaTool(tt.schema))
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Len(t, got, tt.wantCount)
		})
	}
}

func TestHeaderValuesForCall(t *testing.T) {
	params := []HeaderParam{
		{Path: []string{"region"}, Name: "Region"},
		{Path: []string{"target", "zone"}, Name: "Zone"},
		{Path: []string{"count"}, Name: "Count"},
		{Path: []string{"flag"}, Name: "Flag"},
	}

	t.Run("full extraction", func(t *testing.T) {
		got, err := HeaderValuesForCall(params, map[string]interface{}{
			"region": "us-west1",
			"target": map[string]interface{}{"zone": "z1"},
			"count":  float64(42),
			"flag":   true,
		})
		require.NoError(t, err)
		assert.Equal(t, map[string]string{
			"Mcp-Param-Region": "us-west1",
			"Mcp-Param-Zone":   "z1",
			"Mcp-Param-Count":  "42",
			"Mcp-Param-Flag":   "true",
		}, got)
	})

	t.Run("absent and null values omit the header", func(t *testing.T) {
		got, err := HeaderValuesForCall(params, map[string]interface{}{
			"region": nil,
		})
		require.NoError(t, err)
		assert.Empty(t, got)
	})

	t.Run("fractional number rejected", func(t *testing.T) {
		_, err := HeaderValuesForCall(params, map[string]interface{}{"count": 1.5})
		require.Error(t, err)
	})

	t.Run("out-of-safe-range integer rejected", func(t *testing.T) {
		_, err := HeaderValuesForCall(params, map[string]interface{}{"count": float64(1 << 54)})
		require.Error(t, err)
	})

	t.Run("non-primitive value rejected", func(t *testing.T) {
		_, err := HeaderValuesForCall(params, map[string]interface{}{
			"region": map[string]interface{}{"nested": true},
		})
		require.Error(t, err)
	})

	t.Run("unsafe string is base64-encoded", func(t *testing.T) {
		got, err := HeaderValuesForCall(params[:1], map[string]interface{}{"region": "Hello, 世界"})
		require.NoError(t, err)
		assert.Equal(t, "=?base64?SGVsbG8sIOS4lueVjA==?=", got["Mcp-Param-Region"])
	})
}

func TestEncodeHeaderValue(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"us-west1", "us-west1"},
		{"", ""},
		{"Hello, 世界", "=?base64?SGVsbG8sIOS4lueVjA==?="},
		{" padded ", "=?base64?IHBhZGRlZCA=?="},
		{"line1\nline2", "=?base64?bGluZTEKbGluZTI=?="},
		{"=?base64?literal?=", "=?base64?PT9iYXNlNjQ/bGl0ZXJhbD89?="},
		{"with space inside", "with space inside"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, EncodeHeaderValue(tt.in), "input %q", tt.in)
	}
}
