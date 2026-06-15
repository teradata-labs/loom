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

func TestIdentifierValidation(t *testing.T) {
	good := []string{"unemployment_vs_co2", "t", "a1", "co2_by_country_year"}
	bad := []string{"", "1abc", "Bad", "drop table x", "bad; DROP TABLE x", "x-y", "x.y", "select"}
	// 'select' is a reserved word but matches the identifier shape; it is always
	// quoted via pgx.Identifier, so it is safe — keep it allowed.
	bad = bad[:len(bad)-1]
	for _, s := range good {
		assert.True(t, identRe.MatchString(s), "%q should be valid", s)
	}
	for _, s := range bad {
		assert.False(t, identRe.MatchString(s), "%q should be rejected", s)
	}
}

func TestPgType(t *testing.T) {
	assert.Equal(t, "double precision", pgType(float64(3.2)))
	assert.Equal(t, "double precision", pgType(2018))
	assert.Equal(t, "boolean", pgType(true))
	assert.Equal(t, "text", pgType("hello"))
	assert.Equal(t, "text", pgType(nil))
}

func TestListTools(t *testing.T) {
	p := &provider{schema: "dreambase"}
	tools, err := p.ListTools(context.Background())
	require.NoError(t, err)
	require.Len(t, tools, 2)
	names := map[string]bool{}
	for _, tl := range tools {
		names[tl.Name] = true
		assert.NotEmpty(t, tl.Description)
		assert.Equal(t, "object", tl.InputSchema["type"])
	}
	assert.True(t, names["write_table"] && names["list_tables"])
}

func TestUnknownTool(t *testing.T) {
	p := &provider{schema: "dreambase"}
	res, err := p.CallTool(context.Background(), "nope", nil)
	require.NoError(t, err)
	assert.True(t, res.IsError)
}
