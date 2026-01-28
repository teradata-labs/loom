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
	"github.com/teradata-labs/loom/internal/version"
	loomconfig "github.com/teradata-labs/loom/pkg/config"
)

var (
	cfgFile string
	config  *Config
)

// rootCmd represents the base command
var rootCmd = &cobra.Command{
	Use:     "looms",
	Short:   "Loom Server - Multi-domain autonomous LLM agent runtime",
	Long:    `Loom Server (looms) provides a gRPC API for autonomous LLM agents with pattern-guided learning, tool execution, and multi-domain backends.`,
	Version: version.Get(),
}

// Execute runs the root command
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func init() {
	cobra.OnInitialize(initConfig)

	// Custom help template with weave subcommands and Support at bottom
	rootCmd.SetHelpTemplate(`{{with (or .Long .Short)}}{{. | trimTrailingWhitespaces}}

{{end}}{{if or .Runnable .HasSubCommands}}{{.UsageString}}{{end}}

Weave Commands:
  looms weave "<requirements>"  Create thread from natural language
  looms weave list              List all threads
  looms weave stats             Show weaver learning statistics
  looms weave insights          Get improvement suggestions
  looms weave feedback          Record thread feedback
  looms weave refine            Refine existing thread

Support:
  GitHub: https://github.com/teradata-labs/loom/issues
  Documentation: https://github.com/teradata-labs/loom
`)

	// Global flags
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default: $LOOM_DATA_DIR/looms.yaml)")

	// Server flags
	rootCmd.PersistentFlags().Int("port", 60051, "gRPC server port")
	rootCmd.PersistentFlags().String("host", "0.0.0.0", "gRPC server host")
	rootCmd.PersistentFlags().Int("http-port", 5006, "HTTP/REST+SSE server port (0=disabled)")
	rootCmd.PersistentFlags().Bool("reflection", true, "enable gRPC reflection")

	// LLM flags
	rootCmd.PersistentFlags().String("llm-provider", "anthropic", "LLM provider (anthropic, bedrock, ollama)")
	rootCmd.PersistentFlags().String("anthropic-key", "", "Anthropic API key (or use keyring/env)")
	rootCmd.PersistentFlags().String("anthropic-model", "claude-sonnet-4-5-20250929", "Anthropic model")
	rootCmd.PersistentFlags().Float64("temperature", 1.0, "LLM temperature")
	rootCmd.PersistentFlags().Int("max-tokens", 4096, "Maximum tokens per request")

	// Database flags
	// GetLoomDataDir respects LOOM_DATA_DIR environment variable
	defaultDBPath := filepath.Join(loomconfig.GetLoomDataDir(), "loom.db")
	rootCmd.PersistentFlags().String("db", defaultDBPath, "SQLite database path")

	// Observability flags (enabled by default)
	rootCmd.PersistentFlags().Bool("observability", true, "Enable observability (use --observability=false to disable)")
	rootCmd.PersistentFlags().String("hawk-endpoint", "", "Hawk endpoint URL")
	rootCmd.PersistentFlags().String("hawk-key", "", "Hawk API key (or use keyring/env)")

	// Logging flags
	rootCmd.PersistentFlags().String("log-level", "info", "Log level (debug, info, warn, error)")
	rootCmd.PersistentFlags().String("log-format", "text", "Log format (text, json)")

	// Tool permission flags
	rootCmd.PersistentFlags().Bool("yolo", false, "Bypass all tool permission prompts (YOLO mode)")
	rootCmd.PersistentFlags().Bool("require-approval", false, "Require user approval before executing tools")

	// Bind flags to viper
	_ = viper.BindPFlag("server.port", rootCmd.PersistentFlags().Lookup("port"))
	_ = viper.BindPFlag("server.host", rootCmd.PersistentFlags().Lookup("host"))
	_ = viper.BindPFlag("server.http_port", rootCmd.PersistentFlags().Lookup("http-port"))
	_ = viper.BindPFlag("server.enable_reflection", rootCmd.PersistentFlags().Lookup("reflection"))

	_ = viper.BindPFlag("llm.provider", rootCmd.PersistentFlags().Lookup("llm-provider"))
	_ = viper.BindPFlag("llm.anthropic_api_key", rootCmd.PersistentFlags().Lookup("anthropic-key"))
	_ = viper.BindPFlag("llm.anthropic_model", rootCmd.PersistentFlags().Lookup("anthropic-model"))
	_ = viper.BindPFlag("llm.temperature", rootCmd.PersistentFlags().Lookup("temperature"))
	_ = viper.BindPFlag("llm.max_tokens", rootCmd.PersistentFlags().Lookup("max-tokens"))

	_ = viper.BindPFlag("database.path", rootCmd.PersistentFlags().Lookup("db"))

	_ = viper.BindPFlag("observability.enabled", rootCmd.PersistentFlags().Lookup("observability"))
	_ = viper.BindPFlag("observability.hawk_endpoint", rootCmd.PersistentFlags().Lookup("hawk-endpoint"))
	_ = viper.BindPFlag("observability.hawk_api_key", rootCmd.PersistentFlags().Lookup("hawk-key"))

	_ = viper.BindPFlag("logging.level", rootCmd.PersistentFlags().Lookup("log-level"))
	_ = viper.BindPFlag("logging.format", rootCmd.PersistentFlags().Lookup("log-format"))

	_ = viper.BindPFlag("tools.permissions.yolo", rootCmd.PersistentFlags().Lookup("yolo"))
	_ = viper.BindPFlag("tools.permissions.require_approval", rootCmd.PersistentFlags().Lookup("require-approval"))
}

// initConfig reads in config file and ENV variables if set.
func initConfig() {
	var err error
	config, err = LoadConfig(cfgFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}
}
