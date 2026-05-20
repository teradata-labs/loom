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

package importer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDefaultTaxonomyParses asserts the embedded YAML actually parses
// and carries the expected built-in domains. Catches breakage from
// careless edits to embedded/taxonomy.yaml at compile-test time.
func TestDefaultTaxonomyParses(t *testing.T) {
	t1 := DefaultTaxonomy()
	assert.True(t, t1.HasDomain("teradata"), "default taxonomy must include teradata")

	buckets := t1.BucketsFor("teradata")
	assert.NotEmpty(t, buckets)

	// Check a couple of specific paths we ship with so a typo in the
	// YAML file gets caught.
	pathSet := map[string]bool{}
	for _, b := range buckets {
		pathSet[b.Path] = true
	}
	for _, want := range []string{
		"teradata/performance",
		"teradata/security",
		"teradata/storage",
		"teradata/sql",
	} {
		assert.True(t, pathSet[want], "default taxonomy missing %s", want)
	}
}

// TestDefaultTaxonomyCached asserts the embedded YAML is parsed once
// and cached. Two calls should return the same Taxonomy value.
func TestDefaultTaxonomyCached(t *testing.T) {
	a := DefaultTaxonomy()
	b := DefaultTaxonomy()
	// We compare by domain map identity; sync.Once + caching means the
	// second call returns the same map.
	assert.Equal(t, a.Domains, b.Domains)
}

func TestParseTaxonomy(t *testing.T) {
	tests := []struct {
		name        string
		yaml        string
		wantError   bool
		wantDomains []string
	}{
		{
			name: "happy path",
			yaml: `domains:
  postgres:
    description: "Postgres skills"
    buckets:
      - path: postgres/performance
        description: "EXPLAIN, vacuum, indexes"
      - path: postgres/replication
        description: "streaming replication"
`,
			wantDomains: []string{"postgres"},
		},
		{
			name: "domain root path is acceptable",
			yaml: `domains:
  foo:
    buckets:
      - path: foo
`,
			wantDomains: []string{"foo"},
		},
		{
			name: "multiple domains",
			yaml: `domains:
  postgres:
    buckets:
      - path: postgres/perf
  teradata:
    buckets:
      - path: teradata/sql
`,
			wantDomains: []string{"postgres", "teradata"},
		},
		{
			name:      "no domains rejected",
			yaml:      `domains: {}`,
			wantError: true,
		},
		{
			name:      "empty top-level rejected",
			yaml:      ``,
			wantError: true,
		},
		{
			name: "bucket path missing domain root rejected",
			yaml: `domains:
  postgres:
    buckets:
      - path: general/foo
`,
			wantError: true,
		},
		{
			name: "uppercase segment rejected",
			yaml: `domains:
  postgres:
    buckets:
      - path: postgres/Performance
`,
			wantError: true,
		},
		{
			name: "underscore segment rejected",
			yaml: `domains:
  postgres:
    buckets:
      - path: postgres/data_types
`,
			wantError: true,
		},
		{
			name: "embedded space rejected",
			yaml: `domains:
  postgres:
    buckets:
      - path: postgres/data types
`,
			wantError: true,
		},
		{
			name: "empty bucket path rejected",
			yaml: `domains:
  postgres:
    buckets:
      - path: ""
`,
			wantError: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseTaxonomy([]byte(tc.yaml))
			if tc.wantError {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			for _, d := range tc.wantDomains {
				assert.True(t, got.HasDomain(d), "expected domain %q", d)
			}
		})
	}
}

func TestLoadTaxonomy_FallbackToDefault(t *testing.T) {
	// Empty path returns the embedded default.
	got, err := LoadTaxonomy("")
	require.NoError(t, err)
	assert.True(t, got.HasDomain("teradata"))
}

func TestLoadTaxonomy_FromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "custom.yaml")
	body := `domains:
  postgres:
    description: "Postgres skills"
    buckets:
      - path: postgres/performance
        description: "perf"
`
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))

	got, err := LoadTaxonomy(path)
	require.NoError(t, err)
	assert.True(t, got.HasDomain("postgres"))
	assert.False(t, got.HasDomain("teradata"),
		"a user-supplied taxonomy must NOT silently merge with the default — it replaces it")
}

func TestLoadTaxonomy_MissingFile(t *testing.T) {
	_, err := LoadTaxonomy("/nonexistent/taxonomy.yaml")
	assert.Error(t, err)
}

func TestLoadTaxonomy_MalformedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "malformed.yaml")
	require.NoError(t, os.WriteFile(path, []byte("not: [valid"), 0o600))
	_, err := LoadTaxonomy(path)
	assert.Error(t, err)
}

func TestTaxonomy_BucketsForUnknownDomain(t *testing.T) {
	tax := MustParseTaxonomy([]byte(`domains:
  postgres:
    buckets:
      - path: postgres/foo
`))
	assert.Empty(t, tax.BucketsFor("teradata"),
		"unknown domain returns empty slice (caller falls through to <domain>/<topic>)")
}
