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
// Package highlight provides syntax highlighting stubs.
package highlight

import "strings"

// Highlight returns syntax-highlighted code.
// Stub implementation - returns code as-is.
func Highlight(code, language string) string {
	return code
}

// HighlightWithTheme returns syntax-highlighted code with a theme.
// Stub implementation - returns code as-is.
func HighlightWithTheme(code, language, theme string) string {
	return code
}

// DetectLanguage attempts to detect the language from filename or content.
func DetectLanguage(filename, content string) string {
	ext := strings.ToLower(filename)
	switch {
	case strings.HasSuffix(ext, ".go"):
		return "go"
	case strings.HasSuffix(ext, ".py"):
		return "python"
	case strings.HasSuffix(ext, ".js"):
		return "javascript"
	case strings.HasSuffix(ext, ".ts"):
		return "typescript"
	case strings.HasSuffix(ext, ".sql"):
		return "sql"
	case strings.HasSuffix(ext, ".yaml"), strings.HasSuffix(ext, ".yml"):
		return "yaml"
	case strings.HasSuffix(ext, ".json"):
		return "json"
	default:
		return "text"
	}
}

// SyntaxHighlight applies syntax highlighting to code.
// This is a stub that returns code as-is for now.
func SyntaxHighlight(code, filename string, bg interface{}) (string, error) {
	return code, nil
}
