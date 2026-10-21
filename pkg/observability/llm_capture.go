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
package observability

import (
	"strings"
	"sync/atomic"
)

var captureLLMMessages atomic.Bool //nolint:gochecknoglobals // process-wide setting, written once at startup

// SetCaptureLLMMessages turns on or off recording each LLM call's request
// (messages, tool schemas, invocation parameters) and response messages on
// its span as OpenInference attributes. Off by default; servers call it once
// at startup from observability.otlp_capture_llm_messages.
func SetCaptureLLMMessages(on bool) {
	captureLLMMessages.Store(on)
}

// CaptureLLMMessages reports whether LLM calls record their messages.
func CaptureLLMMessages() bool {
	return captureLLMMessages.Load()
}

// IsLLMMessageCaptureKey reports whether key is one of the OpenInference
// attributes LLM calls record only while CaptureLLMMessages is on:
// llm.input_messages.*, llm.output_messages.*, llm.tools.<index>.* and
// llm.invocation_parameters. Loom's own llm.tools.count and llm.tools.names
// are not.
func IsLLMMessageCaptureKey(key string) bool {
	return strings.HasPrefix(key, "llm.input_messages.") ||
		strings.HasPrefix(key, "llm.output_messages.") ||
		key == "llm.invocation_parameters" ||
		isOpenInferenceToolKey(key)
}

// isOpenInferenceToolKey reports whether key is an OpenInference
// llm.tools.<index>.* attribute, as opposed to Loom's llm.tools.count and
// llm.tools.names, which keep their loom. prefix on export.
func isOpenInferenceToolKey(key string) bool {
	rest, ok := strings.CutPrefix(key, "llm.tools.")
	return ok && rest != "" && rest[0] >= '0' && rest[0] <= '9'
}

// withoutLLMMessageCapture returns attrs without the IsLLMMessageCaptureKey
// attributes, or attrs itself when it has none.
func withoutLLMMessageCapture(attrs map[string]interface{}) map[string]interface{} {
	found := false
	for k := range attrs {
		if IsLLMMessageCaptureKey(k) {
			found = true
			break
		}
	}
	if !found {
		return attrs
	}
	out := make(map[string]interface{}, len(attrs))
	for k, v := range attrs {
		if !IsLLMMessageCaptureKey(k) {
			out[k] = v
		}
	}
	return out
}
