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
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/teradata-labs/loom/embedded"
	"gopkg.in/yaml.v3"
)

// Taxonomy is the per-domain seed map the LLM classifier presents as
// candidate parent_index_path buckets.
//
// Built-in domains ship via embedded/taxonomy.yaml (DefaultTaxonomy).
// Users with their own skill libraries provide a YAML file via
// LoadTaxonomy and pass it to Classify / ClassifyAgainstGraph.
type Taxonomy struct {
	Domains map[string]TaxonomyDomain `yaml:"domains"`
}

// TaxonomyDomain holds the bucket suggestions and a short domain
// description used in the classifier prompt.
type TaxonomyDomain struct {
	Description string           `yaml:"description"`
	Buckets     []TaxonomyBucket `yaml:"buckets"`
}

// TaxonomyBucket is one suggested classification path.
//
// Path is what the classifier may emit as parent_index_path; Description
// is presented to the LLM alongside the path so it can match user
// skills to buckets by vocabulary, not just by name.
type TaxonomyBucket struct {
	Path        string `yaml:"path"`
	Description string `yaml:"description"`
}

// HasDomain reports whether the taxonomy carries an entry for d.
func (t Taxonomy) HasDomain(d string) bool {
	if t.Domains == nil {
		return false
	}
	_, ok := t.Domains[d]
	return ok
}

// BucketsFor returns the bucket suggestions for the given domain. The
// caller can use the returned slice in classifier prompts. Returns nil
// when the domain is unknown — callers should fall through to the
// generic <domain>/<topic> placeholder.
func (t Taxonomy) BucketsFor(domain string) []TaxonomyBucket {
	if t.Domains == nil {
		return nil
	}
	d, ok := t.Domains[domain]
	if !ok {
		return nil
	}
	return d.Buckets
}

// DefaultTaxonomy returns a parsed copy of the built-in seed taxonomy
// shipped via embedded/taxonomy.yaml. The result is cached after the
// first parse; mutate the returned struct at your peril.
//
// Callers wanting a private copy should call MustParseTaxonomy on
// embedded.GetSkillsTaxonomy() themselves.
func DefaultTaxonomy() Taxonomy {
	defaultTaxonomyOnce.Do(func() {
		t, err := ParseTaxonomy(embedded.GetSkillsTaxonomy())
		if err != nil {
			// Embedded YAML is committed to the repo; if it doesn't parse
			// the build is broken. Panic loudly rather than silently
			// degrading the classifier to no-suggestions mode.
			panic(fmt.Sprintf("embedded/taxonomy.yaml does not parse: %v", err))
		}
		defaultTaxonomy = t
	})
	return defaultTaxonomy
}

var (
	defaultTaxonomyOnce sync.Once
	defaultTaxonomy     Taxonomy
)

// LoadTaxonomy reads a user-supplied taxonomy YAML from disk. When path
// is empty, returns DefaultTaxonomy. The file format mirrors
// embedded/taxonomy.yaml exactly; see that file for the schema and the
// validation rules ParseTaxonomy enforces.
func LoadTaxonomy(path string) (Taxonomy, error) {
	if path == "" {
		return DefaultTaxonomy(), nil
	}
	data, err := os.ReadFile(path) // #nosec G304 -- user-supplied path is the documented contract for --taxonomy
	if err != nil {
		return Taxonomy{}, fmt.Errorf("read taxonomy %s: %w", path, err)
	}
	t, err := ParseTaxonomy(data)
	if err != nil {
		return Taxonomy{}, fmt.Errorf("parse taxonomy %s: %w", path, err)
	}
	return t, nil
}

// ParseTaxonomy unmarshals taxonomy YAML and validates that every bucket
// path starts with its declaring domain root and uses only lowercase
// kebab-case segments. Invalid taxonomies are rejected at load time
// rather than at classifier invocation, so misconfiguration surfaces
// before the first LLM call.
func ParseTaxonomy(data []byte) (Taxonomy, error) {
	var t Taxonomy
	if err := yaml.Unmarshal(data, &t); err != nil {
		return Taxonomy{}, fmt.Errorf("yaml unmarshal: %w", err)
	}
	if len(t.Domains) == 0 {
		return Taxonomy{}, fmt.Errorf("taxonomy has no domains")
	}
	for domainName, d := range t.Domains {
		if domainName == "" {
			return Taxonomy{}, fmt.Errorf("taxonomy has an empty domain key")
		}
		for i, b := range d.Buckets {
			if err := validateBucket(domainName, b); err != nil {
				return Taxonomy{}, fmt.Errorf("domain %q bucket %d: %w", domainName, i, err)
			}
		}
	}
	return t, nil
}

// MustParseTaxonomy is the panicking variant of ParseTaxonomy. Use only
// for compile-time-known YAML (tests, embedded defaults).
func MustParseTaxonomy(data []byte) Taxonomy {
	t, err := ParseTaxonomy(data)
	if err != nil {
		panic(err)
	}
	return t
}

// validateBucket enforces the shape rules the classifier relies on:
//   - Path is non-empty.
//   - Path starts with "<domain>/" or equals "<domain>" (anchors the
//     classifier to the declaring domain root).
//   - Every segment is lowercase kebab-case (no underscores, no
//     uppercase, no embedded spaces).
func validateBucket(domain string, b TaxonomyBucket) error {
	path := strings.Trim(b.Path, "/")
	if path == "" {
		return fmt.Errorf("path is empty")
	}
	if path != domain && !strings.HasPrefix(path, domain+"/") {
		return fmt.Errorf("path %q does not start with domain root %q/", path, domain)
	}
	for _, seg := range strings.Split(path, "/") {
		if seg == "" {
			return fmt.Errorf("empty segment in path %q", path)
		}
		if seg != strings.ToLower(seg) {
			return fmt.Errorf("non-lowercase segment %q in path %q", seg, path)
		}
		if strings.ContainsAny(seg, " _") {
			return fmt.Errorf("invalid characters in segment %q in path %q", seg, path)
		}
	}
	return nil
}
