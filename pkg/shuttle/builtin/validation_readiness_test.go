// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package builtin

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMain clears every provider key before the package runs, so web_search
// starts degraded here.
func TestBuiltinReadiness_ReportsWebSearchState(t *testing.T) {
	state, summary, remediation, ok := builtinReadiness("web_search")
	require.True(t, ok)
	assert.Equal(t, "degraded", state)
	assert.Contains(t, summary, "No API key")
	assert.Contains(t, remediation, "TAVILY_API_KEY")
	assert.NotContains(t, summary+remediation, testSecret)

	t.Setenv("TAVILY_API_KEY", testSecret)
	state, summary, _, ok = builtinReadiness("web_search")
	require.True(t, ok)
	assert.Equal(t, "ready", state)
	assert.NotContains(t, summary, testSecret, "readiness never includes key values")

	_, _, _, ok = builtinReadiness("http_request")
	assert.False(t, ok, "tools that do not report readiness are skipped")
	_, _, _, ok = builtinReadiness("no_such_tool")
	assert.False(t, ok)
}

func TestWebSearchTool_DoesNotAdvertiseAPIKeyArgument(t *testing.T) {
	tool := NewWebSearchTool()
	_, advertised := tool.InputSchema().Properties["api_key"]
	assert.False(t, advertised, "a key passed as an argument ends up in spans and history")

	result, err := tool.Execute(context.Background(), map[string]interface{}{"query": "golang", "provider": "tavily"})
	require.NoError(t, err)
	require.NotNil(t, result.Error)
	assert.Equal(t, "MISSING_API_KEY", result.Error.Code)
	assert.NotContains(t, result.Error.Suggestion, "api_key parameter")
	assert.Contains(t, result.Error.Suggestion, "cannot be passed as an argument")
}
