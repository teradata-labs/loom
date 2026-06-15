// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListTools(t *testing.T) {
	p := &provider{}
	tools, err := p.ListTools(context.Background())
	require.NoError(t, err)
	require.Len(t, tools, 7, "expected 7 OpenData tools")

	want := map[string]bool{
		"search": true, "list_providers": true, "get_dataset": true,
		"list_columns": true, "query": true, "query_dataset": true, "related_datasets": true,
	}
	for _, tl := range tools {
		assert.NotEmpty(t, tl.Name)
		assert.NotEmpty(t, tl.Description, "%s needs a description", tl.Name)
		assert.Equal(t, "object", tl.InputSchema["type"], "%s schema must be an object", tl.Name)
		delete(want, tl.Name)
	}
	assert.Empty(t, want, "missing expected tools: %v", want)
}

func TestArgHelpers(t *testing.T) {
	args := map[string]interface{}{
		"s":      "hello",
		"f":      float64(42), // JSON numbers decode to float64
		"istr":   "7",
		"nope":   nil,
		"notnum": "abc",
	}
	assert.Equal(t, "hello", argStr(args, "s"))
	assert.Equal(t, "", argStr(args, "missing"))
	assert.Equal(t, 42, argInt(args, "f", 1))
	assert.Equal(t, 7, argInt(args, "istr", 1))
	assert.Equal(t, 99, argInt(args, "missing", 99), "missing key uses default")
	assert.Equal(t, 5, argInt(args, "notnum", 5), "non-numeric string uses default")
}

func TestUnknownToolReturnsError(t *testing.T) {
	p := &provider{}
	res, err := p.CallTool(context.Background(), "does_not_exist", nil)
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.True(t, res.IsError)
}
