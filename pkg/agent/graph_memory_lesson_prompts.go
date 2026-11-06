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
	"context"
	"fmt"
	"strings"
)

// Prompt-registry keys of the lesson lane. The shipped text lives in
// prompts/memory/lessons.yaml; lessonPromptFallbacks holds the same text for
// an agent without a registry, or with a registry that lacks a key.
const (
	lessonPromptMiningPreamble = "memory.lesson_mining_preamble"
	lessonPromptMiningTaskHeading = "memory.lesson_mining_task_heading"
	lessonPromptMiningInstructions = "memory.lesson_mining_instructions"
	lessonPromptHeadingStart = "memory.lesson_heading_conversation_start"
	lessonPromptHeadingErrorVerified = "memory.lesson_heading_error_verified"
	lessonPromptHeadingErrorRetrial = "memory.lesson_heading_error_retrial"
	lessonPromptErrorVerifiedIntro = "memory.lesson_error_verified_intro"
	lessonPromptErrorRetrialIntro = "memory.lesson_error_retrial_intro"
)

// lessonPromptFallbacks is the built-in text of every lesson-lane prompt,
// identical to prompts/memory/lessons.yaml (pinned by
// TestLessonPromptsShippedYAMLMatchesFallback). Entries carry no leading or
// trailing newline, because the registry trims its entries; callers add the
// line breaks around them.
var lessonPromptFallbacks = map[string]string{
	lessonPromptMiningPreamble: "Each numbered item below is a CLAIMED fix from a tool-execution ledger: a tool call that failed, the error it produced, and the later call of the same kind that stopped erroring. Audit these claims: an error going away proves nothing about the work being right. Name what each change did — and what it traded away.",
	lessonPromptMiningTaskHeading: "THE CONVERSATION'S TASK (context for judging whether a fix served it — lessons must still generalize beyond this task):",
	lessonPromptMiningInstructions: `For each item, extract ONE memory stating the lesson in reusable, situation-independent form — one or two sentences, the cause, the change, and its trade-off; not the story. Compare the failed and succeeded inputs: the difference between them is the change. When the failed and succeeded inputs are identical or nearly identical, the change happened in the CALLS BETWEEN FAILURE AND SUCCESS — name THAT change as the lesson.

State the trade-off and its applicability condition whenever the change has one. A change that silences an error by discarding or nulling data is a trade, not a free fix. Example of a clean fix: "inserting 16-digit identifiers into an INTEGER column overflows — declare such columns BIGINT". Example of a trade-off lesson: "wrapping CC_Number in a defensive CAST silences numeric overflow but NULLs unconvertible values — safe only when the column is not used as a key or in counts downstream".

Judge each claim against the evidence:
- LATER USE OF THE SAME OBJECT shows what the "fixed" object actually held afterward. Zeros, NULLs, or empty rows where the task expected data mean the change corrupted the data — record the lesson as a WARNING against that change, stating what it broke, not as a fix.
- SUCCEEDED RESULT: a success that did no work is NOT a fix. An empty result set, zero rows inserted/affected (e.g. "activity_count":0 on an INSERT), or all-NULL values means the change silenced the error by matching nothing. Skip such items entirely.
- Skip an item if the succeeded input does not actually address the error (e.g. it succeeded by abandoning the approach).

The OBSERVED CHANGED FRAGMENTS line lists what the recovery ACTUALLY changed, computed mechanically from the ledger. The lesson MUST quote these fragments verbatim. When several fragments changed, name ALL of them in the lesson — never pick one and present it as the sole cause; the ledger cannot tell which mattered, and neither can you.

Return ONLY a JSON object:
{"entities": [], "relationships": [], "memories": [{"content": "cause, change, and trade-off", "summary": "short summary", "memory_type": "lesson", "tags": ["lesson"], "salience": 0.9, "entities": [], "event_date": "", "event_date_confidence": ""}]}`,
	lessonPromptHeadingStart: "Verified lessons from prior work (each fix was observed to succeed):",
	lessonPromptHeadingErrorVerified: "[Verified Lessons — matched to the error just hit]",
	lessonPromptHeadingErrorRetrial: "[Lesson Re-trial — matched to the error just hit]",
	lessonPromptErrorVerifiedIntro: "These fixes were observed to succeed on this error in prior work:",
	lessonPromptErrorRetrialIntro: "Under re-trial (this one has preceded failures before — use only if it clearly fits):",
}

// lessonPrompt returns the registry text for a lesson-lane key, or the
// built-in fallback when there is no registry, the key is missing or empty,
// or the entry still holds an unrendered placeholder — the same rule as
// getGuidanceMessage. Prompt authors can tune the wording without a rebuild.
func (a *Agent) lessonPrompt(ctx context.Context, key string) string {
	if a != nil && a.prompts != nil {
		if text, err := a.prompts.Get(ctx, key, nil); err == nil {
			if text = strings.TrimSpace(text); text != "" && !strings.Contains(text, "{{") {
				return text
			}
		}
	}
	return lessonPromptFallbacks[key]
}

// lessonMiningText is the fixed text of the lesson-mining prompt; the
// ledger evidence between its parts is rendered by renderLessonMiningPrompt.
type lessonMiningText struct {
	preamble     string
	taskHeading  string
	instructions string
}

// lessonMiningPrompt builds the mining prompt with text from the prompt
// registry (fallback: the built-in text).
func (a *Agent) lessonMiningPrompt(ctx context.Context, pairs []lessonPair, task string) string {
	return renderLessonMiningPrompt(lessonMiningText{
		preamble:     a.lessonPrompt(ctx, lessonPromptMiningPreamble),
		taskHeading:  a.lessonPrompt(ctx, lessonPromptMiningTaskHeading),
		instructions: a.lessonPrompt(ctx, lessonPromptMiningInstructions),
	}, pairs, task)
}

// buildLessonMiningPrompt builds the mining prompt from the built-in text.
func buildLessonMiningPrompt(pairs []lessonPair, task string) string {
	return renderLessonMiningPrompt(lessonMiningText{
		preamble:     lessonPromptFallbacks[lessonPromptMiningPreamble],
		taskHeading:  lessonPromptFallbacks[lessonPromptMiningTaskHeading],
		instructions: lessonPromptFallbacks[lessonPromptMiningInstructions],
	}, pairs, task)
}

// renderLessonMiningPrompt asks for one reusable cause→fix memory per
// verified transition. The evidence is the ledger excerpt itself, so the
// model is describing an observed fix, never endorsing a theory. The
// evidence lines are structure, not wording, and stay in code.
func renderLessonMiningPrompt(text lessonMiningText, pairs []lessonPair, task string) string {
	var sb strings.Builder
	sb.WriteString(text.preamble)
	sb.WriteString("\n\n")
	if task != "" {
		sb.WriteString(text.taskHeading)
		sb.WriteString("\n")
		sb.WriteString(task)
		sb.WriteString("\n\n")
	}
	for i, p := range pairs {
		fmt.Fprintf(&sb, "%d. tool: %s\n   FAILED INPUT: %s\n   ERROR: %s\n", i+1, p.Tool, p.FailingIn, p.ErrorText)
		for _, b := range p.Intervening {
			fmt.Fprintf(&sb, "   CALL BETWEEN FAILURE AND SUCCESS: %s\n", b)
		}
		fmt.Fprintf(&sb, "   SUCCEEDED INPUT: %s\n", p.SucceedsIn)
		if p.SucceedsResult != "" {
			fmt.Fprintf(&sb, "   SUCCEEDED RESULT: %s\n", p.SucceedsResult)
		}
		for _, d := range p.Downstream {
			fmt.Fprintf(&sb, "   LATER USE OF THE SAME OBJECT: %s\n", d)
		}
		if len(p.ChangedFragments) > 0 {
			fmt.Fprintf(&sb, "   OBSERVED CHANGED FRAGMENTS: %s\n", strings.Join(p.ChangedFragments, ", "))
		}
		sb.WriteString("\n")
	}
	sb.WriteString(text.instructions)
	sb.WriteString("\n")
	return sb.String()
}
