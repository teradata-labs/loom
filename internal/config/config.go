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
// Package config provides configuration types compatible with Crush's interface.
package config

import (
	"sync"
)

var (
	globalConfig     *Config
	globalConfigOnce sync.Once
)

// SelectedModelType represents the type of selected model.
type SelectedModelType string

const (
	SelectedModelTypeLarge SelectedModelType = "large"
	SelectedModelTypeSmall SelectedModelType = "small"
)

// Config represents application configuration.
type Config struct {
	mu         sync.RWMutex
	workingDir string

	// Options
	Options Options
}

// Options represents TUI options.
type Options struct {
	TUI TUIOptions
}

// TUIOptions represents TUI-specific options.
type TUIOptions struct {
	DiffMode    string
	CompactMode bool
}

// SetCompactMode sets the compact mode option.
func (c *Config) SetCompactMode(compact bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Options.TUI.CompactMode = compact
}

// Get returns the global configuration.
func Get() *Config {
	globalConfigOnce.Do(func() {
		globalConfig = &Config{
			workingDir: ".",
		}
	})
	return globalConfig
}

// Set sets the global configuration.
func Set(cfg *Config) {
	globalConfig = cfg
}

// WorkingDir returns the working directory.
func (c *Config) WorkingDir() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.workingDir
}

// SetWorkingDir sets the working directory.
func (c *Config) SetWorkingDir(dir string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.workingDir = dir
}

// UpdatePreferredModel updates the preferred model.
func (c *Config) UpdatePreferredModel(modelType SelectedModelType, model Model) error {
	// In Loom, model selection is handled via gRPC
	return nil
}

// IsConfigured returns true if the configuration is complete.
func (c *Config) IsConfigured() bool {
	return true
}

// HasInitialDataConfig returns true if initial data is configured.
func HasInitialDataConfig() bool {
	return true
}

// Model represents an LLM model configuration.
type Model struct {
	Provider               string
	Model                  string
	Name                   string
	ContextWindow          int
	DefaultReasoningEffort string
	ReasoningLevels        []string
	SupportsReasoning      bool
	SupportsImages         bool
}

// CanReason returns whether the model supports reasoning.
func (m *Model) CanReason() bool {
	return m.SupportsReasoning
}

// SelectedModel represents the currently selected model.
type SelectedModel struct {
	Provider string
	Model    string
	Name     string
}

// Providers returns available model providers.
var Providers = []string{"anthropic", "bedrock", "ollama"}

// Models returns the model list.
func (c *Config) Models() []Model {
	return []Model{
		{Provider: "anthropic", Model: "claude-sonnet-4-20250514", Name: "Claude Sonnet 4", ContextWindow: 200000},
		{Provider: "anthropic", Model: "claude-opus-4-20250514", Name: "Claude Opus 4", ContextWindow: 200000},
	}
}

// GlobalConfigData holds global config information.
type GlobalConfigData struct {
	ConfigDir string
}

// AgentType represents the type of agent.
type AgentType string

const (
	AgentCoder    AgentType = "coder"
	AgentResearch AgentType = "research"
)

// Agent represents an agent configuration.
type Agent struct {
	Type AgentType
	Name string
}

// Agents returns configured agents.
func (c *Config) Agents() map[AgentType]Agent {
	return map[AgentType]Agent{
		AgentCoder: {Type: AgentCoder, Name: "Coder"},
	}
}

// GetModelByType returns the model for a given type.
func (c *Config) GetModelByType(modelType SelectedModelType) *Model {
	return &Model{
		Provider:               "anthropic",
		Model:                  "claude-sonnet-4-20250514",
		Name:                   "Claude Sonnet 4",
		ContextWindow:          200000,
		DefaultReasoningEffort: "medium",
		ReasoningLevels:        []string{"low", "medium", "high"},
		SupportsReasoning:      true,
	}
}

// GetModel returns the current model configuration.
func (c *Config) GetModel() *Model {
	return c.GetModelByType(SelectedModelTypeLarge)
}

// LSPConfig represents LSP configuration.
type LSPConfig struct {
	Enabled bool
}

// LSP returns the LSP configuration.
func (c *Config) LSP() *LSPConfig {
	return &LSPConfig{Enabled: false}
}

// MCPConfig represents MCP configuration.
type MCPConfig struct {
	Enabled bool
}

// MCP returns the MCP configuration.
func (c *Config) MCP() *MCPConfig {
	return &MCPConfig{Enabled: false}
}

// ProjectNeedsInitialization returns true if the project needs initialization.
// Returns (needsInit, error).
func ProjectNeedsInitialization() (bool, error) {
	// In Loom, project initialization is handled by the server
	return false, nil
}

// GetProviderForModel returns the provider for a model.
func (c *Config) GetProviderForModel(modelID string) string {
	return "anthropic"
}
