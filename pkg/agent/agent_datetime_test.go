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
	"strings"
	"testing"
	"time"
)

func TestFormatSystemPromptWithDatetime(t *testing.T) {
	now := time.Now()
	prompt := "You are a helpful agent."

	result := formatSystemPromptWithDatetime(prompt, nil)

	// Verify structure
	if !strings.Contains(result, "CURRENT DATE AND TIME") {
		t.Error("Expected datetime header not found")
	}

	if !strings.Contains(result, "Date:") {
		t.Error("Expected 'Date:' field not found")
	}

	if !strings.Contains(result, "Time:") {
		t.Error("Expected 'Time:' field not found")
	}

	if !strings.Contains(result, "Timezone:") {
		t.Error("Expected 'Timezone:' field not found")
	}

	// Verify year is included (common hallucination issue)
	year := now.Format("2006")
	if !strings.Contains(result, year) {
		t.Errorf("Expected year %s not found in datetime header", year)
	}

	// Verify original prompt is preserved
	if !strings.Contains(result, prompt) {
		t.Error("Original prompt not found in result")
	}

	// Verify datetime comes before prompt
	promptIndex := strings.Index(result, prompt)
	headerIndex := strings.Index(result, "CURRENT DATE AND TIME")
	if headerIndex >= promptIndex {
		t.Error("Datetime header should come before prompt")
	}

	// Verify UTC offset is included
	if !strings.Contains(result, "UTC") {
		t.Error("Expected UTC offset not found")
	}
}

func TestGetSystemPromptIncludesDatetime(t *testing.T) {
	// Create a minimal agent with a simple config
	config := &Config{
		Name:         "test-agent",
		SystemPrompt: "Test system prompt",
	}

	agent := &Agent{
		config: config,
	}

	systemPrompt := agent.getSystemPrompt()

	// Verify datetime information is included
	if !strings.Contains(systemPrompt, "CURRENT DATE AND TIME") {
		t.Error("System prompt missing datetime header")
	}

	if !strings.Contains(systemPrompt, "Date:") {
		t.Error("System prompt missing date field")
	}

	// Verify original prompt is included
	if !strings.Contains(systemPrompt, "Test system prompt") {
		t.Error("System prompt missing original content")
	}

	// Verify current year is included (helps prevent LLM confusion)
	year := time.Now().Format("2006")
	if !strings.Contains(systemPrompt, year) {
		t.Errorf("System prompt missing current year %s", year)
	}
}

func TestDatetimeFormatReadability(t *testing.T) {
	prompt := "Agent instructions"
	result := formatSystemPromptWithDatetime(prompt, nil)

	// Check for day of week (helps LLMs understand temporal context)
	daysOfWeek := []string{"Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday"}
	foundDay := false
	for _, day := range daysOfWeek {
		if strings.Contains(result, day) {
			foundDay = true
			break
		}
	}
	if !foundDay {
		t.Error("Expected day of week not found in datetime format")
	}

	// Check for month name (more readable than numbers)
	months := []string{"January", "February", "March", "April", "May", "June",
		"July", "August", "September", "October", "November", "December"}
	foundMonth := false
	for _, month := range months {
		if strings.Contains(result, month) {
			foundMonth = true
			break
		}
	}
	if !foundMonth {
		t.Error("Expected month name not found in datetime format")
	}
}
