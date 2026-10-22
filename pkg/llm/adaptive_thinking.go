// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package llm

import "strings"

// adaptiveThinkingMarkers name the model families that take adaptive thinking
// — type:"adaptive" plus output_config.effort — rather than the older
// enabled+budget_tokens form. Matching is on a substring of the lowered model
// id so it holds for bare names, dated suffixes and Bedrock profile ids alike.
//
// This list lived in four copies across two clients, each deciding the same
// question for a different request field. A model added to one copy and not
// the others would take adaptive thinking while being told an effort tier it
// cannot use, or the reverse. One list, one answer.
var adaptiveThinkingMarkers = []string{
	"sonnet-5", "opus-5", "haiku-5", "fable-5", "-4-6", "-4-7", "-4-8",
}

// IsAdaptiveThinkingModel reports whether model takes adaptive thinking.
func IsAdaptiveThinkingModel(model string) bool {
	m := strings.ToLower(model)
	for _, marker := range adaptiveThinkingMarkers {
		if strings.Contains(m, marker) {
			return true
		}
	}
	return false
}

// EffortTiers are the output_config.effort values the API accepts. "auto" and
// "" are not tiers: they omit the field and take the API default.
var EffortTiers = []string{"low", "medium", "high", "xhigh", "max"}

// EffortForLevel maps a thinking level to an effort tier for a model that
// accepts output_config.effort, or "" to omit the field — because the model is
// not adaptive, or the level is auto/empty, or the level is not a tier the API
// recognises. An unrecognised level omits the field rather than passing it
// through, so a typo cannot become a 400.
func EffortForLevel(model, level string) string {
	if !IsAdaptiveThinkingModel(model) {
		return ""
	}
	l := strings.ToLower(level)
	for _, tier := range EffortTiers {
		if l == tier {
			return l
		}
	}
	return ""
}
