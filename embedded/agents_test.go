package embedded

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestGetWeaver(t *testing.T) {
	data := GetWeaver()

	require.NotNil(t, data, "Embedded weaver data should not be nil")
	require.NotEmpty(t, data, "Embedded weaver data should not be empty")

	// Verify it looks like YAML content
	content := string(data)
	assert.Contains(t, content, "name: weaver", "Should contain weaver agent name")
	assert.Contains(t, content, "version:", "Should contain version field")
	assert.Contains(t, content, "description:", "Should contain description field")
	assert.Contains(t, content, "max_tool_executions:", "Should contain max_tool_executions config")

	// Verify reasonable size (should be a few KB)
	assert.Greater(t, len(data), 1000, "Weaver config should be at least 1KB")
	assert.Less(t, len(data), 50000, "Weaver config should be less than 50KB")
}

func TestWeaverYAMLVariable(t *testing.T) {
	// Test direct access to the variable
	require.NotNil(t, WeaverYAML, "WeaverYAML variable should not be nil")
	require.NotEmpty(t, WeaverYAML, "WeaverYAML variable should not be empty")

	// Verify it matches GetWeaver() output
	assert.Equal(t, WeaverYAML, GetWeaver(), "GetWeaver() should return the same data as WeaverYAML")
}

// TestWeaverCreationSkill_ChatCommandGuidance is a regression test for #172:
// the weaver used to tell users to run a created agent with a positional
// `loom chat <name>`, which fails because chat requires the --thread flag.
// The always-on weaver-creation skill's output_format must document the exact,
// correct invocations so the model stops inventing the broken form.
func TestWeaverCreationSkill_ChatCommandGuidance(t *testing.T) {
	data := GetWeaverCreationSkill()
	require.NotEmpty(t, data, "weaver-creation skill should be embedded")

	var parsed struct {
		Prompt struct {
			OutputFormat string `yaml:"output_format"`
		} `yaml:"prompt"`
	}
	require.NoError(t, yaml.Unmarshal(data, &parsed), "weaver-creation.yaml must be valid YAML")

	of := parsed.Prompt.OutputFormat
	require.NotEmpty(t, of, "output_format must be present")

	assert.Contains(t, of, "loom chat --thread <thread-id>",
		"must document the correct one-shot CLI invocation")
	assert.Contains(t, of, "loom --thread <thread-id>",
		"must document the correct interactive TUI invocation")
	assert.Contains(t, of, "--thread flag is required",
		"must call out that --thread is required, not a positional agent argument")
}
