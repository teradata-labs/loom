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

package plugins

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

var kebabCase = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// validResolutionModes is the set of accepted RefActivationFailureMode strings.
var validResolutionModes = map[string]bool{
	"":             true, // unspecified → runtime default
	"FAIL":         true,
	"SKIP_WARN":    true,
	"SKIP_SILENT":  true,
}

// validBindingModes is the set of accepted PluginBindingMode strings.
var validBindingModes = map[string]bool{
	"":       true, // unspecified → LAZY
	"EAGER":  true,
	"LAZY":   true,
	"ALWAYS": true,
}

// ─── YAML structs ─────────────────────────────────────────────────────────────

// pluginYAML is the top-level YAML document for a plugin file.
type pluginYAML struct {
	APIVersion string          `yaml:"apiVersion"`
	Kind       string          `yaml:"kind"`
	Metadata   pluginMetaYAML  `yaml:"metadata"`
	Trigger    pluginTriggerYAML `yaml:"trigger"`
	Workflows  []workflowRefYAML `yaml:"workflows"`
	Skills     []skillRefYAML    `yaml:"skills"`
	Agents     []agentRefYAML    `yaml:"agents"`
	MCPTools   []mcpToolRefYAML  `yaml:"mcp_tools"`

	RequiredSkillNames []string `yaml:"required_skill_names"`
	RequiredToolNames  []string `yaml:"required_tool_names"`

	Install            pluginInstallYAML    `yaml:"install"`
	DefaultBindingMode string               `yaml:"default_binding_mode"`
	Resolution         pluginResolutionYAML `yaml:"resolution"`
}

type pluginMetaYAML struct {
	Name        string            `yaml:"name"`
	Title       string            `yaml:"title"`
	Description string            `yaml:"description"`
	Version     string            `yaml:"version"`
	Author      string            `yaml:"author"`
	Domains     []string          `yaml:"domains"`
	Labels      map[string]string `yaml:"labels"`
}

type pluginTriggerYAML struct {
	SlashCommands []string `yaml:"slash_commands"`
	Keywords      []string `yaml:"keywords"`
	MinConfidence float64  `yaml:"min_confidence"`
	Description   string   `yaml:"description"`
}

type workflowRefYAML struct {
	Name        string `yaml:"name"`
	MinVersion  string `yaml:"min_version"`
	Required    bool   `yaml:"required"`
	Description string `yaml:"description"`
}

type skillRefYAML struct {
	Name        string `yaml:"name"`
	MinVersion  string `yaml:"min_version"`
	Required    bool   `yaml:"required"`
	Description string `yaml:"description"`
	Synthesize  bool   `yaml:"synthesize"`
}

type agentRefYAML struct {
	ID          string `yaml:"id"`
	Role        string `yaml:"role"`
	Required    bool   `yaml:"required"`
	Description string `yaml:"description"`
	Synthesize  bool   `yaml:"synthesize"`
}

type mcpToolRefYAML struct {
	ToolName    string `yaml:"tool_name"`
	ServerName  string `yaml:"server_name"`
	Required    bool   `yaml:"required"`
	Description string `yaml:"description"`
}

type pluginInstallYAML struct {
	AutoRegisterSkills bool   `yaml:"auto_register_skills"`
	AutoConfigureMCP   bool   `yaml:"auto_configure_mcp"`
	CreateDefaultAgent bool   `yaml:"create_default_agent"`
	DefaultAgentName   string `yaml:"default_agent_name"`
}

type pluginResolutionYAML struct {
	OnRequiredMissing        string `yaml:"on_required_missing"`
	OnOptionalMissing        string `yaml:"on_optional_missing"`
	ResynthesizeOnActivation bool   `yaml:"resynthesize_on_activation"`
}

// ─── Public API ───────────────────────────────────────────────────────────────

// LoadPlugin reads a plugin YAML file and returns the runtime Plugin struct.
func LoadPlugin(path string) (*Plugin, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("plugins: read %s: %w", path, err)
	}
	return ParsePlugin(data, path)
}

// ParsePlugin parses raw YAML bytes into a Plugin. sourcePath is used only
// for error messages and Plugin.SourcePath; pass "" when parsing from memory.
func ParsePlugin(data []byte, sourcePath string) (*Plugin, error) {
	// Expand environment variables so ${VAR} tokens work in descriptions etc.
	expanded := os.ExpandEnv(string(data))

	var py pluginYAML
	if err := yaml.Unmarshal([]byte(expanded), &py); err != nil {
		return nil, fmt.Errorf("plugins: parse %s: %w", sourcePath, err)
	}

	if err := validatePluginYAML(&py, sourcePath); err != nil {
		return nil, err
	}

	p := convertPlugin(&py)
	p.SourcePath = sourcePath
	return p, nil
}

// LoadPluginDir loads all *.yaml files from dir that have kind: Plugin.
// Files with other kinds are silently skipped. Returns all successfully loaded
// plugins; individual errors are collected and returned as a joined error.
func LoadPluginDir(dir string) ([]*Plugin, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("plugins: read dir %s: %w", dir, err)
	}

	var plugins []*Plugin
	var errs []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		p, err := LoadPlugin(path)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		plugins = append(plugins, p)
	}

	if len(errs) > 0 {
		return plugins, fmt.Errorf("plugins: %d file(s) failed to load:\n%s",
			len(errs), strings.Join(errs, "\n"))
	}
	return plugins, nil
}

// PluginToYAML serializes a Plugin back to YAML bytes. Used by the weaver to
// write newly created plugins to disk.
func PluginToYAML(p *Plugin) ([]byte, error) {
	py := &pluginYAML{
		APIVersion: "loom/v1",
		Kind:       "Plugin",
		Metadata: pluginMetaYAML{
			Name:        p.Name,
			Title:       p.Title,
			Description: p.Description,
			Version:     p.Version,
			Author:      p.Author,
			Domains:     p.Domains,
			Labels:      p.Labels,
		},
		Trigger: pluginTriggerYAML{
			SlashCommands: p.Trigger.SlashCommands,
			Keywords:      p.Trigger.Keywords,
			MinConfidence: p.Trigger.MinConfidence,
			Description:   p.Trigger.Description,
		},
		RequiredSkillNames: p.RequiredSkillNames,
		RequiredToolNames:  p.RequiredToolNames,
		DefaultBindingMode: p.DefaultBindingMode,
		Install: pluginInstallYAML{
			AutoRegisterSkills: p.Install.AutoRegisterSkills,
			AutoConfigureMCP:   p.Install.AutoConfigureMCP,
			CreateDefaultAgent: p.Install.CreateDefaultAgent,
			DefaultAgentName:   p.Install.DefaultAgentName,
		},
		Resolution: pluginResolutionYAML{
			OnRequiredMissing:        p.Resolution.OnRequiredMissing,
			OnOptionalMissing:        p.Resolution.OnOptionalMissing,
			ResynthesizeOnActivation: p.Resolution.ResynthesizeOnActivation,
		},
	}

	for _, r := range p.WorkflowRefs {
		py.Workflows = append(py.Workflows, workflowRefYAML(r))
	}
	for _, r := range p.SkillRefs {
		py.Skills = append(py.Skills, skillRefYAML(r))
	}
	for _, r := range p.AgentRefs {
		py.Agents = append(py.Agents, agentRefYAML(r))
	}
	for _, r := range p.MCPToolRefs {
		py.MCPTools = append(py.MCPTools, mcpToolRefYAML(r))
	}

	return yaml.Marshal(py)
}

// ─── Validation ───────────────────────────────────────────────────────────────

func validatePluginYAML(py *pluginYAML, sourcePath string) error {
	loc := sourcePath
	if loc == "" {
		loc = "<inline>"
	}

	if py.APIVersion != "loom/v1" {
		return fmt.Errorf("plugins: %s: apiVersion must be loom/v1, got %q", loc, py.APIVersion)
	}
	if py.Kind != "Plugin" {
		return fmt.Errorf("plugins: %s: kind must be Plugin, got %q", loc, py.Kind)
	}
	if py.Metadata.Name == "" {
		return fmt.Errorf("plugins: %s: metadata.name is required", loc)
	}
	if !kebabCase.MatchString(py.Metadata.Name) {
		return fmt.Errorf("plugins: %s: metadata.name %q must be kebab-case (lowercase letters, digits, hyphens)", loc, py.Metadata.Name)
	}
	if py.Metadata.Description == "" {
		return fmt.Errorf("plugins: %s: metadata.description is required", loc)
	}

	// At least one component ref must be present.
	if len(py.Workflows) == 0 && len(py.Skills) == 0 && len(py.Agents) == 0 && len(py.MCPTools) == 0 {
		return fmt.Errorf("plugins: %s: at least one of workflows, skills, agents, or mcp_tools must be non-empty", loc)
	}

	// Validate synthesize refs have descriptions.
	for i, s := range py.Skills {
		if s.Synthesize && s.Description == "" {
			return fmt.Errorf("plugins: %s: skills[%d] (%q): synthesize=true requires description", loc, i, s.Name)
		}
	}
	for i, a := range py.Agents {
		if a.Synthesize && a.Description == "" {
			return fmt.Errorf("plugins: %s: agents[%d] (%q): synthesize=true requires description", loc, i, a.ID)
		}
	}

	// Validate binding mode.
	if !validBindingModes[strings.ToUpper(py.DefaultBindingMode)] {
		return fmt.Errorf("plugins: %s: default_binding_mode %q is not valid (EAGER|LAZY|ALWAYS)", loc, py.DefaultBindingMode)
	}

	// Validate resolution modes.
	if !validResolutionModes[strings.ToUpper(py.Resolution.OnRequiredMissing)] {
		return fmt.Errorf("plugins: %s: resolution.on_required_missing %q is not valid (FAIL|SKIP_WARN|SKIP_SILENT)", loc, py.Resolution.OnRequiredMissing)
	}
	if !validResolutionModes[strings.ToUpper(py.Resolution.OnOptionalMissing)] {
		return fmt.Errorf("plugins: %s: resolution.on_optional_missing %q is not valid (FAIL|SKIP_WARN|SKIP_SILENT)", loc, py.Resolution.OnOptionalMissing)
	}

	return nil
}

// ─── Conversion ───────────────────────────────────────────────────────────────

func convertPlugin(py *pluginYAML) *Plugin {
	p := &Plugin{
		Name:               py.Metadata.Name,
		Title:              firstNonEmpty(py.Metadata.Title, py.Metadata.Name),
		Description:        py.Metadata.Description,
		Version:            firstNonEmpty(py.Metadata.Version, "1.0.0"),
		Author:             py.Metadata.Author,
		Domains:            py.Metadata.Domains,
		Labels:             py.Metadata.Labels,
		RequiredSkillNames: py.RequiredSkillNames,
		RequiredToolNames:  py.RequiredToolNames,
		DefaultBindingMode: normalizeMode(py.DefaultBindingMode, "LAZY"),
		Trigger: PluginTrigger{
			SlashCommands: py.Trigger.SlashCommands,
			Keywords:      py.Trigger.Keywords,
			MinConfidence: defaultFloat(py.Trigger.MinConfidence, 0.7),
			Description:   py.Trigger.Description,
		},
		Install: PluginInstallPolicy{
			AutoRegisterSkills: py.Install.AutoRegisterSkills,
			AutoConfigureMCP:   py.Install.AutoConfigureMCP,
			CreateDefaultAgent: py.Install.CreateDefaultAgent,
			DefaultAgentName:   py.Install.DefaultAgentName,
		},
		Resolution: PluginResolutionPolicy{
			OnRequiredMissing:        normalizeMode(py.Resolution.OnRequiredMissing, "FAIL"),
			OnOptionalMissing:        normalizeMode(py.Resolution.OnOptionalMissing, "SKIP_WARN"),
			ResynthesizeOnActivation: py.Resolution.ResynthesizeOnActivation,
		},
	}

	for _, r := range py.Workflows {
		p.WorkflowRefs = append(p.WorkflowRefs, PluginWorkflowRef(r))
	}
	for _, r := range py.Skills {
		p.SkillRefs = append(p.SkillRefs, PluginSkillRef(r))
	}
	for _, r := range py.Agents {
		p.AgentRefs = append(p.AgentRefs, PluginAgentRef(r))
	}
	for _, r := range py.MCPTools {
		p.MCPToolRefs = append(p.MCPToolRefs, PluginMCPToolRef(r))
	}

	return p
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func defaultFloat(v, def float64) float64 {
	if v == 0 {
		return def
	}
	return v
}

func normalizeMode(v, def string) string {
	u := strings.ToUpper(v)
	if u == "" {
		return def
	}
	return u
}
