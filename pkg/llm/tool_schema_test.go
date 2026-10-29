package llm

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/xeipuuv/gojsonschema"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestInferSchemaType(t *testing.T) {
	for _, test := range []struct {
		name   string
		schema *shuttle.JSONSchema
		want   string
	}{
		{name: "absent schema"},
		{name: "empty", schema: &shuttle.JSONSchema{}, want: "string"},
		{name: "object", schema: &shuttle.JSONSchema{Properties: map[string]*shuttle.JSONSchema{}}, want: "object"},
		{name: "array", schema: &shuttle.JSONSchema{Items: &shuttle.JSONSchema{}}, want: "array"},
		{name: "object precedence", schema: &shuttle.JSONSchema{Properties: map[string]*shuttle.JSONSchema{}, Items: &shuttle.JSONSchema{}}, want: "object"},
		{name: "declared type", schema: &shuttle.JSONSchema{Type: "integer", Properties: map[string]*shuttle.JSONSchema{}}, want: "integer"},
		{name: "declared casing", schema: &shuttle.JSONSchema{Type: "OBJECT"}, want: "OBJECT"},
	} {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, InferSchemaType(test.schema))
			if test.schema != nil && test.name != "declared type" && test.name != "declared casing" {
				assert.Empty(t, test.schema.Type)
			}
		})
	}
}

func TestPrepareToolSchema(t *testing.T) {
	for _, test := range []struct {
		name       string
		input      string
		strict     bool
		disallowed []string
		keep       bool
		wantError  bool
	}{
		{name: "compatible conjunction", input: `{"allOf":[{"properties":{"count":{"type":"integer"}}}]}`, keep: true},
		{name: "skip alternative", input: `{"anyOf":[{"type":"object"}]}`},
		{name: "strict alternative", input: `{"oneOf":[{"type":"object"}]}`, strict: true, wantError: true},
		{name: "anthropic prohibition", input: `{"not":{"required":["blocked"]}}`, keep: true},
		{name: "openai prohibition", input: `{"not":{"required":["blocked"]}}`, disallowed: []string{"enum", "not"}},
		{name: "openai enum", input: `{"enum":[{}]}`, disallowed: []string{"enum", "not"}},
		{name: "strict openai enum", input: `{"enum":[{}]}`, strict: true, disallowed: []string{"enum", "not"}, wantError: true},
		{name: "null property", input: `{"properties":{"a":null}}`},
		{name: "strict null property", input: `{"properties":{"a":null}}`, strict: true, wantError: true},
		{name: "null property in item", input: `{"properties":{"array":{"items":{"properties":{"a":null}}}}}`},
		{name: "null alternative", input: `{"properties":{"choice":{"anyOf":[null]}}}`},
		{name: "null property in negation", input: `{"not":{"properties":{"a":null}}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			core, logs := observer.New(zap.WarnLevel)
			previous := zap.L()
			zap.ReplaceGlobals(zap.New(core))
			t.Cleanup(func() { zap.ReplaceGlobals(previous) })
			span := &observability.Span{}
			ctx := observability.ContextWithSpan(context.Background(), span)
			schema, err := shuttle.FromJSON([]byte(test.input))
			require.NoError(t, err)
			policy := &ToolSchemaPolicy{}
			result, keep, err := policy.Prepare(ctx, "problem_tool", schema, test.strict, test.disallowed...)
			assert.Equal(t, test.keep, keep)
			if test.wantError {
				require.ErrorContains(t, err, `tool "problem_tool" schema:`)
			} else {
				require.NoError(t, err)
			}
			if test.keep {
				assert.NotNil(t, result)
			} else {
				assert.Nil(t, result)
			}
			if !test.keep && !test.strict {
				require.Equal(t, 1, logs.Len())
				assert.Equal(t, "problem_tool", logs.All()[0].ContextMap()["tool"])
				assert.Contains(t, logs.All()[0].ContextMap()["error"], "schema:")
				require.Len(t, span.Events, 1)
				assert.Equal(t, "llm.tool.schema.skipped", span.Events[0].Name)
				assert.Equal(t, "problem_tool", span.Events[0].Attributes["tool"])
				assert.Contains(t, span.Events[0].Attributes["reason"], "schema:")
			} else {
				assert.Empty(t, logs.All())
				assert.Empty(t, span.Events)
			}
		})
	}
	policy := &ToolSchemaPolicy{}
	result, keep, err := policy.Prepare(context.Background(), "no_schema", nil, false)
	require.NoError(t, err)
	assert.True(t, keep)
	assert.Nil(t, result)
}

func TestToolSchemaPolicyWarnsOnce(t *testing.T) {
	core, logs := observer.New(zap.WarnLevel)
	previous := zap.L()
	zap.ReplaceGlobals(zap.New(core))
	t.Cleanup(func() { zap.ReplaceGlobals(previous) })
	policy := &ToolSchemaPolicy{}
	schema := &shuttle.JSONSchema{AnyOf: []*shuttle.JSONSchema{{Type: "object"}}}
	span := &observability.Span{}
	ctx := observability.ContextWithSpan(context.Background(), span)
	for attempt := 0; attempt < 3; attempt++ {
		_, keep, err := policy.Prepare(ctx, "incompatible", schema, false)
		require.NoError(t, err)
		assert.False(t, keep)
	}
	assert.Equal(t, 1, logs.Len())
	assert.Len(t, span.Events, 3)
	changedSchema := &shuttle.JSONSchema{Properties: map[string]*shuttle.JSONSchema{"a": nil}}
	for attempt := 0; attempt < 2; attempt++ {
		_, keep, err := policy.Prepare(ctx, "incompatible", changedSchema, false)
		require.NoError(t, err)
		assert.False(t, keep)
	}
	assert.Equal(t, 2, logs.Len())
	assert.Len(t, span.Events, 5)
	assert.Contains(t, logs.All()[1].ContextMap()["error"], "is null")
	var requests sync.WaitGroup
	for attempt := 0; attempt < 20; attempt++ {
		requests.Go(func() {
			_, keep, err := policy.Prepare(context.Background(), "concurrent", schema, false)
			assert.NoError(t, err)
			assert.False(t, keep)
		})
	}
	requests.Wait()
	assert.Equal(t, 3, logs.Len())
	otherClient := &ToolSchemaPolicy{}
	_, keep, err := otherClient.Prepare(context.Background(), "incompatible", schema, false)
	require.NoError(t, err)
	assert.False(t, keep)
	assert.Equal(t, 4, logs.Len())
}

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
			name:    "untyped root properties do not become strings",
			input:   `{"type":"object","properties":{"count":{"minimum":1}}}`,
			samples: []string{`{"count":0}`, `{"count":3}`, `{"count":"3"}`, `{"count":true}`, `{"count":null}`, `{}`},
		},
		{
			name:    "untyped branch properties follow the root rule",
			input:   `{"type":"object","allOf":[{"properties":{"count":{"minimum":1}}}]}`,
			samples: []string{`{"count":0}`, `{"count":3}`, `{"count":"3"}`, `{"count":true}`, `{"count":null}`, `{}`},
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
			wantError: "tool schema $.allOf[0] is null",
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
			name:  "root default wins conflicting annotations",
			input: `{"default":{"mode":"staging"},"allOf":[{"default":{"mode":"production"}}]}`,
			want:  `{"type":"object","properties":{},"default":{"mode":"staging"}}`,
		},
		{
			name:  "first branch default wins without a root annotation",
			input: `{"allOf":[{"default":{"mode":"staging"}},{"default":{"mode":"production"}}]}`,
			want:  `{"type":"object","properties":{},"default":{"mode":"staging"}}`,
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
