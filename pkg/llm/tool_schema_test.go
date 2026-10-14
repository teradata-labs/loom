package llm

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/xeipuuv/gojsonschema"
)

func TestNormalizeObjectToolSchemaPreservesProhibition(t *testing.T) {
	const input = `{"type":"object","properties":{"environment":{"type":"string"}},"required":["environment"],"allOf":[
		{"not":{"properties":{"environment":{"enum":["production"]}},"required":["environment"]}}
	]}`
	schema, err := shuttle.FromJSON([]byte(input))
	require.NoError(t, err)
	adapted, err := NormalizeObjectToolSchema(schema)
	require.NoError(t, err)
	for _, sample := range []map[string]interface{}{
		{"environment": "staging"},
		{"environment": "production"},
	} {
		original, err := gojsonschema.Validate(gojsonschema.NewStringLoader(input), gojsonschema.NewGoLoader(sample))
		require.NoError(t, err)
		result, err := gojsonschema.Validate(gojsonschema.NewGoLoader(adapted), gojsonschema.NewGoLoader(sample))
		require.NoError(t, err)
		assert.Equal(t, original.Valid(), result.Valid(), "validation changed for %v", sample)
	}
}

func TestNormalizeObjectToolSchemaSemantics(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		samples []string
	}{
		{
			name: "root and nested prohibitions are conjunctive",
			input: `{"type":"object","properties":{"environment":{"type":"string"}},"required":["environment"],"not":{"required":["blocked"]},"allOf":[
				{"allOf":[{"not":{"properties":{"environment":{"enum":["production"]}},"required":["environment"]}}]},
				{"not":{"properties":{"environment":{"enum":["retired"]}},"required":["environment"]}}
			]}`,
			samples: []string{`{"environment":"staging"}`, `{"environment":"production"}`, `{"environment":"retired"}`, `{"environment":"staging","blocked":false}`, `{}`},
		},
		{
			name: "negation preserves missing-field behavior",
			input: `{"type":"object","properties":{"environment":{"type":"string"}},"allOf":[
				{"not":{"properties":{"environment":{"enum":["production"]}}}}
			]}`,
			samples: []string{`{}`, `{"environment":"production"}`, `{"environment":"staging"}`},
		},
		{
			name: "object enums intersect",
			input: `{"type":"object","enum":[{"mode":"staging"},{"mode":"production"}],"allOf":[
				{"enum":[{"mode":"staging"},{"mode":"development"}]}
			]}`,
			samples: []string{`{"mode":"staging"}`, `{"mode":"production"}`, `{"mode":"development"}`, `{}`},
		},
		{
			name: "disjoint enums remain unsatisfiable",
			input: `{"type":"object","enum":[{"mode":"staging"}],"allOf":[
				{"enum":[{"mode":"production"}],"not":{"required":["blocked"]}}
			]}`,
			samples: []string{`{"mode":"staging"}`, `{"mode":"production"}`, `{"blocked":true}`, `{}`},
		},
		{
			name: "constraint-only property branches stay untyped",
			input: `{"type":"object","properties":{"count":{"type":"integer"}},"required":["count"],"allOf":[
				{"allOf":[{"properties":{"count":{"minimum":0}}},{"properties":{"count":{"maximum":5}}}]}
			]}`,
			samples: []string{`{"count":0}`, `{"count":3}`, `{"count":-1}`, `{"count":6}`, `{"count":"3"}`, `{}`},
		},
		{
			name:    "nested field composites stay untyped",
			input:   `{"type":"object","properties":{"count":{"type":"integer","allOf":[{"minimum":1}]}},"required":["count"]}`,
			samples: []string{`{"count":3}`, `{"count":0}`, `{"count":"3"}`, `{}`},
		},
		{
			name:    "empty conjunction branch does not add constraints",
			input:   `{"type":"object","allOf":[{}]}`,
			samples: []string{`{}`, `{"count":3}`, `{"count":null}`},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			schema, err := shuttle.FromJSON([]byte(test.input))
			require.NoError(t, err)
			before, err := json.Marshal(schema)
			require.NoError(t, err)
			adapted, err := NormalizeObjectToolSchema(schema)
			require.NoError(t, err)
			for _, sample := range test.samples {
				original, err := gojsonschema.Validate(gojsonschema.NewStringLoader(test.input), gojsonschema.NewStringLoader(sample))
				require.NoError(t, err)
				result, err := gojsonschema.Validate(gojsonschema.NewGoLoader(adapted), gojsonschema.NewStringLoader(sample))
				require.NoError(t, err)
				assert.Equal(t, original.Valid(), result.Valid(), "validation changed for %s", sample)
			}
			after, err := json.Marshal(schema)
			require.NoError(t, err)
			assert.Equal(t, string(before), string(after))
		})
	}
}

func TestNormalizeObjectToolSchemaJSONEquivalentEnums(t *testing.T) {
	schema := &shuttle.JSONSchema{
		Type: "object",
		Enum: []interface{}{map[string]int{"count": 1}},
		AllOf: []*shuttle.JSONSchema{{
			Enum: []interface{}{map[string]interface{}{"count": float64(1)}},
		}},
	}
	original := schema.ToToolMap()
	adapted, err := NormalizeObjectToolSchema(schema)
	require.NoError(t, err)
	for _, sample := range []string{`{"count":1}`, `{"count":2}`, `{}`} {
		before, err := gojsonschema.Validate(gojsonschema.NewGoLoader(original), gojsonschema.NewStringLoader(sample))
		require.NoError(t, err)
		after, err := gojsonschema.Validate(gojsonschema.NewGoLoader(adapted), gojsonschema.NewStringLoader(sample))
		require.NoError(t, err)
		assert.Equal(t, before.Valid(), after.Valid(), "JSON-equivalent enum values changed validation for %s", sample)
	}
}

func TestNormalizeObjectToolSchema(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		want      string
		wantError string
	}{
		{
			name:  "empty root",
			input: `{}`,
			want:  `{"type":"object","properties":{}}`,
		},
		{
			name: "allOf combines overlapping branch constraints",
			input: `{"allOf":[
				{"properties":{"idx":{"type":"integer","minimum":1}},"required":["idx"]},
				{"properties":{"idx":{"type":"integer","maximum":5}},"required":["idx"]}
			]}`,
			want: `{"type":"object","properties":{"idx":{"allOf":[
				{"type":"integer","minimum":1},{"type":"integer","maximum":5}
			]}},"required":["idx"]}`,
		},
		{
			name: "allOf preserves properties and unions required",
			input: `{"description":"Input","properties":{"idx":{"type":"integer"}},"required":["idx"],"allOf":[
				{"type":"object","properties":{"idx":{"type":"integer","minimum":1},"name":{"type":"string","description":"Name"}},"required":["idx","name"]}
			]}`,
			want: `{"type":"object","description":"Input","properties":{
				"idx":{"allOf":[{"type":"integer"},{"type":"integer","minimum":1}]},
				"name":{"type":"string","description":"Name"}
			},"required":["idx","name"]}`,
		},
		{
			name: "anyOf is rejected instead of losing correlations",
			input: `{"anyOf":[
				{"type":"object","properties":{"kind":{"type":"string","enum":["file"]},"path":{"type":"string","description":"Path"}},"required":["kind","path"]},
				{"type":"object","properties":{"kind":{"type":"string","enum":["text"]},"text":{"type":"string","description":"Text"}},"required":["kind","text"]}
			]}`,
			wantError: "cannot losslessly adapt tool schema root anyOf",
		},
		{
			name: "oneOf is rejected instead of losing exclusivity",
			input: `{"oneOf":[
				{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]},
				{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}
			]}`,
			wantError: "cannot losslessly adapt tool schema root oneOf",
		},
		{
			name: "nested root alternatives are rejected",
			input: `{"allOf":[{"anyOf":[
				{"type":"object","properties":{"tasks":{"type":"array","items":{"type":"object","properties":{"idx":{"anyOf":[{"type":"integer"},{"type":"null"}]}},"required":["idx"]}}},"required":["tasks"]},
				{"type":"object","properties":{}}
			]}]}`,
			wantError: "tool schema allOf branch 0: cannot losslessly adapt tool schema root anyOf",
		},
		{
			name:      "nil branch is rejected",
			input:     `{"allOf":[null]}`,
			wantError: "tool schema allOf branch 0 is nil",
		},
		{
			name:  "branch annotations and other supported keywords survive",
			input: `{"description":"Root","allOf":[{"description":"Branch","default":{},"format":"custom","pattern":"x","minimum":0,"maximum":5,"minLength":0,"maxLength":10}]}`,
			want:  `{"type":"object","properties":{},"description":"Root\nBranch","default":{},"format":"custom","pattern":"x","minimum":0,"maximum":5,"minLength":0,"maxLength":10}`,
		},
		{
			name:  "identical defaults do not conflict",
			input: `{"default":{},"allOf":[{"default":{}}]}`,
			want:  `{"type":"object","properties":{},"default":{}}`,
		},
		{
			name:      "conflicting defaults are explicit errors",
			input:     `{"default":{"mode":"staging"},"allOf":[{"default":{"mode":"production"}}]}`,
			wantError: "cannot losslessly merge conflicting root default",
		},
		{
			name:      "explicit scalar root is not silently replaced",
			input:     `{"type":"string"}`,
			wantError: "tool schema root must be an object",
		},
		{
			name:      "explicit non-object branch is rejected",
			input:     `{"allOf":[{"type":"array"}]}`,
			wantError: "tool schema allOf branch 0: tool schema root must be an object",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			schema, err := shuttle.FromJSON([]byte(test.input))
			require.NoError(t, err)
			before, err := json.Marshal(schema)
			require.NoError(t, err)
			result, err := NormalizeObjectToolSchema(schema)
			if test.wantError != "" {
				require.ErrorContains(t, err, test.wantError)
				assert.Nil(t, result)
			} else {
				require.NoError(t, err)
				data, err := json.Marshal(result)
				require.NoError(t, err)
				assert.JSONEq(t, test.want, string(data))
			}
			after, err := json.Marshal(schema)
			require.NoError(t, err)
			assert.Equal(t, string(before), string(after))
		})
	}
	result, err := NormalizeObjectToolSchema(nil)
	require.NoError(t, err)
	assert.Nil(t, result)
}
