// Package embedded provides access to files embedded into the loom binary.
// This ensures critical configuration files are always available, even when
// the binary is distributed separately from the source tree.
package embedded

import (
	_ "embed"

	"github.com/teradata-labs/loom/pkg/agent"
)

// WeaverYAML contains the default weaver agent configuration.
// Weaver is the meta-agent that orchestrates other agents and manages complex workflows.
//
//go:embed weaver.yaml
var WeaverYAML []byte

// GetWeaver returns the embedded weaver.yaml content.
func GetWeaver() []byte {
	return WeaverYAML
}

// GuideYAML contains the default guide agent configuration.
// Guide is the helper agent that discovers and recommends agents based on user needs.
//
//go:embed guide.yaml
var GuideYAML []byte

// GetGuide returns the embedded guide.yaml content.
func GetGuide() []byte {
	return GuideYAML
}

// WeaverCreationSkillYAML contains the weaver-creation skill configuration.
//
//go:embed skills/weaver-creation.yaml
var WeaverCreationSkillYAML []byte

// GetWeaverCreationSkill returns the embedded weaver-creation.yaml skill content.
func GetWeaverCreationSkill() []byte {
	return WeaverCreationSkillYAML
}

// WeaverPresetsSkillYAML wires the weaver-presets skill (slash: /preset).
//
//go:embed skills/weaver-presets.yaml
var WeaverPresetsSkillYAML []byte

// GetWeaverPresetsSkill returns the embedded weaver-presets.yaml content.
func GetWeaverPresetsSkill() []byte {
	return WeaverPresetsSkillYAML
}

// WeaverTemplatesSkillYAML wires the weaver-templates skill (slash: /template).
//
//go:embed skills/weaver-templates.yaml
var WeaverTemplatesSkillYAML []byte

// GetWeaverTemplatesSkill returns the embedded weaver-templates.yaml content.
func GetWeaverTemplatesSkill() []byte {
	return WeaverTemplatesSkillYAML
}

// WeaverFromScratchSkillYAML wires the weaver-from-scratch skill
// (slash: /from-scratch). Fallback when no preset/template fits.
//
//go:embed skills/weaver-from-scratch.yaml
var WeaverFromScratchSkillYAML []byte

// GetWeaverFromScratchSkill returns the embedded weaver-from-scratch.yaml content.
func GetWeaverFromScratchSkill() []byte {
	return WeaverFromScratchSkillYAML
}

// SkillsTaxonomyYAML is the default seed taxonomy the skills importer's
// classifier consults when --classify is set without an explicit
// --taxonomy file. Edit embedded/taxonomy.yaml to change defaults; users
// extending Loom for their own domains should copy the file rather than
// editing this one.
//
//go:embed taxonomy.yaml
var SkillsTaxonomyYAML []byte

// GetSkillsTaxonomy returns the embedded taxonomy.yaml content.
func GetSkillsTaxonomy() []byte {
	return SkillsTaxonomyYAML
}

// GetStartHere returns the base ROM (START_HERE.md) content.
// This delegates to pkg/agent/rom_loader.go which is the single source of truth for ROM files.
// The ROM is embedded from pkg/agent/roms/START_HERE.md at compile time.
func GetStartHere() []byte {
	return agent.GetBaseROM()
}
