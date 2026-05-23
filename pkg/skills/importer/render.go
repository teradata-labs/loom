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
	"strings"

	"gopkg.in/yaml.v3"
)

// SkillRefsCap mirrors the loader's cap on the skill_refs YAML field.
// Resolved cross-skill references beyond this stay in the prompt body
// (for parent-index skills) but aren't in the top-level field.
const SkillRefsCap = 3

// ChooseDomain maps imported skills to the loader's allowed domain set.
// Anything starting with "teradata-" gets domain "teradata"; the parent
// index gets "meta-agent"; everything else falls back to "general".
func ChooseDomain(imp *Skill) string {
	if imp.IsParentIndex {
		return "meta-agent"
	}
	if strings.HasPrefix(imp.Name, "teradata-") {
		return "teradata"
	}
	return "general"
}

// DeriveTitle converts a kebab-case skill name to a Title Case string.
// "teradata-sql-fundamentals" -> "Teradata Sql Fundamentals".
func DeriveTitle(name string) string {
	parts := strings.Split(name, "-")
	for i, p := range parts {
		if p == "" {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, " ")
}

// BuildInstructions concatenates the SKILL.md body with each reference
// under a labeled section. Cross-skill markdown links are normalized to
// bare names so the LLM does not chase ../<name>/SKILL.md paths that
// don't exist in Loom's filesystem.
func BuildInstructions(imp *Skill) string {
	var b strings.Builder
	body := normalizeCrossSkillLinks(imp.Body)
	b.WriteString(body)
	if !strings.HasSuffix(body, "\n") {
		b.WriteString("\n")
	}
	if imp.IsParentIndex && len(imp.LinkedSkills) > 0 {
		b.WriteString("\n## Linked Skills (Loom Catalog)\n\n")
		b.WriteString("This index points at the following skills available in the Loom catalog. Use slash commands or natural language to activate them:\n\n")
		for _, n := range imp.LinkedSkills {
			fmt.Fprintf(&b, "- `%s`\n", n)
		}
	}
	for _, r := range imp.References {
		fmt.Fprintf(&b, "\n## Reference: %s\n\n", r.Title)
		b.WriteString(normalizeCrossSkillLinks(r.Body))
		if !strings.HasSuffix(r.Body, "\n") {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// normalizeCrossSkillLinks rewrites "[label](../foo/SKILL.md)" to
// "[label](skill:foo)" so the rendered prompt does not promise filesystem
// paths that are not part of the converted output.
func normalizeCrossSkillLinks(body string) string {
	return crossSkillLink.ReplaceAllStringFunc(body, func(match string) string {
		groups := crossSkillLink.FindStringSubmatch(match)
		if len(groups) < 2 {
			return match
		}
		labelEnd := strings.Index(match, "]")
		if labelEnd <= 0 {
			return match
		}
		label := match[1:labelEnd]
		return fmt.Sprintf("[%s](skill:%s)", label, groups[1])
	})
}

// RenderYAML produces a loom/v1 Skill YAML document for one Skill. The
// shape mirrors what skills.LoadSkill expects so callers can validate by
// round-tripping through the loader. Multi-line fields use literal block
// scalars (`|`) so the output is human-readable.
func RenderYAML(imp *Skill) ([]byte, error) {
	if imp == nil || imp.Name == "" {
		return nil, fmt.Errorf("Skill missing name")
	}

	domain := ChooseDomain(imp)
	mode := "AUTO"
	if imp.IsParentIndex {
		mode = "ALWAYS"
	}

	instructions := BuildInstructions(imp)
	keywords := BuildKeywords(imp)
	slashCmds := BuildSlashCommands(imp)

	// skill_refs is loader-capped at SkillRefsCap; only emit the most
	// relevant ones. Parent-index skills carry their full routing table
	// inline already, so we leave skill_refs empty for them and let the
	// prompt do the work. We use ResolvedRefs (filtered against the known
	// importable set) so dangling references like Linux package names
	// don't leak into the generated YAML.
	var skillRefs []string
	if !imp.IsParentIndex && len(imp.ResolvedRefs) > 0 {
		maxRefs := len(imp.ResolvedRefs)
		if maxRefs > SkillRefsCap {
			maxRefs = SkillRefsCap
		}
		skillRefs = imp.ResolvedRefs[:maxRefs]
	}

	version := imp.Version
	if version == "" {
		version = "1.0.0"
	}
	// SKILL.md often uses "1.0"; normalize to semver-ish.
	if !strings.Contains(version, ".") || strings.Count(version, ".") < 2 {
		version = version + ".0"
	}

	author := imp.Author
	if author == "" {
		author = "imported"
	}

	rootKVs := []kvPair{
		scalarKV("apiVersion", "loom/v1"),
		scalarKV("kind", "Skill"),
		mappingKV("metadata",
			scalarKV("name", imp.Name),
			scalarKV("title", DeriveTitle(imp.Name)),
			multilineKV("description", imp.Description),
			scalarKV("version", version),
			scalarKV("domain", domain),
			scalarKV("author", author),
			mappingKV("labels",
				scalarKV("source", "agent-skill-import"),
				scalarKV("upstream", imp.Name),
			),
		),
		mappingKV("trigger",
			seqKV("slash_commands", slashCmds),
			seqKV("keywords", keywords),
			seqKV("intent_categories", nil),
			scalarKV("mode", mode),
			scalarFloatKV("min_confidence", 0.6),
		),
		mappingKV("prompt",
			literalKV("instructions", instructions),
		),
		mappingKV("tools",
			seqKV("required_tools", nil),
			seqKV("preferred_order", nil),
			seqKV("excluded_tools", nil),
			seqKV("mcp_servers", nil),
		),
		seqKV("pattern_refs", nil),
		seqKV("skill_refs", skillRefs),
	}
	// parent_index_path is loader-aware — when set, the index builder
	// uses it directly to place the skill in the routing tree. When
	// empty, the builder falls back to "unclassified/<domain>" via
	// SkillPath(). Emit the field only when a path was assigned to keep
	// older YAMLs unchanged.
	if imp.ParentIndexPath != "" {
		rootKVs = append(rootKVs, scalarKV("parent_index_path", imp.ParentIndexPath))
	}
	root := mappingNode(rootKVs...)

	doc := &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}}
	var buf strings.Builder
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, fmt.Errorf("encode YAML: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("close encoder: %w", err)
	}
	return []byte(buf.String()), nil
}

// --- yaml.Node builders (package-private; renderer-internal) ---------------

type kvPair struct{ K, V *yaml.Node }

func keyNode(s string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Value: s}
}

func scalarKV(k, v string) kvPair {
	return kvPair{K: keyNode(k), V: &yaml.Node{Kind: yaml.ScalarNode, Value: v}}
}

func scalarFloatKV(k string, v float64) kvPair {
	return kvPair{K: keyNode(k), V: &yaml.Node{
		Kind:  yaml.ScalarNode,
		Tag:   "!!float",
		Value: fmt.Sprintf("%g", v),
	}}
}

func multilineKV(k, v string) kvPair {
	var style yaml.Style
	if strings.Contains(v, "\n") {
		style = yaml.LiteralStyle
	}
	return kvPair{K: keyNode(k), V: &yaml.Node{
		Kind:  yaml.ScalarNode,
		Style: style,
		Value: v,
	}}
}

func literalKV(k, v string) kvPair {
	return kvPair{K: keyNode(k), V: &yaml.Node{
		Kind:  yaml.ScalarNode,
		Style: yaml.LiteralStyle,
		Value: v,
	}}
}

func seqKV(k string, items []string) kvPair {
	seq := &yaml.Node{Kind: yaml.SequenceNode}
	if len(items) == 0 {
		seq.Style = yaml.FlowStyle
	}
	for _, it := range items {
		seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: it})
	}
	return kvPair{K: keyNode(k), V: seq}
}

func mappingNode(pairs ...kvPair) *yaml.Node {
	n := &yaml.Node{Kind: yaml.MappingNode}
	for _, p := range pairs {
		n.Content = append(n.Content, p.K, p.V)
	}
	return n
}

func mappingKV(k string, pairs ...kvPair) kvPair {
	return kvPair{K: keyNode(k), V: mappingNode(pairs...)}
}
