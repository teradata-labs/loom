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

package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
)

// getCLIHelp generates comprehensive CLI help documentation
func getCLIHelp() (string, error) {
	var buf bytes.Buffer

	// Try to use built binary first, fallback to go run
	loomsBinary := "./bin/looms"
	var cmdName string
	var cmdArgs []string

	if _, err := os.Stat(loomsBinary); err == nil {
		// Binary exists, use it
		cmdName = loomsBinary
		cmdArgs = []string{}
	} else {
		// Binary doesn't exist, use go run
		cmdName = "go"
		cmdArgs = []string{"run", "-tags", "fts5", "./cmd/looms"}
	}

	// Main help
	args := append([]string{}, cmdArgs...)
	args = append(args, "--help")
	mainHelp, err := runCommand(cmdName, args...)
	if err != nil {
		return "", fmt.Errorf("failed to get main help: %w", err)
	}

	buf.WriteString("    ### Main Commands\n\n")
	buf.WriteString("    ```\n")
	buf.WriteString(indentText(mainHelp, "    "))
	buf.WriteString("\n    ```\n\n")

	// Workflow commands
	args = append([]string{}, cmdArgs...)
	args = append(args, "workflow", "--help")
	workflowHelp, err := runCommand(cmdName, args...)
	if err != nil {
		return "", fmt.Errorf("failed to get workflow help: %w", err)
	}

	buf.WriteString("    ### Workflow Commands\n\n")
	buf.WriteString("    ```\n")
	buf.WriteString(indentText(workflowHelp, "    "))
	buf.WriteString("\n    ```\n\n")

	// Pattern commands
	args = append([]string{}, cmdArgs...)
	args = append(args, "pattern", "--help")
	patternHelp, err := runCommand(cmdName, args...)
	if err != nil {
		return "", fmt.Errorf("failed to get pattern help: %w", err)
	}

	buf.WriteString("    ### Pattern Commands\n\n")
	buf.WriteString("    ```\n")
	buf.WriteString(indentText(patternHelp, "    "))
	buf.WriteString("\n    ```\n\n")

	// Config commands
	args = append([]string{}, cmdArgs...)
	args = append(args, "config", "--help")
	configHelp, err := runCommand(cmdName, args...)
	if err != nil {
		return "", fmt.Errorf("failed to get config help: %w", err)
	}

	buf.WriteString("    ### Config Commands\n\n")
	buf.WriteString("    ```\n")
	buf.WriteString(indentText(configHelp, "    "))
	buf.WriteString("\n    ```\n")

	return buf.String(), nil
}

// runCommand executes a command and returns its output
func runCommand(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		// Some commands might return non-zero but still produce valid help
		// Check if we have stdout output
		if stdout.Len() > 0 {
			return stdout.String(), nil
		}
		if stderr.Len() > 0 {
			return stderr.String(), nil
		}
		return "", fmt.Errorf("command failed: %w\nstderr: %s", err, stderr.String())
	}

	return stdout.String(), nil
}

// indentText indents each line of text
func indentText(text, indent string) string {
	lines := strings.Split(text, "\n")
	var result []string
	for _, line := range lines {
		if line != "" {
			result = append(result, indent+line)
		} else {
			result = append(result, "")
		}
	}
	return strings.Join(result, "\n")
}

func main() {
	// Get project root
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to get working directory: %v\n", err)
		os.Exit(1)
	}

	// Navigate to project root if not already there
	for {
		if _, err := os.Stat(filepath.Join(cwd, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(cwd)
		if parent == cwd {
			fmt.Fprintf(os.Stderr, "Error: could not find project root (no go.mod found)\n")
			os.Exit(1)
		}
		cwd = parent
	}

	if err := os.Chdir(cwd); err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to change to project root: %v\n", err)
		os.Exit(1)
	}

	templatePath := filepath.Join(cwd, "embedded", "weaver.yaml.tmpl")
	outputPath := filepath.Join(cwd, "embedded", "weaver.yaml")

	fmt.Printf("Generating weaver.yaml from template...\n")
	fmt.Printf("Template: %s\n", templatePath)
	fmt.Printf("Output: %s\n", outputPath)

	// Read template
	templatePath = filepath.Clean(templatePath)
	tmplContent, err := os.ReadFile(templatePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to read template: %v\n", err)
		os.Exit(1)
	}

	// Get CLI help
	fmt.Println("Generating CLI help documentation...")
	cliHelp, err := getCLIHelp()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to generate CLI help: %v\n", err)
		os.Exit(1)
	}

	// Parse and execute template
	tmpl, err := template.New("weaver").Parse(string(tmplContent))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to parse template: %v\n", err)
		os.Exit(1)
	}

	var output bytes.Buffer
	data := map[string]string{
		"CLIHelp": cliHelp,
	}

	if err := tmpl.Execute(&output, data); err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to execute template: %v\n", err)
		os.Exit(1)
	}

	// Write output
	outputPath = filepath.Clean(outputPath)
	if err := os.WriteFile(outputPath, output.Bytes(), 0600); err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to write output: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("✅ Generated %s (%d bytes)\n", outputPath, output.Len())
}
