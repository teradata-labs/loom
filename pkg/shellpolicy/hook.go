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

package shellpolicy

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"sort"
	"strings"

	"github.com/teradata-labs/loom/pkg/shuttle"
)

// CommandParam is the parameter holding the shell command, for every tool the
// command-policy hook governs (shell_execute, shell_execute_sandbox).
const CommandParam = "command"

// shellParam and envParam are shell_execute's parameters that change how the
// command runs: the shell that runs it, and variables merged into its
// environment.
const (
	shellParam = "shell"
	envParam   = "env"
)

// goos is the operating system the hook judges commands for. It is
// runtime.GOOS; tests set it to judge as another OS would.
var goos = runtime.GOOS

// lookPath finds a program on PATH, as shell_execute does when it picks the
// shell. It is exec.LookPath; tests replace it to judge as a host with or
// without bash.
var lookPath = exec.LookPath

// OptionsFunc supplies the working directory and roots for one call. An error
// denies the call.
type OptionsFunc func(req shuttle.AdmissionRequest) (Options, error)

// Factory returns the shuttle.CommandPolicyFactory serve wires into
// shuttle.ChainDeps: a command-policy binding names one of policies, and its
// decision body analyzes the call's command under it. A binding naming an
// unknown policy is a build error. An empty enforcement means static: only
// an explicit "runtime" leaves to a runner what the analyzer cannot see.
func Factory(policies map[string]*Policy, opts OptionsFunc) shuttle.CommandPolicyFactory {
	return func(b shuttle.HookBinding) (func(shuttle.AdmissionRequest) shuttle.Decision, error) {
		pol, ok := policies[b.Policy]
		if !ok || pol == nil {
			known := make([]string, 0, len(policies))
			for n := range policies {
				known = append(known, n)
			}
			sort.Strings(known)
			return nil, fmt.Errorf("command-policy names unknown policy %q (known: %s)", b.Policy, strings.Join(known, ", "))
		}
		static := b.Enforcement != shuttle.CommandPolicyRuntime
		return func(req shuttle.AdmissionRequest) shuttle.Decision {
			return Decide(pol, req, opts, static)
		}, nil
	}
}

// Decide is the command-policy decision for one call.
func Decide(pol *Policy, req shuttle.AdmissionRequest, opts OptionsFunc, static bool) shuttle.Decision {
	command, _ := req.Params[CommandParam].(string)
	if strings.TrimSpace(command) == "" {
		return shuttle.Decision{Kind: shuttle.Deny, Reason: "command-policy: the call has no command"}
	}
	if why := unsupportedOS(); why != "" {
		return shuttle.Decision{Kind: shuttle.Deny, Reason: "command-policy: " + why}
	}
	if why := unanalyzableShell(req.Params); why != "" {
		return shuttle.Decision{Kind: shuttle.Deny, Reason: "command-policy: " + why}
	}
	opt, err := opts(req)
	if err != nil {
		return shuttle.Decision{Kind: shuttle.Deny, Reason: "command-policy: " + err.Error()}
	}
	opt.Static = static
	v := pol.analyze(command, envNames(req.Params), opt)
	switch v.Kind {
	case Allow:
		return shuttle.Decision{Kind: shuttle.Allow}
	case Ask:
		if tooLongToReview(req.Params) {
			return shuttle.Decision{Kind: shuttle.Deny, Reason: fmt.Sprintf(
				"%s. The call is too long to show in full on an approval card (over %d bytes), so it cannot be approved: split the command into smaller calls",
				v.Reason(), shuttle.ApprovalParamsMaxBytes)}
		}
		d := shuttle.Decision{Kind: shuttle.Ask, Reason: v.Reason()}
		if v.Grant != nil {
			d.Grant = v.Grant
		}
		return d
	default:
		return shuttle.Decision{Kind: shuttle.Deny, Reason: v.Reason()}
	}
}

// unsupportedOS returns why no call can be judged on this OS, or "": the
// analyzer judges paths as POSIX paths through path/filepath, which on Windows
// does not treat "/etc/x" as absolute, so a judgement there would fail open.
// Every call is refused on Windows, whatever its shell parameter and
// enforcement. The binding still builds, so a Windows server starts.
func unsupportedOS() string {
	if goos == "windows" {
		return "the command policy judges POSIX paths and cannot judge commands on Windows"
	}
	return ""
}

// unanalyzableShell returns why the call's command cannot be judged, or "":
// the policy parses commands as bash, so only a call that bash runs is
// judged. shell_execute runs bash for "bash". For an absent, empty or
// "default" shell it runs bash when bash is on PATH, and sh otherwise; for
// "sh" it runs sh. On Debian and Ubuntu sh is dash, which parses some
// commands differently from bash (it has no $'...' quoting, so
// echo $'\' ; touch x ; #' runs touch there), so "sh", and the default shell
// on a host without bash, are refused. Every other shell (powershell, cmd,
// zsh, or "default" on Windows, where it means PowerShell or cmd) is refused,
// and so is a shell value that is not a string, since nothing says which
// shell runs it.
func unanalyzableShell(params map[string]interface{}) string {
	const judged = "only bash commands can be judged"
	s := ""
	if v, ok := params[shellParam]; ok && v != nil {
		str, ok := v.(string)
		if !ok {
			return fmt.Sprintf("the %s parameter is not a string, so the shell that runs the command is unknown; %s", shellParam, judged)
		}
		s = str
	}
	switch s {
	case "bash":
		return ""
	case "", "default":
		if goos == "windows" {
			break
		}
		if _, err := lookPath("bash"); err != nil {
			return "the command runs under the default shell, and bash is not on PATH, so sh runs it; " + judged
		}
		return ""
	}
	return fmt.Sprintf("the command runs under shell %q, which the policy cannot parse; %s", s, judged)
}

// envNames returns the names of the variables the call's env parameter puts
// in the command's environment, sorted. shell_execute uses the entries whose
// value is a string.
func envNames(params map[string]interface{}) []string {
	env, ok := params[envParam].(map[string]interface{})
	if !ok {
		return nil
	}
	names := make([]string, 0, len(env))
	for k, v := range env {
		if _, ok := v.(string); ok {
			names = append(names, k)
		}
	}
	sort.Strings(names)
	return names
}

// tooLongToReview reports whether params would be cut on an approval card.
// Params that cannot be encoded count as too long.
func tooLongToReview(params map[string]interface{}) bool {
	encoded, err := json.Marshal(params)
	return err != nil || len(encoded) > shuttle.ApprovalParamsMaxBytes
}
