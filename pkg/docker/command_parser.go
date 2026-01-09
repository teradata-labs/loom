// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package docker

import (
	"fmt"
	"strings"
	"unicode"
)

// parseCommand parses a command string into []string, handling shell-like quoting.
//
// Supports:
//   - Single quotes: 'no expansion'
//   - Double quotes: "allows spaces"
//   - Escaping: \"quote\" or \'quote\'
//   - Whitespace splitting
//
// Examples:
//   - "python -c 'print(\"hello\")'" -> ["python", "-c", "print(\"hello\")"]
//   - "echo \"hello world\"" -> ["echo", "hello world"]
//   - "python script.py" -> ["python", "script.py"]
func parseCommand(query string) ([]string, error) {
	var args []string
	var current strings.Builder
	var inSingleQuote, inDoubleQuote bool
	var escaped bool

	for i, ch := range query {
		// Handle escape sequences
		if escaped {
			current.WriteRune(ch)
			escaped = false
			continue
		}

		if ch == '\\' {
			escaped = true
			continue
		}

		// Handle quotes
		if ch == '\'' && !inDoubleQuote {
			inSingleQuote = !inSingleQuote
			continue
		}

		if ch == '"' && !inSingleQuote {
			inDoubleQuote = !inDoubleQuote
			continue
		}

		// Handle whitespace (split tokens)
		if unicode.IsSpace(ch) && !inSingleQuote && !inDoubleQuote {
			if current.Len() > 0 {
				args = append(args, current.String())
				current.Reset()
			}
			continue
		}

		// Regular character
		current.WriteRune(ch)

		// Check for unclosed quotes at end
		if i == len(query)-1 {
			if inSingleQuote {
				return nil, fmt.Errorf("unclosed single quote in command: %s", query)
			}
			if inDoubleQuote {
				return nil, fmt.Errorf("unclosed double quote in command: %s", query)
			}
		}
	}

	// Add final token
	if current.Len() > 0 {
		args = append(args, current.String())
	}

	if len(args) == 0 {
		return nil, fmt.Errorf("empty command")
	}

	return args, nil
}
