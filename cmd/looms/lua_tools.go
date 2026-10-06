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
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/spf13/viper"
	"go.uber.org/zap"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/luasandbox"
	"github.com/teradata-labs/loom/pkg/luasandbox/store/filestore"
)

// LuaToolsConfig is the tools.lua block: the run_lua tool, off by default.
type LuaToolsConfig struct {
	// Enabled is the master switch (env LOOM_TOOLS_LUA_ENABLED). When false,
	// run_lua is never registered, even for agents that list it.
	Enabled bool `mapstructure:"enabled"`
	// Limits bound every run. Zero means the engine default.
	Limits LuaLimitsConfig `mapstructure:"limits"`
	// Tools restricts what scripts may call, on top of what the model sees.
	Tools LuaToolListsConfig `mapstructure:"tools"`
	// ScriptsDir holds saved scripts. Empty means <LOOM_DATA_DIR>/lua_scripts.
	ScriptsDir string `mapstructure:"scripts_dir"`
	// Scripts controls saved scripts.
	Scripts LuaScriptsConfig `mapstructure:"scripts"`
}

// LuaScriptsConfig is tools.lua.scripts.
type LuaScriptsConfig struct {
	// SaveEnabled lets agents that list manage_lua_scripts save and manage
	// scripts. Default true. run_lua can run saved scripts by name either way.
	SaveEnabled bool `mapstructure:"save_enabled"`
	// PublishAsToolEnabled lets manage_lua_scripts publish a script as a
	// tool named lua_<name>. Default false.
	PublishAsToolEnabled bool `mapstructure:"publish_as_tool_enabled"`
}

// LuaLimitsConfig mirrors luasandbox.Limits in config units. Values above the
// engine's ceilings are lowered to them.
type LuaLimitsConfig struct {
	WallSeconds            int    `mapstructure:"wall_seconds"`
	CPUTicks               uint64 `mapstructure:"cpu_ticks"`
	MemoryBytes            uint64 `mapstructure:"memory_bytes"`
	MaxToolCalls           int    `mapstructure:"max_tool_calls"`
	ToolCallTimeoutSeconds int    `mapstructure:"tool_call_timeout_seconds"`
	MaxCallResultBytes     int    `mapstructure:"max_call_result_bytes"`
	MaxOutputBytes         int    `mapstructure:"max_output_bytes"`
	MaxResultBytes         int    `mapstructure:"max_result_bytes"`
	MaxSourceBytes         int    `mapstructure:"max_source_bytes"`
	MaxSleepSeconds        int    `mapstructure:"max_sleep_seconds"`
	// MaxConcurrentRuns caps the run slots per server. 0 derives them from the
	// process memory limit; a positive value can only lower the derived count.
	MaxConcurrentRuns int `mapstructure:"max_concurrent_runs"`
	// MaxConcurrentRunsPerKey caps concurrent runs per agent.
	MaxConcurrentRunsPerKey int `mapstructure:"max_concurrent_runs_per_key"`
}

// LuaToolListsConfig holds the script tool lists. Entries are exact names or
// path.Match patterns ("web_*").
type LuaToolListsConfig struct {
	// Allow, when non-empty, restricts scripts to these tools.
	Allow []string `mapstructure:"allow"`
	// Deny removes tools for every script.
	Deny []string `mapstructure:"deny"`
	// DenyForShared removes tools for scripts the runner did not write.
	DenyForShared []string `mapstructure:"deny_for_shared"`
}

// Default lists, from the design (01 §6): tools that are slow, hold locks,
// manage agents or skills, or (for shared scripts) can send data out.
// shell_execute is loom's own unsandboxed shell: a script calling it in a loop
// would run commands nobody sees one by one, and under the default YOLO
// permissions nothing checks them. It stays denied until a command-policy
// hook governs it; an operator who binds one can remove it from the list.
var (
	defaultLuaDeny          = []string{"shell_execute", "shell_execute_sandbox", "agent_management", "project_manager", "git_contribute", "propose_skill_edit"}
	defaultLuaDenyForShared = []string{"http_request", "web_browse", "web_search", "file_write", "files", "workspace"}
)

// setLuaDefaults registers the tools.lua defaults.
func setLuaDefaults() {
	viper.SetDefault("tools.lua.enabled", false)
	viper.SetDefault("tools.lua.limits.max_concurrent_runs", 0)
	viper.SetDefault("tools.lua.limits.max_concurrent_runs_per_key", 2)
	viper.SetDefault("tools.lua.tools.allow", []string{})
	viper.SetDefault("tools.lua.tools.deny", defaultLuaDeny)
	viper.SetDefault("tools.lua.tools.deny_for_shared", defaultLuaDenyForShared)
	viper.SetDefault("tools.lua.scripts_dir", "")
	viper.SetDefault("tools.lua.scripts.save_enabled", true)
	viper.SetDefault("tools.lua.scripts.publish_as_tool_enabled", false)
}

// limits converts the config to engine limits (normalized).
func (c LuaLimitsConfig) limits() luasandbox.Limits {
	sec := func(n int) time.Duration { return time.Duration(n) * time.Second }
	return luasandbox.Limits{
		Wall:               sec(c.WallSeconds),
		CPUTicks:           c.CPUTicks,
		MemoryBytes:        c.MemoryBytes,
		MaxToolCalls:       c.MaxToolCalls,
		ToolCallTimeout:    sec(c.ToolCallTimeoutSeconds),
		MaxCallResultBytes: c.MaxCallResultBytes,
		MaxOutputBytes:     c.MaxOutputBytes,
		MaxResultBytes:     c.MaxResultBytes,
		MaxSourceBytes:     c.MaxSourceBytes,
		MaxSleep:           sec(c.MaxSleepSeconds),
	}.Normalize()
}

// luaRuntime is the server-wide run_lua wiring: one policy, one gate and one
// script store.
type luaRuntime struct {
	options agent.RunLuaToolOptions
	scripts agent.LuaScriptsOptions
	store   *filestore.Store
	slots   int
	perKey  int
	runMem  uint64
}

// newLuaRuntime builds the run_lua wiring from tools.lua. It returns nil when
// Lua is disabled, and an error for a malformed policy or an unusable scripts
// directory, which aborts serve like a malformed hook binding (fail closed).
// memLimit is the process memory limit used to size the gate; dataDir is the
// default parent of the scripts directory.
func newLuaRuntime(c LuaToolsConfig, memLimit uint64, dataDir string) (*luaRuntime, error) {
	if !c.Enabled {
		return nil, nil
	}
	pol := luasandbox.Policy{
		Limits:        c.Limits.limits(),
		Allow:         c.Tools.Allow,
		Deny:          c.Tools.Deny,
		DenyForShared: c.Tools.DenyForShared,
	}
	if err := pol.Validate(); err != nil {
		return nil, fmt.Errorf("tools.lua.tools: %w", err)
	}
	slots, runMem := luasandbox.DeriveCapacity(memLimit, pol.Limits.MemoryBytes)
	if c.Limits.MaxConcurrentRuns > 0 && c.Limits.MaxConcurrentRuns < slots {
		slots = c.Limits.MaxConcurrentRuns
	}
	pol.Limits.MemoryBytes = runMem
	perKey := c.Limits.MaxConcurrentRunsPerKey
	if perKey <= 0 || perKey > slots {
		perKey = slots
	}
	dir := c.ScriptsDir
	if dir == "" {
		dir = filepath.Join(dataDir, "lua_scripts")
	}
	st, err := filestore.Open(dir, pol.Limits)
	if err != nil {
		return nil, fmt.Errorf("tools.lua.scripts_dir: %w", err)
	}
	return &luaRuntime{
		options: agent.RunLuaToolOptions{
			Policy:  func(context.Context) (luasandbox.Policy, error) { return pol, nil },
			Gate:    luasandbox.NewGate(slots, perKey),
			Resolve: agent.StoreScriptResolver(st),
		},
		scripts: agent.LuaScriptsOptions{
			Store:          st,
			SaveEnabled:    c.Scripts.SaveEnabled,
			PublishEnabled: c.Scripts.PublishAsToolEnabled,
		},
		store:  st,
		slots:  slots,
		perKey: perKey,
		runMem: runMem,
	}, nil
}

// scriptsRegistryOptions returns the saved-script wiring for
// agent.RegistryConfig, nil when Lua is disabled.
func (rt *luaRuntime) scriptsRegistryOptions() *agent.LuaScriptsOptions {
	if rt == nil {
		return nil
	}
	opts := rt.scripts
	return &opts
}

// registryOptions returns the wiring for agent.RegistryConfig, nil when Lua
// is disabled.
func (rt *luaRuntime) registryOptions() *agent.RunLuaToolOptions {
	if rt == nil {
		return nil
	}
	opts := rt.options
	return &opts
}

// listsBuiltin reports whether cfg lists name in tools.builtin.
func listsBuiltin(cfg *loomv1.AgentConfig, name string) bool {
	if cfg == nil || cfg.Tools == nil {
		return false
	}
	for _, n := range cfg.Tools.Builtin {
		if n == name {
			return true
		}
	}
	return false
}

// registerLuaTools registers run_lua on an agent built by serve's own loops
// (cold start and hot reload) when its YAML lists it. Registry-built agents go
// through agent.RegistryConfig.RunLuaTool instead; both refuse the same cases.
func registerLuaTools(ag *agent.Agent, cfg *loomv1.AgentConfig, rt *luaRuntime, logger *zap.Logger, indent string) {
	if !listsBuiltin(cfg, agent.RunLuaToolName) {
		return
	}
	if rt == nil {
		logger.Info(indent+"run_lua listed but tools.lua.enabled is false; not registered", zap.String("agent", cfg.Name))
		return
	}
	err := ag.RegisterRunLuaTool(rt.options)
	switch {
	case err == nil:
		logger.Info(indent+"  Tool registered", zap.String("name", agent.RunLuaToolName))
	case errors.Is(err, agent.ErrLuaNoGuards):
		logger.Warn(indent+"run_lua not registered: the agent has no admission chain and no permission checker, "+
			"so scripts would run unguarded; configure tools.hooks or tools.permissions",
			zap.String("agent", cfg.Name))
	case errors.Is(err, agent.ErrLuaToolSuppressed):
		logger.Info(indent+"run_lua suppressed on this agent; not registered", zap.String("agent", cfg.Name))
	default:
		logger.Warn(indent+"run_lua not registered", zap.String("agent", cfg.Name), zap.Error(err))
	}
	if err != nil {
		return
	}
	var custom []string
	for _, c := range cfg.GetTools().GetCustom() {
		if name, ok := agent.LuaScriptFromImplementation(c.GetImplementation()); ok {
			custom = append(custom, name)
		}
	}
	rep := ag.RegisterLuaScriptTools(context.Background(), rt.scripts, listsBuiltin(cfg, agent.ManageLuaScriptsToolName), custom)
	for _, name := range rep.Registered {
		logger.Info(indent+"  Tool registered", zap.String("name", name))
	}
	for name, reason := range rep.Skipped {
		logger.Warn(indent+"Lua script tool not registered", zap.String("agent", cfg.Name),
			zap.String("tool", name), zap.String("reason", reason))
	}
}
