// Package embedded provides access to files embedded into the loom binary.
// This ensures critical configuration files are always available, even when
// the binary is distributed separately from the source tree.
package embedded

import (
	_ "embed"
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

// StartHereMD contains the agent instructions and operational guidelines.
// This file provides critical information for all agents running in the Loom framework.
//
//go:embed START_HERE.md
var StartHereMD []byte

// GetStartHere returns the embedded START_HERE.md content.
func GetStartHere() []byte {
	return StartHereMD
}
