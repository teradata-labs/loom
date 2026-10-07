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
	"sort"
	"strings"

	"github.com/teradata-labs/loom/pkg/shuttle"
)

// CommandParam is the parameter holding the shell command, for every tool the
// command-policy hook governs (shell_execute, shell_execute_sandbox).
const CommandParam = "command"

// OptionsFunc supplies the working directory and roots for one call. An error
// denies the call.
type OptionsFunc func(req shuttle.AdmissionRequest) (Options, error)

// Factory returns the shuttle.CommandPolicyFactory serve wires into
// shuttle.ChainDeps: a command-policy binding names one of policies, and its
// decision body analyzes the call's command under it. A binding naming an
// unknown policy is a build error.
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
		static := b.Enforcement == shuttle.CommandPolicyStatic
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
	opt, err := opts(req)
	if err != nil {
		return shuttle.Decision{Kind: shuttle.Deny, Reason: "command-policy: " + err.Error()}
	}
	opt.Static = static
	v := pol.Analyze(command, opt)
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

// tooLongToReview reports whether params would be cut on an approval card.
// Params that cannot be encoded count as too long.
func tooLongToReview(params map[string]interface{}) bool {
	encoded, err := json.Marshal(params)
	return err != nil || len(encoded) > shuttle.ApprovalParamsMaxBytes
}
