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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/luasandbox"
	"github.com/teradata-labs/loom/pkg/luasandbox/store"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// TestLuaTools_DefaultOff locks in that run_lua is opt-in: the server switch
// defaults to false and the default deny lists are populated.
func TestLuaTools_DefaultOff(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	setDefaults()

	var cfg Config
	require.NoError(t, viper.Unmarshal(&cfg))
	assert.False(t, cfg.Tools.Lua.Enabled, "tools.lua.enabled MUST default to false")
	assert.Equal(t, defaultLuaDeny, cfg.Tools.Lua.Tools.Deny)
	assert.Equal(t, defaultLuaDenyForShared, cfg.Tools.Lua.Tools.DenyForShared)
	assert.Empty(t, cfg.Tools.Lua.Tools.Allow)
	assert.Equal(t, 2, cfg.Tools.Lua.Limits.MaxConcurrentRunsPerKey)

	rt, err := newLuaRuntime(cfg.Tools.Lua, 4<<30, t.TempDir())
	require.NoError(t, err)
	assert.Nil(t, rt, "a disabled tools.lua builds no runtime")
	assert.Nil(t, rt.registryOptions())
}

func TestNewLuaRuntime(t *testing.T) {
	const gib = uint64(1) << 30
	base := LuaToolsConfig{Enabled: true, Limits: LuaLimitsConfig{MaxConcurrentRunsPerKey: 2}}

	t.Run("derived slots at the default budget", func(t *testing.T) {
		rt, err := newLuaRuntime(base, 4*gib, t.TempDir())
		require.NoError(t, err)
		assert.Equal(t, 4, rt.slots, "4 GiB pod at 128 MiB per run")
		assert.Equal(t, 2, rt.perKey)
		assert.Equal(t, uint64(128<<20), rt.runMem)
		_, total, perKey := rt.options.Gate.Stats()
		assert.Equal(t, 4, total)
		assert.Equal(t, 2, perKey)
	})
	t.Run("a configured slot count only lowers", func(t *testing.T) {
		c := base
		c.Limits.MaxConcurrentRuns = 16
		rt, err := newLuaRuntime(c, 4*gib, t.TempDir())
		require.NoError(t, err)
		assert.Equal(t, 4, rt.slots, "16 configured cannot exceed the 4 derived")
		c.Limits.MaxConcurrentRuns = 1
		rt, err = newLuaRuntime(c, 4*gib, t.TempDir())
		require.NoError(t, err)
		assert.Equal(t, 1, rt.slots)
		assert.Equal(t, 1, rt.perKey, "per-agent runs never exceed the total")
	})
	t.Run("small pods lower the per-run budget", func(t *testing.T) {
		rt, err := newLuaRuntime(base, gib, t.TempDir())
		require.NoError(t, err)
		assert.Equal(t, 2, rt.slots)
		assert.Less(t, rt.runMem, uint64(128<<20))
		pol, err := rt.options.Policy(context.Background())
		require.NoError(t, err)
		assert.Equal(t, rt.runMem, pol.Limits.MemoryBytes, "runs use the lowered budget")
	})
	t.Run("limits are converted and capped", func(t *testing.T) {
		c := base
		c.Limits.WallSeconds = 30
		c.Limits.MemoryBytes = 8 << 30 // above the 512 MiB ceiling
		c.Limits.MaxToolCalls = 5
		rt, err := newLuaRuntime(c, 64*gib, t.TempDir())
		require.NoError(t, err)
		pol, err := rt.options.Policy(context.Background())
		require.NoError(t, err)
		assert.Equal(t, 30*time.Second, pol.Limits.Wall)
		assert.Equal(t, luasandbox.MaxLimits().MemoryBytes, pol.Limits.MemoryBytes)
		assert.Equal(t, 5, pol.Limits.MaxToolCalls)
		assert.Equal(t, luasandbox.DefaultLimits().CPUTicks, pol.Limits.CPUTicks, "unset fields take defaults")
	})
	t.Run("a malformed pattern fails closed", func(t *testing.T) {
		c := base
		c.Tools.Deny = []string{"web_["}
		_, err := newLuaRuntime(c, 4*gib, t.TempDir())
		assert.Error(t, err)
	})
}

type luaGuardHook struct{}

func (luaGuardHook) Matches(shuttle.AdmissionRequest) bool { return true }
func (luaGuardHook) Evaluate(shuttle.AdmissionRequest) shuttle.Decision {
	return shuttle.Decision{Kind: shuttle.Allow}
}

func guardedAgent(opts ...agent.Option) *agent.Agent {
	opts = append(opts, agent.WithAdmissionHooks(shuttle.NewChain([]shuttle.Hook{luaGuardHook{}}, nil, nil)))
	return agent.NewAgent(nil, nil, opts...)
}

func enabledLuaRuntime(t *testing.T) *luaRuntime {
	t.Helper()
	rt, err := newLuaRuntime(LuaToolsConfig{Enabled: true, Limits: LuaLimitsConfig{MaxConcurrentRunsPerKey: 2}}, 4<<30, t.TempDir())
	require.NoError(t, err)
	return rt
}

// The registration matrix from the design (L2.3).
func TestRegisterLuaTools(t *testing.T) {
	t.Run("disabled: absent even when listed", func(t *testing.T) {
		ag := guardedAgent()
		registerLuaTools(ag, newCfg("run_lua"), nil, zap.NewNop(), "  ")
		assert.False(t, hasTool(ag, agent.RunLuaToolName))
	})
	t.Run("enabled and listed: present", func(t *testing.T) {
		ag := guardedAgent()
		registerLuaTools(ag, newCfg("run_lua"), enabledLuaRuntime(t), zap.NewNop(), "  ")
		assert.True(t, hasTool(ag, agent.RunLuaToolName))
	})
	t.Run("enabled but not listed: absent", func(t *testing.T) {
		ag := guardedAgent()
		registerLuaTools(ag, newCfg("http_request"), enabledLuaRuntime(t), zap.NewNop(), "  ")
		assert.False(t, hasTool(ag, agent.RunLuaToolName))
	})
	t.Run("no guard: absent with one warning naming the reason", func(t *testing.T) {
		core, logs := observer.New(zapcore.InfoLevel)
		ag := agent.NewAgent(nil, nil) // no admission chain, no permission checker
		registerLuaTools(ag, newCfg("run_lua"), enabledLuaRuntime(t), zap.New(core), "  ")
		assert.False(t, hasTool(ag, agent.RunLuaToolName))
		warns := logs.FilterLevelExact(zapcore.WarnLevel).All()
		require.Len(t, warns, 1)
		assert.Contains(t, warns[0].Message, "no admission chain and no permission checker")
	})
	t.Run("suppressed: absent", func(t *testing.T) {
		ag := guardedAgent(agent.WithoutBuiltinTool(agent.RunLuaToolName))
		registerLuaTools(ag, newCfg("run_lua"), enabledLuaRuntime(t), zap.NewNop(), "  ")
		assert.False(t, hasTool(ag, agent.RunLuaToolName))
	})
}

// registerYAMLBuiltinTools must not try to build run_lua from builtin.ByName,
// so listing it never logs "Unknown builtin tool".
func TestRegisterYAMLBuiltinTools_SkipsLuaTools(t *testing.T) {
	withViperMinimalMode(t, false, false)
	core, logs := observer.New(zapcore.DebugLevel)
	ag := agent.NewAgent(nil, nil)
	registerYAMLBuiltinTools(ag, newCfg("run_lua", "manage_lua_scripts"), nil, zap.New(core), "  ", "agent_management")
	assert.Empty(t, logs.FilterMessage("  Unknown builtin tool").All())
	assert.False(t, hasTool(ag, agent.RunLuaToolName))
}

// The switch is reachable from the environment, as the config docs say.
func TestLuaTools_EnvSwitch(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.SetEnvPrefix("LOOM")
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	viper.AutomaticEnv()
	setDefaults()
	t.Setenv("LOOM_TOOLS_LUA_ENABLED", "true")

	var cfg Config
	require.NoError(t, viper.Unmarshal(&cfg))
	assert.True(t, cfg.Tools.Lua.Enabled)
}

func TestLuaTools_ScriptDefaults(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	setDefaults()
	var cfg Config
	require.NoError(t, viper.Unmarshal(&cfg))
	assert.True(t, cfg.Tools.Lua.Scripts.SaveEnabled, "saving defaults on (inside a disabled feature)")
	assert.False(t, cfg.Tools.Lua.Scripts.PublishAsToolEnabled, "publishing as a tool MUST default off")
	assert.Empty(t, cfg.Tools.Lua.ScriptsDir)
}

func TestNewLuaRuntime_OpensTheScriptStore(t *testing.T) {
	dataDir := t.TempDir()
	rt, err := newLuaRuntime(LuaToolsConfig{Enabled: true, Scripts: LuaScriptsConfig{SaveEnabled: true}}, 4<<30, dataDir)
	require.NoError(t, err)
	require.NotNil(t, rt.store)
	assert.DirExists(t, filepath.Join(dataDir, "lua_scripts"), "default scripts_dir")
	assert.True(t, rt.scripts.SaveEnabled)
	assert.False(t, rt.scripts.PublishEnabled)
	assert.NotNil(t, rt.options.Resolve, "run_lua resolves names against the store")
	assert.NotNil(t, rt.scriptsRegistryOptions())

	custom := t.TempDir()
	rt, err = newLuaRuntime(LuaToolsConfig{Enabled: true, ScriptsDir: custom}, 4<<30, dataDir)
	require.NoError(t, err)
	_, _, err = rt.store.Save(context.Background(), store.Script{Name: "abc", Description: "d", Source: "return 1"}, false)
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(custom, "abc.lua"))

	file := filepath.Join(t.TempDir(), "not-a-dir")
	require.NoError(t, os.WriteFile(file, nil, 0o600))
	_, err = newLuaRuntime(LuaToolsConfig{Enabled: true, ScriptsDir: file}, 4<<30, dataDir)
	assert.Error(t, err, "an unusable scripts_dir aborts startup")
}

func TestRegisterLuaTools_SavedScripts(t *testing.T) {
	rt, err := newLuaRuntime(LuaToolsConfig{Enabled: true, Scripts: LuaScriptsConfig{SaveEnabled: true},
		Limits: LuaLimitsConfig{MaxConcurrentRunsPerKey: 2}}, 4<<30, t.TempDir())
	require.NoError(t, err)
	ctx := context.Background()
	params := map[string]any{"type": "object", "properties": map[string]any{}}
	for _, name := range []string{"attached_one", "custom_one"} {
		_, _, err := rt.store.Save(ctx, store.Script{Name: name, Description: "d", Source: "return 1", Manifest: &store.Manifest{Parameters: params}}, false)
		require.NoError(t, err)
		require.NoError(t, rt.store.SetPublished(ctx, name, true))
	}
	require.NoError(t, rt.store.Attach(ctx, "lua-agent", "attached_one"))

	ag := guardedAgent(agent.WithName("lua-agent"))
	cfg := newCfg("run_lua", "manage_lua_scripts")
	cfg.Name = "lua-agent"
	cfg.Tools.Custom = []*loomv1.CustomToolConfig{{Name: "custom", Implementation: "lua://custom_one"}}
	registerLuaTools(ag, cfg, rt, zap.NewNop(), "  ")
	for _, name := range []string{agent.RunLuaToolName, agent.ManageLuaScriptsToolName, "lua_attached_one", "lua_custom_one"} {
		assert.True(t, hasTool(ag, name), name)
	}

	core, logs := observer.New(zapcore.InfoLevel)
	noSave := *rt
	noSave.scripts.SaveEnabled = false
	ag2 := guardedAgent(agent.WithName("other"))
	registerLuaTools(ag2, newCfg("run_lua", "manage_lua_scripts"), &noSave, zap.New(core), "  ")
	assert.True(t, hasTool(ag2, agent.RunLuaToolName))
	assert.False(t, hasTool(ag2, agent.ManageLuaScriptsToolName))
	assert.Len(t, logs.FilterMessage("  Lua script tool not registered").All(), 1)
}
