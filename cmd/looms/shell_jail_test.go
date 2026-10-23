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
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/shuttle/builtin"
)

func TestValidateShellExecute(t *testing.T) {
	binding := func(policy string) shuttle.HookBinding {
		return shuttle.HookBinding{Kind: "command-policy", Scope: "shell_execute", Policy: policy}
	}
	for name, tc := range map[string]struct {
		se       ShellExecuteConfig
		bindings []shuttle.HookBinding
		err      string
	}{
		"bash default":           {se: ShellExecuteConfig{}},
		"jailed readonly":        {se: ShellExecuteConfig{Mode: "jailed"}},
		"bad mode":               {se: ShellExecuteConfig{Mode: "docker"}, err: "want bash|jailed"},
		"unknown runtime policy": {se: ShellExecuteConfig{Mode: "jailed", Policy: "nope"}, err: `policy "nope" is not defined`},
		"custom policy": {se: ShellExecuteConfig{Mode: "jailed", Policy: "dev",
			Policies: map[string]ShellPolicyConfig{"dev": {Allow: []string{"make"}}}}},
		"extends unknown":    {se: ShellExecuteConfig{Policies: map[string]ShellPolicyConfig{"dev": {Extends: "base"}}}, err: "extends unknown policy"},
		"extends loop":       {se: ShellExecuteConfig{Policies: map[string]ShellPolicyConfig{"a": {Extends: "b"}, "b": {Extends: "a"}}}, err: "loop"},
		"readonly redefined": {se: ShellExecuteConfig{Policies: map[string]ShellPolicyConfig{"readonly": {}}}, err: "built in"},
		"binding in bash mode": {se: ShellExecuteConfig{}, bindings: []shuttle.HookBinding{binding("readonly")},
			err: "needs tools.shell_execute.mode: jailed"},
		"binding names another policy": {se: ShellExecuteConfig{Mode: "jailed", Policy: "readonly",
			Policies: map[string]ShellPolicyConfig{"dev": {}}}, bindings: []shuttle.HookBinding{binding("dev")}, err: "they must match"},
		"binding matches": {se: ShellExecuteConfig{Mode: "jailed"}, bindings: []shuttle.HookBinding{binding("readonly")}},
		"wildcard scope counts": {se: ShellExecuteConfig{}, bindings: []shuttle.HookBinding{
			{Kind: "command-policy", Scope: "shell_*", Policy: "readonly"}}, err: "needs tools.shell_execute.mode: jailed"},
		"other tools are not checked": {se: ShellExecuteConfig{}, bindings: []shuttle.HookBinding{
			{Kind: "command-policy", Scope: "shell_execute_sandbox", Policy: "x", Enforcement: "static"}}},
	} {
		c := &Config{}
		c.Tools.ShellExecute = tc.se
		c.Tools.Hooks.Bindings = tc.bindings
		err := c.validateShellExecute()
		if tc.err == "" {
			assert.NoError(t, err, name)
		} else {
			require.Error(t, err, name)
			assert.Contains(t, err.Error(), tc.err, name)
		}
	}
}

func TestLoadConfig_ShellExecuteJailed(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "looms.yaml")
	yaml := `
tools:
  shell_execute:
    mode: jailed
    policy: dev
    search_path: [/usr/bin, /bin]
    memory_bytes: 268435456
    read_roots: [/opt/data]
    policies:
      dev:
        allow: [make, go]
        never: [curl]
  hooks:
    - kind: command-policy
      scope: shell_execute
      policy: dev
`
	require.NoError(t, os.WriteFile(cfgPath, []byte(yaml), 0o600))
	config, err := LoadConfig(cfgPath)
	require.NoError(t, err)
	se := config.Tools.ShellExecute
	assert.Equal(t, "jailed", se.Mode)
	assert.Equal(t, "dev", se.Policy)
	assert.Equal(t, []string{"/usr/bin", "/bin"}, se.SearchPath)
	assert.Equal(t, uint64(268435456), se.MemoryBytes)
	assert.True(t, se.Strict, "strict defaults to true")
	assert.Equal(t, []string{"make", "go"}, se.Policies["dev"].Allow)
	assert.Equal(t, []string{"curl"}, se.Policies["dev"].Never)
	require.Len(t, config.Tools.Hooks.Bindings, 1)
	assert.Equal(t, "dev", config.Tools.Hooks.Bindings[0].Policy)
	assert.NoError(t, config.validateShellExecute())
}

func TestSetupShellJail(t *testing.T) {
	t.Cleanup(func() { builtin.SetShellJail(nil) })
	logger := zap.NewNop()
	data, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	t.Setenv("LOOM_DATA_DIR", data)

	c := &Config{}
	factory, err := setupShellJail(context.Background(), c, data, logger)
	require.NoError(t, err)
	assert.Nil(t, factory, "bash mode wires no command policies")
	assert.Nil(t, builtin.ShellJail())

	if runtime.GOOS == "windows" {
		t.Skip("jailed mode is not supported on windows")
	}
	c.Tools.ShellExecute = ShellExecuteConfig{Mode: "jailed", Strict: true,
		Policies: map[string]ShellPolicyConfig{"dev": {Allow: []string{"make"}}}}
	factory, err = setupShellJail(context.Background(), c, data, logger)
	require.NoError(t, err)
	require.NotNil(t, factory)
	require.NotNil(t, builtin.ShellJail(), "the runner is installed for tools created afterwards")
	_, err = factory(shuttle.HookBinding{Kind: "command-policy", Scope: "shell_execute", Policy: "dev"})
	assert.NoError(t, err, "every configured policy is known to the hook")
	_, err = factory(shuttle.HookBinding{Kind: "command-policy", Scope: "shell_execute", Policy: "nope"})
	assert.Error(t, err)

	c.Tools.ShellExecute.SearchPath = []string{"relative"}
	_, err = setupShellJail(context.Background(), c, data, logger)
	assert.ErrorContains(t, err, "not absolute")
}
