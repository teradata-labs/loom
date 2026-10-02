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

package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/decision"
	decisionmock "github.com/teradata-labs/loom/pkg/decision/mock"
	"github.com/teradata-labs/loom/pkg/skills"
	skillindex "github.com/teradata-labs/loom/pkg/skills/index"
)

// Review #410 minor: skill.route was never wired outside tests. The skill
// router BuildSkillsOptions builds for an agent now carries that agent's
// decision router (and nothing for an agent without a decision layer). The
// BuildSkillsOptions signature is unchanged; the wiring is one more Option.
func TestBuildSkillsOptionsWiresSkillRouteDecision(t *testing.T) {
	t.Parallel()
	build := func(name string) ([]Option, *skillindex.Router) {
		var wired *skillindex.Router
		opts := BuildSkillsOptions(SkillsWiringDeps{
			SkillsConfig: &skills.SkillsConfig{Enabled: true, SkillsDir: t.TempDir()},
			PrimaryLLM:   rerankReplyLLM{reply: "{}"},
			AgentName:    name,
			// No index build in this test: the wiring is what is checked.
			WarmIndexAsync: func(*skills.Library, *skillindex.Builder, skillindex.Store, *skillindex.Router) {},
			OnWired:        func(w WiredSkillSubsystem) { wired = w.Router },
		})
		require.NotNil(t, wired, "router subsystem wired")
		return opts, wired
	}

	optsA, routerA := build("with-layer")
	dr := decision.NewRouter(decisionmock.New())
	agA := NewAgent(nil, rerankReplyLLM{reply: "1"}, append(optsA, WithName("with-layer"), WithDecisionRouter(dr))...)
	assert.Same(t, agA.DecisionRouter(), routerA.DecisionRouter(), "the skill router carries its own agent's decision router")

	optsB, routerB := build("no-layer")
	_ = NewAgent(nil, rerankReplyLLM{reply: "1"}, append(optsB, WithName("no-layer"))...)
	assert.Nil(t, routerB.DecisionRouter(), "an agent without a decision layer attaches none")
	assert.Same(t, dr, routerA.DecisionRouter(), "building B did not touch A's skill router")
}
