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

// GetStartHere returns the base ROM (START_HERE.md) content.
// This delegates to pkg/agent/rom_loader.go which is the single source of truth for ROM files.
// The ROM is embedded from pkg/agent/roms/START_HERE.md at compile time.
func GetStartHere() []byte {
	return agent.GetBaseROM()
}
