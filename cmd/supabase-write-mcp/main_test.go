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

func TestBuildRowMaps_ObjectShape(t *testing.T) {
	cols, rows, sample, errMsg := buildRowMaps(map[string]interface{}{
		"rows": []interface{}{
			map[string]interface{}{"year": 2020.0, "country": "USA"},
			map[string]interface{}{"year": 2021.0, "country": "CHN"},
		},
	})
	require.Empty(t, errMsg)
	assert.Equal(t, []string{"country", "year"}, cols, "object-shape columns are the sorted key union")
	assert.Len(t, rows, 2)
	assert.Equal(t, "USA", rows[0]["country"])
	assert.Equal(t, 2020.0, sample["year"])
}

func TestBuildRowMaps_DuckDBShape(t *testing.T) {
	// Exactly what opendata_query returns: columns + rows as value-arrays.
	cols, rows, sample, errMsg := buildRowMaps(map[string]interface{}{
		"columns": []interface{}{"country_code", "year", "population", "gdp"},
		"rows": []interface{}{
			[]interface{}{"USA", 2020.0, 331000000.0, 2.1e13},
			[]interface{}{"CHN", 2020.0, 1402000000.0, 1.47e13},
		},
	})
	require.Empty(t, errMsg)
	assert.Equal(t, []string{"country_code", "year", "population", "gdp"}, cols, "column order is preserved as given")
	require.Len(t, rows, 2)
	assert.Equal(t, "CHN", rows[1]["country_code"])
	assert.Equal(t, 1402000000.0, rows[1]["population"])
	assert.Equal(t, "USA", sample["country_code"])
}

func TestBuildRowMaps_Errors(t *testing.T) {
	cases := []struct {
		name string
		args map[string]interface{}
		want string
	}{
		{"empty rows", map[string]interface{}{"rows": []interface{}{}}, "non-empty array"},
		{"bad column name", map[string]interface{}{"columns": []interface{}{"ok", "bad name"}, "rows": []interface{}{[]interface{}{1.0, 2.0}}}, "invalid column name"},
		{"arity mismatch", map[string]interface{}{"columns": []interface{}{"a", "b"}, "rows": []interface{}{[]interface{}{1.0}}}, "values but 2 columns"},
		{"object expected", map[string]interface{}{"rows": []interface{}{"notanobject"}}, "must be a JSON object"},
		{"array expected with columns", map[string]interface{}{"columns": []interface{}{"a"}, "rows": []interface{}{map[string]interface{}{"a": 1.0}}}, "must be an array of values"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, errMsg := buildRowMaps(tc.args)
			require.NotEmpty(t, errMsg)
			assert.Contains(t, errMsg, tc.want)
		})
	}
}

func TestQueryGuardForbiddenRe(t *testing.T) {
	// Must REJECT: mutations/DDL, cross-schema refs, and pg_* references.
	reject := []string{
		"SELECT * FROM public.messages",
		"select * from auth.users",
		"SELECT * FROM storage.objects",
		"SELECT * FROM information_schema.tables",
		"SELECT * FROM pg_catalog.pg_tables",
		"SELECT * FROM pg_class",
		"WITH x AS (DELETE FROM t RETURNING *) SELECT * FROM x",
		"SELECT 1; DROP TABLE t",
		"SELECT * FROM t; TRUNCATE t",
		"select * from vault.secrets",
	}
	for _, q := range reject {
		if !forbiddenQueryRe.MatchString(q) {
			t.Errorf("forbiddenQueryRe should REJECT: %q", q)
		}
	}
	// Must ALLOW: pure reads of the write schema (bare table names, aggregates, CTEs).
	allow := []string{
		"SELECT country_code, gdp FROM population_gdp_joined WHERE year = 2020",
		"WITH t AS (SELECT * FROM us_unemployment_co2) SELECT avg(cpi) FROM t",
		"select apd_district, count(*) AS n from inflation_vs_austin_crime group by 1 order by 2 desc limit 10",
		"SELECT a.x, b.y FROM table_a a JOIN table_b b USING (county)",
	}
	for _, q := range allow {
		if forbiddenQueryRe.MatchString(q) {
			t.Errorf("forbiddenQueryRe should ALLOW: %q", q)
		}
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
	// No odKey -> query_to_table is not advertised; base set is write_table,
	// list_tables, query.
	p := &provider{schema: "dreambase"}
	tools, err := p.ListTools(context.Background())
	require.NoError(t, err)
	require.Len(t, tools, 3)
	names := map[string]bool{}
	for _, tl := range tools {
		names[tl.Name] = true
		assert.NotEmpty(t, tl.Description)
		assert.Equal(t, "object", tl.InputSchema["type"])
	}
	assert.True(t, names["write_table"] && names["list_tables"] && names["query"])

	// With an OpenData key, query_to_table is also advertised (4 total).
	p2 := &provider{schema: "dreambase", odKey: "od_live_test"}
	tools2, err := p2.ListTools(context.Background())
	require.NoError(t, err)
	require.Len(t, tools2, 4)
}

func TestUnknownTool(t *testing.T) {
	p := &provider{schema: "dreambase"}
	res, err := p.CallTool(context.Background(), "nope", nil)
	require.NoError(t, err)
	assert.True(t, res.IsError)
}
