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
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	loomconfig "github.com/teradata-labs/loom/pkg/config"
	"github.com/zalando/go-keyring"
	"golang.org/x/term"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage Loom Server configuration",
	Long:  `Manage configuration files and secrets for Loom Server.`,
}

var configInitCmd = &cobra.Command{
	Use:   "init",
	Short: "Generate example configuration file",
	Long:  `Generate an example looms.yaml configuration file in ~/.loom/`,
	Run:   runConfigInit,
}

var configSetKeyCmd = &cobra.Command{
	Use:   "set-key [key-name]",
	Short: "Save API key to system keyring",
	Long: `Save an API key to the system keyring securely.

The key will be stored in your system's secure credential storage
(Keychain on macOS, Credential Manager on Windows, Secret Service on Linux).

Run 'looms config list-keys' to see available key names.`,
	Args: cobra.ExactArgs(1),
	Run:  runConfigSetKey,
}

var configGetKeyCmd = &cobra.Command{
	Use:   "get-key [key-name]",
	Short: "Retrieve API key from system keyring",
	Long:  `Retrieve an API key from the system keyring (for verification).`,
	Args:  cobra.ExactArgs(1),
	Run:   runConfigGetKey,
}

var configDeleteKeyCmd = &cobra.Command{
	Use:   "delete-key [key-name]",
	Short: "Delete API key from system keyring",
	Long:  `Remove an API key from the system keyring.`,
	Args:  cobra.ExactArgs(1),
	Run:   runConfigDeleteKey,
}

var configShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Show current configuration",
	Long:  `Display the current configuration (merged from all sources).`,
	Run:   runConfigShow,
}

var configListKeysCmd = &cobra.Command{
	Use:   "list-keys",
	Short: "List available secret keys",
	Long:  `List all available secret keys that can be stored in the keyring.`,
	Run:   runConfigListKeys,
}

var configSetCmd = &cobra.Command{
	Use:   "set [key] [value]",
	Short: "Set a configuration value",
	Long: `Set a non-sensitive configuration value in ~/.loom/looms.yaml.

For sensitive values (API keys, secrets), use 'looms config set-key' instead.

Examples:
  looms config set llm.provider bedrock
  looms config set llm.bedrock_region us-west-2
  looms config set llm.bedrock_profile default
  looms config set llm.bedrock_model_id anthropic.claude-sonnet-4-5-20250929-v1:0
  looms config set server.port 60051
  looms config set logging.level debug`,
	Args: cobra.ExactArgs(2),
	Run:  runConfigSet,
}

var configGetCmd = &cobra.Command{
	Use:   "get [key]",
	Short: "Get a configuration value",
	Long: `Get a configuration value from ~/.loom/looms.yaml.

Examples:
  looms config get llm.provider
  looms config get llm.bedrock_region
  looms config get server.port`,
	Args: cobra.ExactArgs(1),
	Run:  runConfigGet,
}

func init() {
	rootCmd.AddCommand(configCmd)
	configCmd.AddCommand(configInitCmd)
	configCmd.AddCommand(configSetCmd)
	configCmd.AddCommand(configGetCmd)
	configCmd.AddCommand(configSetKeyCmd)
	configCmd.AddCommand(configGetKeyCmd)
	configCmd.AddCommand(configDeleteKeyCmd)
	configCmd.AddCommand(configShowCmd)
	configCmd.AddCommand(configListKeysCmd)
}

func runConfigInit(cmd *cobra.Command, args []string) {
	configDir := loomconfig.GetLoomDataDir()
	configPath := filepath.Join(configDir, "looms.yaml")

	// Create directory if it doesn't exist
	if err := os.MkdirAll(configDir, 0750); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating config directory: %v\n", err)
		os.Exit(1)
	}

	// Check if file already exists
	if _, err := os.Stat(configPath); err == nil {
		fmt.Printf("Config file already exists: %s\n", configPath)
		fmt.Print("Overwrite? (y/N): ")
		var response string
		_, _ = fmt.Scanln(&response)
		if response != "y" && response != "Y" {
			fmt.Println("Aborted.")
			return
		}
	}

	// Interactive configuration
	fmt.Println("Loom Configuration Setup")
	fmt.Println("========================")
	fmt.Println()

	// Ask for LLM provider
	fmt.Println("Choose your LLM provider:")
	fmt.Println("  1. Anthropic Claude (API key required)")
	fmt.Println("  2. AWS Bedrock (AWS credentials required)")
	fmt.Println("  3. Ollama (local inference, free)")
	fmt.Print("Selection (1-3) [1]: ")
	var providerChoice string
	_, _ = fmt.Scanln(&providerChoice)
	if providerChoice == "" {
		providerChoice = "1"
	}

	llmProvider := "anthropic"
	switch providerChoice {
	case "2":
		llmProvider = "bedrock"
	case "3":
		llmProvider = "ollama"
	}

	// Detect available backends
	availableBackends := detectAvailableBackends()

	// Ask which backends to include
	fmt.Println()
	fmt.Println("Available backends:")
	for i, backend := range availableBackends {
		fmt.Printf("  %d. %s\n", i+1, backend)
	}
	fmt.Print("Include backends (comma-separated numbers, e.g., 1,3,4) or 'all' [all]: ")
	var backendsChoice string
	_, _ = fmt.Scanln(&backendsChoice)
	if backendsChoice == "" {
		backendsChoice = "all"
	}

	selectedBackends := availableBackends
	if backendsChoice != "all" && backendsChoice != "" {
		selectedBackends = selectBackendsFromInput(backendsChoice, availableBackends)
	}

	// Generate customized config
	configContent := generateCustomConfig(llmProvider, selectedBackends)

	// Write config
	if err := os.WriteFile(configPath, []byte(configContent), 0600); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing config file: %v\n", err)
		os.Exit(1)
	}

	fmt.Println()
	fmt.Printf("✓ Config file created: %s\n", configPath)
	fmt.Println("\nNext steps:")

	switch llmProvider {
	case "anthropic":
		fmt.Println("1. Save your Anthropic API key:")
		fmt.Println("   looms config set-key anthropic_api_key")
	case "bedrock":
		fmt.Println("1. Configure AWS credentials (choose one method):")
		fmt.Println("   Option A - AWS Profile/SSO:")
		fmt.Println("     aws configure  # or set AWS_PROFILE environment variable")
		fmt.Println("   Option B - Direct credentials (stored in keyring):")
		fmt.Println("     looms config set-key bedrock_access_key_id")
		fmt.Println("     looms config set-key bedrock_secret_access_key")
	case "ollama":
		fmt.Println("1. Ensure Ollama is running:")
		fmt.Println("   ollama serve")
		fmt.Println("   ollama pull qwen2.5:7b")
	}

	fmt.Println("2. Start the server:")
	fmt.Println("   looms serve")
	fmt.Println()
	fmt.Println("Tip: Validate your configuration with 'looms validate file ~/.loom/looms.yaml'")
}

func runConfigSetKey(cmd *cobra.Command, args []string) {
	keyName := args[0]

	// Validate key name using extensible mapping
	availableKeys := ListAvailableSecretKeys()
	validKeys := make(map[string]bool)
	for _, k := range availableKeys {
		validKeys[k] = true
	}

	if !validKeys[keyName] {
		fmt.Fprintf(os.Stderr, "Invalid key name: %s\n", keyName)
		fmt.Fprintf(os.Stderr, "Available keys:\n")
		for _, k := range availableKeys {
			fmt.Fprintf(os.Stderr, "  - %s\n", k)
		}
		os.Exit(1)
	}

	// Read secret from stdin (without echo)
	fmt.Printf("Enter %s (input hidden): ", keyName)
	secretBytes, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println() // New line after hidden input
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading input: %v\n", err)
		os.Exit(1)
	}

	secret := string(secretBytes)
	if secret == "" {
		fmt.Fprintf(os.Stderr, "Secret cannot be empty\n")
		os.Exit(1)
	}

	// Save to keyring
	if err := keyring.Set(ServiceName, keyName, secret); err != nil {
		fmt.Fprintf(os.Stderr, "Error saving to keyring: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("✓ Saved %s to system keyring\n", keyName)
}

func runConfigGetKey(cmd *cobra.Command, args []string) {
	keyName := args[0]

	secret, err := keyring.Get(ServiceName, keyName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error retrieving key: %v\n", err)
		fmt.Fprintf(os.Stderr, "Key not found in keyring. Set it with: looms config set-key %s\n", keyName)
		os.Exit(1)
	}

	// Show partially masked
	masked := maskSecret(secret)
	fmt.Printf("%s: %s\n", keyName, masked)
}

func runConfigDeleteKey(cmd *cobra.Command, args []string) {
	keyName := args[0]

	if err := keyring.Delete(ServiceName, keyName); err != nil {
		fmt.Fprintf(os.Stderr, "Error deleting key: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("✓ Deleted %s from system keyring\n", keyName)
}

func runConfigShow(cmd *cobra.Command, args []string) {
	fmt.Println("Current Configuration:")
	fmt.Println("======================")
	fmt.Println()

	fmt.Println("Server:")
	fmt.Printf("  Host: %s\n", config.Server.Host)
	fmt.Printf("  Port: %d\n", config.Server.Port)
	fmt.Printf("  Reflection: %t\n", config.Server.EnableReflection)
	fmt.Println()

	fmt.Println("LLM:")
	fmt.Printf("  Provider: %s\n", config.LLM.Provider)
	if config.LLM.Provider == "anthropic" {
		fmt.Printf("  Model: %s\n", config.LLM.AnthropicModel)
		if config.LLM.AnthropicAPIKey != "" {
			fmt.Printf("  API Key: %s\n", maskSecret(config.LLM.AnthropicAPIKey))
		} else {
			fmt.Printf("  API Key: (not set)\n")
		}
	}
	fmt.Printf("  Temperature: %.1f\n", config.LLM.Temperature)
	fmt.Printf("  Max Tokens: %d\n", config.LLM.MaxTokens)
	fmt.Println()

	fmt.Println("Database:")
	fmt.Printf("  Path: %s\n", config.Database.Path)
	fmt.Printf("  Driver: %s\n", config.Database.Driver)
	fmt.Println()

	fmt.Println("Observability:")
	fmt.Printf("  Enabled: %t\n", config.Observability.Enabled)
	if config.Observability.Enabled {
		fmt.Printf("  Provider: %s\n", config.Observability.Provider)
		fmt.Printf("  Hawk Endpoint: %s\n", config.Observability.HawkEndpoint)
		if config.Observability.HawkAPIKey != "" {
			fmt.Printf("  Hawk API Key: %s\n", maskSecret(config.Observability.HawkAPIKey))
		} else {
			fmt.Printf("  Hawk API Key: (not set)\n")
		}
	}
	fmt.Println()

	fmt.Println("Logging:")
	fmt.Printf("  Level: %s\n", config.Logging.Level)
	fmt.Printf("  Format: %s\n", config.Logging.Format)
}

func runConfigListKeys(cmd *cobra.Command, args []string) {
	keys := ListAvailableSecretKeys()
	fmt.Println("Available secret keys:")
	fmt.Println("======================")
	for _, key := range keys {
		fmt.Printf("  - %s\n", key)
	}
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  looms config set-key <key-name>")
	fmt.Println("  looms config get-key <key-name>")
	fmt.Println("  looms config delete-key <key-name>")
}

// maskSecret masks a secret for display.
func maskSecret(s string) string {
	if len(s) <= 8 {
		return "***"
	}
	return s[:4] + "..." + s[len(s)-4:]
}

// detectAvailableBackends scans examples/backends/ directory for available backend YAML files.
func detectAvailableBackends() []string {
	// Try multiple possible locations for examples directory
	possiblePaths := []string{
		"examples/backends",
		"./examples/backends",
		"../examples/backends",
		"../../examples/backends",
	}

	var backends []string
	for _, path := range possiblePaths {
		entries, err := os.ReadDir(path)
		if err != nil {
			continue // Try next path
		}

		for _, entry := range entries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".yaml" {
				continue
			}
			backendName := entry.Name()[:len(entry.Name())-5] // Strip .yaml extension
			backends = append(backends, backendName)
		}

		if len(backends) > 0 {
			return backends
		}
	}

	// Fallback to known backends if detection fails
	return []string{"file", "sqlite", "postgres", "mcp-python", "mcp-http"}
}

// selectBackendsFromInput parses user input and returns selected backends.
func selectBackendsFromInput(input string, available []string) []string {
	var selected []string
	parts := splitAndTrim(input, ",")

	for _, part := range parts {
		var idx int
		if _, err := fmt.Sscanf(part, "%d", &idx); err == nil {
			if idx >= 1 && idx <= len(available) {
				selected = append(selected, available[idx-1])
			}
		}
	}

	if len(selected) == 0 {
		return available // Default to all if parsing fails
	}
	return selected
}

// splitAndTrim splits a string by delimiter and trims whitespace.
func splitAndTrim(s, delim string) []string {
	parts := []string{}
	for _, part := range splitString(s, delim) {
		trimmed := trimSpace(part)
		if trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return parts
}

// splitString splits a string by delimiter (simple implementation).
func splitString(s, delim string) []string {
	var result []string
	start := 0
	for i := 0; i < len(s); i++ {
		if i < len(s)-len(delim)+1 && s[i:i+len(delim)] == delim {
			result = append(result, s[start:i])
			start = i + len(delim)
			i += len(delim) - 1
		}
	}
	result = append(result, s[start:])
	return result
}

// trimSpace removes leading and trailing whitespace.
func trimSpace(s string) string {
	start := 0
	end := len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t' || s[start] == '\n' || s[start] == '\r') {
		start++
	}
	for start < end && (s[end-1] == ' ' || s[end-1] == '\t' || s[end-1] == '\n' || s[end-1] == '\r') {
		end--
	}
	return s[start:end]
}

// generateCustomConfig generates a customized configuration based on user choices.
func generateCustomConfig(llmProvider string, backends []string) string {
	config := `# Loom Server Configuration
# Generated by: looms config init

server:
  port: 60051
  host: 0.0.0.0
  enable_reflection: true

llm:
`

	// LLM provider configuration
	switch llmProvider {
	case "anthropic":
		config += `  provider: anthropic
  anthropic_model: claude-sonnet-4-5-20250929
  # anthropic_api_key: set via keyring (looms config set-key anthropic_api_key)
`
	case "bedrock":
		config += `  provider: bedrock
  bedrock_region: us-west-2
  bedrock_model_id: anthropic.claude-sonnet-4-5-20250929-v1:0
  # bedrock_profile: default  # Use AWS profile for authentication
`
	case "ollama":
		config += `  provider: ollama
  ollama_endpoint: http://localhost:11434
  ollama_model: qwen2.5:7b
`
	}

	config += `  temperature: 1.0
  max_tokens: 4096
  timeout_seconds: 60

database:
  path: ./loom.db
  driver: sqlite

observability:
  enabled: false
  provider: hawk
  hawk_endpoint: ""

logging:
  level: info
  format: text

# Multi-agent configuration
agents:
  agents:
`

	// Generate agent config for each selected backend
	for _, backend := range backends {
		agentID := backend + "-agent"
		agentName := capitalizeWords(backend) + " Agent"
		description := getBackendDescription(backend)

		config += fmt.Sprintf(`    %s:
      name: %s
      description: %s
      backend_path: ./examples/backends/%s.yaml
      system_prompt: |
        You are a helpful assistant that can execute operations using %s backend.
        Always explain what you're doing before taking action.
      max_turns: 25
      max_tool_executions: 50
      enable_tracing: true

`, agentID, agentName, description, backend, backend)
	}

	return config
}

// capitalizeWords capitalizes the first letter of each word.
func capitalizeWords(s string) string {
	result := ""
	capitalize := true
	for _, ch := range s {
		if ch == '-' || ch == '_' {
			result += " "
			capitalize = true
		} else if capitalize {
			if ch >= 'a' && ch <= 'z' {
				result += string(ch - 32) // Uppercase conversion
			} else {
				result += string(ch)
			}
			capitalize = false
		} else {
			result += string(ch)
		}
	}
	return result
}

// getBackendDescription returns a human-readable description for a backend.
func getBackendDescription(backend string) string {
	descriptions := map[string]string{
		"file":       "Reads and analyzes files from the file system",
		"sqlite":     "Queries local SQLite databases",
		"postgres":   "Executes SQL queries against PostgreSQL databases",
		"mysql":      "Executes SQL queries against MySQL databases",
		"mcp-python": "Python-based tools via Model Context Protocol",
		"mcp-http":   "Remote HTTP-based tools via Model Context Protocol",
	}

	if desc, ok := descriptions[backend]; ok {
		return desc
	}
	return "Backend operations for " + backend
}

func runConfigSet(cmd *cobra.Command, args []string) {
	key := args[0]
	value := args[1]

	// Get config file path
	configDir := loomconfig.GetLoomDataDir()
	configPath := filepath.Join(configDir, "looms.yaml")

	// Check if config file exists
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Config file not found: %s\n", configPath)
		fmt.Fprintf(os.Stderr, "Run 'looms config init' to create one\n")
		os.Exit(1)
	}

	// Validate key is not a secret (those should use set-key)
	secretKeys := ListAvailableSecretKeys()
	for _, secretKey := range secretKeys {
		if key == secretKey {
			fmt.Fprintf(os.Stderr, "Error: '%s' is a secret key. Use 'looms config set-key %s' instead.\n", key, key)
			os.Exit(1)
		}
	}

	// Load existing config with viper
	v := viper.New()
	v.SetConfigFile(configPath)
	if err := v.ReadInConfig(); err != nil {
		fmt.Fprintf(os.Stderr, "Error reading config file: %v\n", err)
		os.Exit(1)
	}

	// Try to infer type from existing value or common patterns
	inferredValue := inferType(key, value, v)

	// Set the value
	v.Set(key, inferredValue)

	// Write back to file
	if err := v.WriteConfig(); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing config file: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("✓ Set %s = %v\n", key, inferredValue)
}

func runConfigGet(cmd *cobra.Command, args []string) {
	key := args[0]

	// Get config file path
	configDir := loomconfig.GetLoomDataDir()
	configPath := filepath.Join(configDir, "looms.yaml")

	// Check if config file exists
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Config file not found: %s\n", configPath)
		fmt.Fprintf(os.Stderr, "Run 'looms config init' to create one\n")
		os.Exit(1)
	}

	// Load config with viper
	v := viper.New()
	v.SetConfigFile(configPath)
	if err := v.ReadInConfig(); err != nil {
		fmt.Fprintf(os.Stderr, "Error reading config file: %v\n", err)
		os.Exit(1)
	}

	// Get the value
	if !v.IsSet(key) {
		fmt.Fprintf(os.Stderr, "Key not found: %s\n", key)
		os.Exit(1)
	}

	value := v.Get(key)
	fmt.Printf("%s: %v\n", key, value)
}

// inferType attempts to infer the type of a value based on the key name and existing config.
func inferType(key, value string, v *viper.Viper) interface{} {
	// First, check key name patterns for types that must be enforced (like temperature)
	// This prevents issues where YAML converts 1.0 -> 1, changing type from float to int
	if contains(key, "temperature") {
		var floatVal float64
		if _, err := fmt.Sscanf(value, "%f", &floatVal); err == nil {
			return floatVal
		}
	}

	if contains(key, "port") || contains(key, "timeout") || contains(key, "max_tokens") {
		var intVal int
		if _, err := fmt.Sscanf(value, "%d", &intVal); err == nil {
			return intVal
		}
	}

	if contains(key, "enabled") || contains(key, "enable_") {
		if value == "true" {
			return true
		} else if value == "false" {
			return false
		}
	}

	// Check if key already exists - use its type
	if v.IsSet(key) {
		existingValue := v.Get(key)
		switch existingValue.(type) {
		case bool:
			if value == "true" {
				return true
			} else if value == "false" {
				return false
			}
		case int, int64:
			var intVal int
			if _, err := fmt.Sscanf(value, "%d", &intVal); err == nil {
				return intVal
			}
		case float64:
			var floatVal float64
			if _, err := fmt.Sscanf(value, "%f", &floatVal); err == nil {
				return floatVal
			}
		}
	}

	// Default to string
	return value
}

// contains checks if a string contains a substring (case-insensitive).
func contains(s, substr string) bool {
	sLower := toLower(s)
	substrLower := toLower(substr)
	return stringContains(sLower, substrLower)
}

// toLower converts a string to lowercase.
func toLower(s string) string {
	result := ""
	for _, ch := range s {
		if ch >= 'A' && ch <= 'Z' {
			result += string(ch + 32)
		} else {
			result += string(ch)
		}
	}
	return result
}

// stringContains checks if s contains substr.
func stringContains(s, substr string) bool {
	if len(substr) == 0 {
		return true
	}
	if len(substr) > len(s) {
		return false
	}
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
