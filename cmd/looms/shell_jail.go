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
	"fmt"
	"sort"
	"strings"

	"go.uber.org/zap"

	"github.com/teradata-labs/loom/pkg/shelljail"
	"github.com/teradata-labs/loom/pkg/shellpolicy"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/shuttle/builtin"
)

// shellPolicies builds the configured shell policies (plus the built-in
// readonly) and returns them with the runtime policy's name.
func (c ShellExecuteConfig) shellPolicies() (map[string]*shellpolicy.Policy, string, error) {
	names := make([]string, 0, len(c.Policies))
	for n := range c.Policies {
		names = append(names, n)
	}
	sort.Strings(names)
	specs := make([]shellpolicy.Spec, 0, len(names))
	for _, n := range names {
		p := c.Policies[n]
		specs = append(specs, shellpolicy.Spec{Name: n, Extends: p.Extends, Allow: p.Allow, Never: p.Never})
	}
	policies, err := shellpolicy.Build(specs)
	if err != nil {
		return nil, "", fmt.Errorf("tools.shell_execute.policies: %w", err)
	}
	name := strings.TrimSpace(c.Policy)
	if name == "" {
		name = "readonly"
	}
	if _, ok := policies[name]; !ok {
		return nil, "", fmt.Errorf("tools.shell_execute.policy %q is not defined", name)
	}
	return policies, name, nil
}

// validateShellExecute checks tools.shell_execute without starting anything.
func (c *Config) validateShellExecute() error {
	se := c.Tools.ShellExecute
	switch se.Mode {
	case "", "bash", "jailed":
	default:
		return fmt.Errorf("tools.shell_execute.mode %q (want bash|jailed)", se.Mode)
	}
	if _, _, err := se.shellPolicies(); err != nil {
		return err
	}
	return checkCommandPolicyBindings(c.Tools.Hooks.Bindings, se)
}

// checkCommandPolicyBindings refuses a command-policy binding on
// shell_execute that no runner would enforce (bash mode), or that names a
// policy other than the one the runner enforces.
func checkCommandPolicyBindings(bindings []shuttle.HookBinding, se ShellExecuteConfig) error {
	runtimePolicy := strings.TrimSpace(se.Policy)
	if runtimePolicy == "" {
		runtimePolicy = "readonly"
	}
	scope := shuttle.AdmissionRequest{ToolName: "shell_execute"}
	for i, b := range bindings {
		if b.Kind != "command-policy" || !scope.MatchesTool(shuttle.NewToolScope(b.Scope)) {
			continue
		}
		if se.Mode != "jailed" {
			return fmt.Errorf("tools.hooks[%d]: a command-policy binding on shell_execute needs tools.shell_execute.mode: jailed — in bash mode nothing enforces what the hook approves", i)
		}
		if b.Policy != runtimePolicy {
			return fmt.Errorf("tools.hooks[%d]: command-policy names policy %q but jailed shell_execute runs policy %q (tools.shell_execute.policy); they must match", i, b.Policy, runtimePolicy)
		}
	}
	return nil
}

// setupShellJail turns on jailed mode when configured: it builds the runner,
// pins programs, runs the self-test, installs the runner for every
// shell_execute tool created afterwards, and returns the command-policy
// factory for the admission chain. In bash mode it returns nil, so a
// command-policy binding fails the chain build (fail-closed).
func setupShellJail(ctx context.Context, config *Config, loomDataDir string, logger *zap.Logger) (shuttle.CommandPolicyFactory, error) {
	se := config.Tools.ShellExecute
	if se.Mode != "jailed" {
		builtin.SetShellJail(nil)
		return nil, nil
	}
	policies, name, err := se.shellPolicies()
	if err != nil {
		return nil, err
	}
	runner, warnings, err := shelljail.NewRunner(shelljail.Config{
		Policy:      policies[name],
		SearchPath:  se.SearchPath,
		Limits:      shelljail.Limits{MemoryBytes: se.MemoryBytes, FileBytes: se.FileBytes},
		Strict:      se.Strict,
		LoomDataDir: loomDataDir,
		ExtraRead:   se.ReadRoots,
		ExtraWrite:  se.WriteRoots,
	})
	if err != nil {
		return nil, fmt.Errorf("jailed shell: %w", err)
	}
	for _, w := range warnings {
		logger.Warn("Jailed shell: program left out", zap.String("detail", w))
	}
	if err := runner.SelfTest(ctx); err != nil {
		return nil, err
	}
	builtin.SetShellJail(runner)
	logger.Info("shell_execute runs jailed",
		zap.String("policy", name),
		zap.Int("pinned_programs", len(runner.Pinned())),
		zap.Bool("strict", se.Strict))
	return shellpolicy.Factory(policies, runner.OptionsFunc()), nil
}
