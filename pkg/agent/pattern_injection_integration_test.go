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

// TestPatternInjection_MetaAgent, TestPatternInjection_DataAgent, and
// TestPatternInjection_DisabledForMetaAgent are retired by D-2: they asserted that
// selected pattern content is injected into the LLM-visible context as a system message
// (via SegmentedMemory.InjectPattern). That injection channel is deleted entirely by this
// story's deletion manifest (Seam 2) — GetMessagesForLLM now returns exactly ROM(+residue)
// +L1, never pattern content, regardless of pattern selection/confidence/config. There is
// no successor "pattern injected into context" contract to rewrite these against, so this
// coverage is retired, not rewritten. Pattern selection itself (intent classification,
// recommendation) is unaffected and untested here; nil-backend safety around it stays
// covered by pattern_backend_check_test.go.
