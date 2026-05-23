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
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

var skillsCmd = &cobra.Command{
	Use:   "skills",
	Short: "Skill catalog operations",
	Long:  `Manage skill bindings, migrate legacy agent configs, and inspect the skill index.`,
}

var skillsMigrateCmd = &cobra.Command{
	Use:   "migrate <agent.yaml>",
	Short: "Migrate a v1.2.0 agent config to skill bindings",
	Long: `Reads an agent YAML config that uses the legacy enabled_skills /
disabled_skills filter pair and prints an equivalent bindings: block to stdout.

The migration is non-destructive — your file is not modified. Pipe the output
into a new file, or copy the bindings: block into your existing config.

Examples:

  # Print the migrated config to stdout:
  loom skills migrate examples/agent.yaml

  # Save to a new file:
  loom skills migrate examples/agent.yaml > examples/agent-migrated.yaml`,
	Args: cobra.ExactArgs(1),
	RunE: runSkillsMigrate,
}

func init() {
	skillsCmd.AddCommand(skillsMigrateCmd)
	skillsCmd.AddCommand(skillsImportCmd)
	skillsCmd.AddCommand(skillsClassifyCmd)
	skillsCmd.AddCommand(skillsAddCmd)
}

// runSkillsMigrate reads the input YAML, synthesizes bindings from the legacy
// enabled_skills/disabled_skills shape, and writes a migrated config to stdout.
//
// The transformation rules mirror pkg/skills/binding.selectBindings:
//   - enabled_skills present  -> bindings synthesized as EAGER (preserves
//     v1.2.0 always-on behavior)
//   - enabled_skills empty    -> no bindings synthesized; agent at runtime
//     still picks up disabled_skills as a filter
//     via the in-process resolver shim
func runSkillsMigrate(_ *cobra.Command, args []string) error {
	path := filepath.Clean(args[0])
	raw, err := os.ReadFile(path) //nolint:gosec // G304: user-supplied config path is the documented contract of this CLI subcommand
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}

	// Parse generically so we can preserve unknown fields verbatim. The
	// migration only touches the spec.skills (or agent.skills) block.
	var doc map[string]interface{}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("parse YAML: %w", err)
	}

	skillsNode, parent, found := findSkillsBlock(doc)
	if !found {
		return fmt.Errorf("no skills block found at agent.skills or spec.skills")
	}

	migrated := migrateSkillsBlock(skillsNode)
	parent["skills"] = migrated

	out, err := yaml.Marshal(doc)
	if err != nil {
		return fmt.Errorf("marshal migrated YAML: %w", err)
	}
	if _, err := os.Stdout.Write(out); err != nil {
		return fmt.Errorf("write stdout: %w", err)
	}
	return nil
}

// findSkillsBlock walks the agent-config YAML for the skills sub-block.
// Supports both the legacy "agent: { skills: ... }" shape and the
// k8s-style "spec: { skills: ... }" shape used by newer configs.
//
// Returns the skills map, its parent map (so the caller can replace it),
// and whether one was found.
func findSkillsBlock(doc map[string]interface{}) (map[string]interface{}, map[string]interface{}, bool) {
	if a, ok := doc["agent"].(map[string]interface{}); ok {
		if s, ok := a["skills"].(map[string]interface{}); ok {
			return s, a, true
		}
	}
	if s, ok := doc["spec"].(map[string]interface{}); ok {
		if sk, ok := s["skills"].(map[string]interface{}); ok {
			return sk, s, true
		}
	}
	return nil, nil, false
}

// migrateSkillsBlock synthesizes a bindings: list from the legacy
// enabled_skills / disabled_skills fields and returns a new skills block
// with both representations present. Existing bindings: in the source are
// preserved; we never overwrite a hand-authored bindings list.
func migrateSkillsBlock(in map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(in)+2)
	for k, v := range in {
		out[k] = v
	}

	if _, exists := out["bindings"]; exists {
		// Caller already migrated; leave as-is.
		return out
	}

	enabled := stringSliceFromYAML(in["enabled_skills"])
	if len(enabled) == 0 {
		// Nothing to synthesize. The runtime resolver shim handles the
		// pure-disabled case at startup; we annotate so the user knows.
		out["_migration_note"] = "no enabled_skills set; runtime applies LAZY default minus disabled_skills"
		return out
	}

	bindings := make([]map[string]interface{}, 0, len(enabled))
	for _, name := range enabled {
		bindings = append(bindings, map[string]interface{}{
			"name": name,
			"mode": "EAGER",
		})
	}
	out["bindings"] = bindings

	out["_migration_note"] = "bindings synthesized from enabled_skills (mode=EAGER preserves v1.2.0 behavior); enabled_skills/disabled_skills retained as fallback"
	return out
}

// stringSliceFromYAML coerces a YAML list-of-strings node into []string.
// Returns nil for any other shape (including nil and missing keys).
func stringSliceFromYAML(v interface{}) []string {
	list, ok := v.([]interface{})
	if !ok {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
