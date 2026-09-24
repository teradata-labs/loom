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
	"strconv"
	"strings"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/decision"
)

// SiteStageValidation is the pipeline stage output check
// (orchestration.PipelineExecutor.validateStageOutput). Today an LLM reads a
// validation prompt with the output substituted in and the executor scrapes
// its prose for "valid", "yes" or "true"; here it is one Noul.
const SiteStageValidation = "stage.validation"

// QOutputValid is the single question id at SiteStageValidation.
const QOutputValid = "valid"

// ReferenceSourceValidationLLM labels a reference taken from the scraped LLM
// verdict.
const ReferenceSourceValidationLLM = "orchestration.pipeline.validateStageOutput"

// OutputPlaceholder is the token a validation prompt uses for the output.
const OutputPlaceholder = "{{output}}"

// outputStandIn replaces OutputPlaceholder in the requirement carried in
// state, so the requirement reads as a rule about "the output" below it.
const outputStandIn = "the output"

// maxValidationRequirementRunes bounds the requirement text in state.
const maxValidationRequirementRunes = 4000

// maxValidationOutputRunes bounds the output in state. A stage output is
// the material judged, so it gets the most room of any site; beyond this the
// tail is cut and the decider sees a truncation mark.
const maxValidationOutputRunes = 12000

// ValidationRequest builds the request: state is the requirement (the
// stage's validation prompt with the output placeholder replaced by a stand-in)
// and the output, kept as separate fields so the decider never has to find
// where one ends and the other begins. It returns a *decision.ValidationError
// when the requirement is empty.
func ValidationRequest(requirement, output string) (*loomv1.DecisionRequest, error) {
	requirement = strings.TrimSpace(strings.ReplaceAll(requirement, OutputPlaceholder, outputStandIn))
	if requirement == "" {
		return nil, &decision.ValidationError{Field: "state.requirement", Msg: "empty validation requirement"}
	}
	q := decision.Noul(
		"Does the output satisfy the requirement?",
		decision.WithCriteria(
			"the output meets every part of the requirement as written; formatting differences that the requirement does not mention do not matter",
			"the output misses, contradicts, or only partially meets the requirement, or is empty, an error message, or a refusal",
		),
	)
	state := map[string]any{
		"requirement": truncateRunes(requirement, maxValidationRequirementRunes),
		"output":      truncateRunes(output, maxValidationOutputRunes),
	}
	return decision.NewRequest(SiteStageValidation, state, map[string]*loomv1.DecisionQuestion{QOutputValid: q})
}

// ValidationReference renders the existing mechanism's verdict.
func ValidationReference(valid bool) map[string]decision.Reference {
	return map[string]decision.Reference{
		QOutputValid: {Answer: strconv.FormatBool(valid), Source: ReferenceSourceValidationLLM},
	}
}

// ValidationVerdict returns the decider's verdict: valid when the Noul
// probability is at least 0.5. ok is false when the response carries no
// usable answer.
func ValidationVerdict(resp *loomv1.DecisionResponse, band decision.Band) (valid bool, ok bool) {
	if resp == nil {
		return false, false
	}
	a, err := decision.NoulOf(resp, QOutputValid)
	if err != nil || a == nil {
		return false, false
	}
	return band.IsTrue(a.Probability), true
}
