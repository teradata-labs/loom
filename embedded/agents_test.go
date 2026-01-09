package embedded

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
