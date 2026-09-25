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

package sites

import (
	"fmt"
	"sort"
	"strconv"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/decision"
)

// SiteSkillRoute is one step of the skill-index tree walk
// (skills/index.Router.Route). Today each step is a whole LLM call that
// returns JSON naming the child subtrees to descend into and the skills to
// select, with a re-ask on unparseable output; the walk makes one such call
// per node visited, sequentially, before the agent's turn can start. Here it
// is one Noul per option in a single request.
const SiteSkillRoute = "skill.route"

// SkillRouteFanOutKey is the state array whose items pair with the option
// questions (DecisionRequest.fan_out_key), so a chunked request carries only
// the options it is being asked about.
const SkillRouteFanOutKey = "options"

// Reference sources for the skill route site.
const (
	// ReferenceSourceRouterLLM is the tree-walk LLM's own answer.
	ReferenceSourceRouterLLM = "skills.index.router.askDecision"
	// ReferenceSourceRouterLeafLLM is the fat-leaf pick's answer.
	ReferenceSourceRouterLeafLLM = "skills.index.router.pickFromFatLeaf"
)

// Option kinds.
const (
	// OptionKindSubtree is a child node the walk may descend into.
	OptionKindSubtree = "subtree"
	// OptionKindSkill is a skill the walk may select.
	OptionKindSkill = "skill"
)

// Bounds on one routing request. The message is the thing being classified,
// so it gets room; option text is a summary or description and does not.
const (
	maxRouteMessageRunes = 1500
	maxRouteOptionRunes  = 400
	maxRouteNodeRunes    = 400
	// MaxRouteOptions bounds one request. decision.Chunked splits anything
	// larger, so this is a sanity bound rather than a hard ceiling.
	MaxRouteOptions = 128
)

// RouteOption is one thing the walk may choose at this node: a child subtree
// to descend into, or a skill to select.
type RouteOption struct {
	// Kind is OptionKindSubtree or OptionKindSkill.
	Kind string
	// ID is the child node id or the skill name. It is what the caller acts
	// on, and it is never sent as the question id.
	ID string
	// Title is the human label (node title, skill title).
	Title string
	// Text is the node summary or the skill description.
	Text string
}

// RouteOptionQuestionID is the question id for option index i. Option ids
// are positional so a skill name can never collide with the question-id
// grammar the decider validates.
func RouteOptionQuestionID(i int) string { return "o" + strconv.Itoa(i) }

// SkillRouteRequest builds one request asking, for every option, whether it
// bears on the user's message. Subtrees and skills are asked the same
// question in the same request because the walk decides both at once; the
// caller separates them by Kind when it reads the answers.
//
// It returns a *decision.ValidationError when there are no options.
func SkillRouteRequest(message, nodeTitle, nodeSummary string, options []RouteOption) (*loomv1.DecisionRequest, error) {
	if len(options) == 0 {
		return nil, &decision.ValidationError{Field: "state.options", Msg: "no options"}
	}
	if len(options) > MaxRouteOptions {
		options = options[:MaxRouteOptions]
	}
	items := make([]any, 0, len(options))
	questions := make(map[string]*loomv1.DecisionQuestion, len(options))
	for i, o := range options {
		id := RouteOptionQuestionID(i)
		item := map[string]any{
			"id":    id,
			"kind":  o.Kind,
			"title": truncateRunes(o.Title, maxRouteOptionRunes),
		}
		if o.Text != "" {
			item["text"] = truncateRunes(o.Text, maxRouteOptionRunes)
		}
		items = append(items, item)

		whenTrue, whenFalse := skillCriteria(o.Kind)
		questions[id] = decision.Noul(
			fmt.Sprintf("Does option %s bear on what the user is asking for?", id),
			decision.WithCriteria(whenTrue, whenFalse),
		)
	}
	state := map[string]any{
		"message": truncateRunes(message, maxRouteMessageRunes),
		"node": map[string]any{
			"title":   truncateRunes(nodeTitle, maxRouteNodeRunes),
			"summary": truncateRunes(nodeSummary, maxRouteNodeRunes),
		},
		SkillRouteFanOutKey: items,
	}
	req, err := decision.NewRequest(SiteSkillRoute, state, questions)
	if err != nil {
		return nil, err
	}
	req.FanOutKey = SkillRouteFanOutKey
	return req, nil
}

// skillCriteria phrases the yes and no sides for each option kind. A subtree
// is worth descending on a weaker signal than a skill is worth selecting:
// descending costs one more step, selecting puts text in the agent's prompt.
func skillCriteria(kind string) (whenTrue, whenFalse string) {
	if kind == OptionKindSubtree {
		return "the subtree could plausibly contain a skill for this request, even if only some of it is relevant",
			"nothing under that subtree addresses this request"
	}
	return "the skill is about the task the user is asking for and its instructions would help carry it out",
		"the skill is about a different task, or only shares incidental vocabulary with the request"
}

// RouteSelection is what the walk should do at this node.
type RouteSelection struct {
	// Descend holds the ids of subtree options to walk into.
	Descend []string
	// Skills holds the names of skill options to select, most relevant
	// first, already capped.
	Skills []string
	// Uncertain counts options whose answer did not clear the band.
	Uncertain int
	// Answered counts options the decider actually answered.
	Answered int
}

// SkillRouteSelected reads a response into a RouteSelection under a band.
//
// The two kinds are treated differently on purpose, and the asymmetry is the
// point:
//
//   - A subtree is descended when its answer says relevant OR when the
//     answer did not clear the band. Pruning a subtree on an uncertain
//     answer loses everything beneath it; descending costs one more step.
//     This mirrors "never drop a memory on an uncertain answer" at the
//     rerank sites.
//   - A skill is selected only on a confident yes. An uncertain skill that
//     is wrong costs prompt budget on every later turn, and the walk has
//     other chances to find the right one.
//
// Skills are ordered by probability, highest first, and capped at maxSkills
// (0 means no cap).
func SkillRouteSelected(resp *loomv1.DecisionResponse, options []RouteOption, band decision.Band, maxSkills int) RouteSelection {
	var sel RouteSelection
	if resp == nil {
		return sel
	}
	type scored struct {
		name string
		p    float64
	}
	var skills []scored
	for i, o := range options {
		if i >= MaxRouteOptions {
			break
		}
		a, err := decision.NoulOf(resp, RouteOptionQuestionID(i))
		if err != nil {
			// No answer for this option: treat it as uncertain, which
			// descends a subtree and skips a skill.
			sel.Uncertain++
			if o.Kind == OptionKindSubtree {
				sel.Descend = append(sel.Descend, o.ID)
			}
			continue
		}
		sel.Answered++
		confident := band.Confident(resp.Answers[RouteOptionQuestionID(i)])
		if !confident {
			sel.Uncertain++
		}
		switch o.Kind {
		case OptionKindSubtree:
			if !confident || band.IsTrue(a.Probability) {
				sel.Descend = append(sel.Descend, o.ID)
			}
		default:
			if confident && band.IsTrue(a.Probability) {
				skills = append(skills, scored{o.ID, a.Probability})
			}
		}
	}
	sort.SliceStable(skills, func(i, j int) bool { return skills[i].p > skills[j].p })
	if maxSkills > 0 && len(skills) > maxSkills {
		skills = skills[:maxSkills]
	}
	for _, s := range skills {
		sel.Skills = append(sel.Skills, s.name)
	}
	return sel
}

// SkillRouteContributed reports whether the decider's answer is worth acting
// on at all. Two ways it is not:
//
//   - Every answer was uncertain. "Descend into everything and select
//     nothing" is not a routing decision.
//   - Nothing was chosen: no subtree to walk and no skill to select. At a
//     branch node that discards the entire subtree beneath it on the
//     strength of one model's confidence, with no second opinion. Most
//     options are irrelevant at any given node, but *all* of them being
//     irrelevant is the answer most worth double-checking, and the LLM is
//     already sitting behind this call.
//
// In both cases the caller runs the existing walk, which is what it did
// before the decision layer existed.
func SkillRouteContributed(sel RouteSelection) bool {
	if sel.Answered == 0 || sel.Uncertain >= sel.Answered {
		return false
	}
	return len(sel.Descend)+len(sel.Skills) > 0
}

// SkillRouteReference renders what the LLM walk chose as one Reference per
// option, for shadow rows. chosen holds the ids the LLM picked, of either
// kind. subjects is index-aligned with options and carries each option's
// identity (see DecisionShadowRecord.subject) so a row can be graded later.
func SkillRouteReference(options []RouteOption, chosen []string, source string, subjects []string) map[string]decision.Reference {
	chosenSet := make(map[string]struct{}, len(chosen))
	for _, c := range chosen {
		chosenSet[c] = struct{}{}
	}
	refs := make(map[string]decision.Reference, len(options))
	for i, o := range options {
		if i >= MaxRouteOptions {
			break
		}
		_, ok := chosenSet[o.ID]
		ref := decision.Reference{Answer: strconv.FormatBool(ok), Source: source}
		if i < len(subjects) {
			ref.Subject = subjects[i]
		}
		refs[RouteOptionQuestionID(i)] = ref
	}
	return refs
}

// SkillRouteSubjects renders each option's identity as a shadow-row subject:
// "subtree:<node id>" or "skill:<name>". Never the message.
func SkillRouteSubjects(options []RouteOption) []string {
	out := make([]string, len(options))
	for i, o := range options {
		out[i] = o.Kind + ":" + o.ID
	}
	return out
}
