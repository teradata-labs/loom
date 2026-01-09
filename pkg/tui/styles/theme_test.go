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
package styles

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestDefaultTheme(t *testing.T) {
	theme := DefaultTheme

	// Check main colors are set
	if theme.Primary == nil {
		t.Error("Primary color should be set")
	}
	if theme.Secondary == nil {
		t.Error("Secondary color should be set")
	}

	// Check status colors
	if theme.Success == nil {
		t.Error("Success color should be set")
	}
	if theme.Error == nil {
		t.Error("Error color should be set")
	}

	// Check role colors
	if theme.UserColor == nil {
		t.Error("UserColor should be set")
	}
	if theme.AssistantColor == nil {
		t.Error("AssistantColor should be set")
	}
}

func TestNewStyles(t *testing.T) {
	theme := DefaultTheme
	styles := NewStyles(theme)

	if styles == nil {
		t.Fatal("NewStyles should not return nil")
	}

	// Check theme is set
	if styles.Theme.Primary != theme.Primary {
		t.Error("Theme should be copied to Styles")
	}

	// Check styles are initialized by rendering test strings
	if styles.Header.Render("test") == "" {
		t.Error("Header style should be initialized")
	}
	if styles.Footer.Render("test") == "" {
		t.Error("Footer style should be initialized")
	}
	if styles.MessageUser.Render("test") == "" {
		t.Error("MessageUser style should be initialized")
	}
	if styles.MessageAssistant.Render("test") == "" {
		t.Error("MessageAssistant style should be initialized")
	}
	if styles.TextDim.Render("test") == "" {
		t.Error("TextDim style should be initialized")
	}
}

func TestDefaultStyles(t *testing.T) {
	styles := DefaultStyles()

	if styles == nil {
		t.Fatal("DefaultStyles should not return nil")
	}

	// Should use DefaultTheme
	if styles.Theme.Primary != DefaultTheme.Primary {
		t.Error("DefaultStyles should use DefaultTheme")
	}
}

func TestCustomTheme(t *testing.T) {
	customTheme := Theme{
		Primary:        lipgloss.Color("#FF0000"),
		Secondary:      lipgloss.Color("#00FF00"),
		UserColor:      lipgloss.Color("#0000FF"),
		AssistantColor: lipgloss.Color("#FFFF00"),
		TextDim:        lipgloss.Color("#808080"),
	}

	styles := NewStyles(customTheme)

	if styles.Theme.Primary != customTheme.Primary {
		t.Error("Custom theme should be used")
	}
	if styles.Theme.UserColor != customTheme.UserColor {
		t.Error("Custom UserColor should be used")
	}
}

func TestFormatSessionID(t *testing.T) {
	styles := DefaultStyles()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "short ID",
			input:    "sess_123",
			expected: "sess_123",
		},
		{
			name:     "long ID",
			input:    "sess_123456789012345",
			expected: "sess_1234567...",
		},
		{
			name:     "exactly 12 chars",
			input:    "sess_1234567",
			expected: "sess_1234567",
		},
		{
			name:     "13 chars",
			input:    "sess_12345678",
			expected: "sess_1234567...",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := styles.FormatSessionID(tt.input)
			if result != tt.expected {
				t.Errorf("Expected %q, got %q", tt.expected, result)
			}
		})
	}
}

func TestFormatCost(t *testing.T) {
	styles := DefaultStyles()

	tests := []struct {
		name     string
		input    float64
		contains string
	}{
		{
			name:     "zero cost",
			input:    0.0,
			contains: "Free",
		},
		{
			name:     "very small cost",
			input:    0.0001,
			contains: "0.0001",
		},
		{
			name:     "normal cost",
			input:    0.0234,
			contains: "0.0234",
		},
		{
			name:     "large cost",
			input:    1.5,
			contains: "1.5",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := styles.FormatCost(tt.input)
			if !strings.Contains(result, tt.contains) {
				t.Errorf("Expected result to contain %q, got %q", tt.contains, result)
			}
		})
	}
}

func TestFormatError(t *testing.T) {
	styles := DefaultStyles()

	err := &testError{"test error"}
	result := styles.FormatError(err)

	if !strings.Contains(result, "test error") {
		t.Errorf("Expected result to contain error message, got %q", result)
	}
	if !strings.Contains(result, "✗") {
		t.Errorf("Expected result to contain error symbol, got %q", result)
	}
}

func TestFormatSuccess(t *testing.T) {
	styles := DefaultStyles()

	result := styles.FormatSuccess("operation complete")

	if !strings.Contains(result, "operation complete") {
		t.Errorf("Expected result to contain message, got %q", result)
	}
	if !strings.Contains(result, "✓") {
		t.Errorf("Expected result to contain success symbol, got %q", result)
	}
}

func TestStyleRendering(t *testing.T) {
	styles := DefaultStyles()

	// Test that styles can render text without panicking
	tests := []struct {
		name  string
		style lipgloss.Style
		text  string
	}{
		{"Header", styles.Header, "Test Header"},
		{"Footer", styles.Footer, "Test Footer"},
		{"MessageUser", styles.MessageUser, "User message"},
		{"MessageAssistant", styles.MessageAssistant, "Assistant message"},
		{"MessageTool", styles.MessageTool, "Tool message"},
		{"MessageSystem", styles.MessageSystem, "System message"},
		{"TextDim", styles.TextDim, "Dim text"},
		{"ErrorInfo", styles.ErrorInfo, "Error info"},
		{"CostInfo", styles.CostInfo, "$0.01"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Should not panic
			result := tt.style.Render(tt.text)
			if result == "" {
				t.Error("Render should not return empty string")
			}
			// Rendered text should contain original text
			// (may have ANSI codes wrapped around it)
		})
	}
}

func TestWidth(t *testing.T) {
	styles := DefaultStyles()

	text := "Hello, World!"
	style := styles.Header

	width := Width(style, text)
	if width <= 0 {
		t.Errorf("Width should be positive, got %d", width)
	}
}

// testError is a simple error type for testing.
type testError struct {
	msg string
}

func (e *testError) Error() string {
	return e.msg
}

// TestRaceConditions ensures thread-safety.
func TestRaceConditions(t *testing.T) {
	styles := DefaultStyles()

	done := make(chan bool, 100)
	for i := 0; i < 100; i++ {
		go func() {
			// Concurrent formatting operations
			_ = styles.FormatSessionID("sess_123456789012345")
			_ = styles.FormatCost(0.0123)
			_ = styles.FormatError(&testError{"test"})
			_ = styles.FormatSuccess("done")
			_ = styles.Header.Render("test")
			_ = styles.MessageUser.Render("user")
			_ = styles.TextDim.Render("dim")
			done <- true
		}()
	}

	// Wait for all to complete
	for i := 0; i < 100; i++ {
		<-done
	}
}
