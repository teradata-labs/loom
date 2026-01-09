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
package runtime

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGetPythonTraceLibrary tests that the Python trace library is embedded correctly.
func TestGetPythonTraceLibrary(t *testing.T) {
	lib := GetPythonTraceLibrary()

	// Verify library is not empty
	require.NotEmpty(t, lib, "Python trace library should not be empty")

	// Verify key components are present
	assert.Contains(t, lib, "class LoomTracer", "Should contain LoomTracer class")
	assert.Contains(t, lib, "class Span", "Should contain Span class")
	assert.Contains(t, lib, "class trace_span", "Should contain trace_span context manager")
	assert.Contains(t, lib, "LOOM_TRACE_ID", "Should reference LOOM_TRACE_ID env var")
	assert.Contains(t, lib, "LOOM_SPAN_ID", "Should reference LOOM_SPAN_ID env var")
	assert.Contains(t, lib, "LOOM_TRACE_BAGGAGE", "Should reference LOOM_TRACE_BAGGAGE env var")
	assert.Contains(t, lib, "__LOOM_TRACE__:", "Should contain trace output prefix")

	// Verify LOOM_HAWK_ENDPOINT was removed
	assert.NotContains(t, lib, "LOOM_HAWK_ENDPOINT", "Should not reference LOOM_HAWK_ENDPOINT (removed)")

	// Verify it's valid Python (basic check)
	assert.True(t, strings.HasPrefix(lib, "\"\"\"") || strings.HasPrefix(lib, "#"), "Should start with docstring or comment")
	assert.Contains(t, lib, "import os", "Should import os module")
	assert.Contains(t, lib, "import json", "Should import json module")
}

// TestGetNodeTraceLibrary tests that the Node.js trace library is embedded correctly.
func TestGetNodeTraceLibrary(t *testing.T) {
	lib := GetNodeTraceLibrary()

	// Verify library is not empty
	require.NotEmpty(t, lib, "Node.js trace library should not be empty")

	// Verify key components are present
	assert.Contains(t, lib, "class LoomTracer", "Should contain LoomTracer class")
	assert.Contains(t, lib, "class Span", "Should contain Span class")
	assert.Contains(t, lib, "async function traceSpan", "Should contain traceSpan async helper")
	assert.Contains(t, lib, "function traceSpanSync", "Should contain traceSpanSync helper")
	assert.Contains(t, lib, "LOOM_TRACE_ID", "Should reference LOOM_TRACE_ID env var")
	assert.Contains(t, lib, "LOOM_SPAN_ID", "Should reference LOOM_SPAN_ID env var")
	assert.Contains(t, lib, "LOOM_TRACE_BAGGAGE", "Should reference LOOM_TRACE_BAGGAGE env var")
	assert.Contains(t, lib, "__LOOM_TRACE__:", "Should contain trace output prefix")

	// Verify LOOM_HAWK_ENDPOINT was removed
	assert.NotContains(t, lib, "LOOM_HAWK_ENDPOINT", "Should not reference LOOM_HAWK_ENDPOINT (removed)")

	// Verify it's valid JavaScript (basic check)
	assert.True(t, strings.HasPrefix(lib, "/**") || strings.HasPrefix(lib, "//"), "Should start with comment")
	assert.Contains(t, lib, "const", "Should use const keyword")
	assert.Contains(t, lib, "module.exports", "Should export module")
}

// TestTraceLibrarySize tests that embedded libraries are reasonable size.
func TestTraceLibrarySize(t *testing.T) {
	pythonLib := GetPythonTraceLibrary()
	nodeLib := GetNodeTraceLibrary()

	// Verify libraries are substantial (not accidentally truncated)
	assert.Greater(t, len(pythonLib), 5000, "Python library should be >5KB")
	assert.Greater(t, len(nodeLib), 5000, "Node.js library should be >5KB")

	// Verify libraries are reasonable size (not accidentally duplicated)
	assert.Less(t, len(pythonLib), 50000, "Python library should be <50KB")
	assert.Less(t, len(nodeLib), 50000, "Node.js library should be <50KB")
}

// TestPythonTraceLibraryLineCount tests approximate line count.
func TestPythonTraceLibraryLineCount(t *testing.T) {
	lib := GetPythonTraceLibrary()
	lines := strings.Count(lib, "\n")

	// Python library should be ~275-285 lines (after HAWK_ENDPOINT removal)
	assert.Greater(t, lines, 250, "Python library should have >250 lines")
	assert.Less(t, lines, 300, "Python library should have <300 lines")
}

// TestNodeTraceLibraryLineCount tests approximate line count.
func TestNodeTraceLibraryLineCount(t *testing.T) {
	lib := GetNodeTraceLibrary()
	lines := strings.Count(lib, "\n")

	// Node library should be ~290-295 lines (after HAWK_ENDPOINT removal)
	assert.Greater(t, lines, 270, "Node.js library should have >270 lines")
	assert.Less(t, lines, 310, "Node.js library should have <310 lines")
}
