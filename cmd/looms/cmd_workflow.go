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
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/communication"
	loomconfig "github.com/teradata-labs/loom/pkg/config"
	"github.com/teradata-labs/loom/pkg/llm/anthropic"
	"github.com/teradata-labs/loom/pkg/llm/azureopenai"
	"github.com/teradata-labs/loom/pkg/llm/bedrock"
	"github.com/teradata-labs/loom/pkg/llm/gemini"
	"github.com/teradata-labs/loom/pkg/llm/huggingface"
	"github.com/teradata-labs/loom/pkg/llm/mistral"
	"github.com/teradata-labs/loom/pkg/llm/ollama"
	"github.com/teradata-labs/loom/pkg/llm/openai"
	"github.com/teradata-labs/loom/pkg/mcp/manager"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/orchestration"
	"github.com/teradata-labs/loom/pkg/shuttle/builtin"
	"github.com/teradata-labs/loom/pkg/visualization"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
)

var (
	workflowDir     string
	workflowAgents  []string
	workflowDryRun  bool
	workflowTimeout int
)

// workflowCmd represents the workflow command
var workflowCmd = &cobra.Command{
	Use:   "workflow",
	Short: "Manage and execute workflow orchestrations",
	Long: `Manage workflow orchestrations for multi-agent coordination.

Workflows are defined in YAML files using Kubernetes-style structure.
They support 6 orchestration patterns:
- debate: Structured debates with multiple rounds
- fork-join: Parallel execution with merged results
- pipeline: Sequential stages with data flow
- parallel: Independent tasks executed concurrently
- conditional: Dynamic routing based on conditions
- swarm: Collective decision-making through voting

Examples:
  # Validate a workflow file
  looms workflow validate architecture-debate.yaml

  # Execute a workflow
  looms workflow run code-review.yaml

  # List available workflows
  looms workflow list examples/workflows

  # Dry-run (validate without executing)
  looms workflow run --dry-run feature-pipeline.yaml`,
}

// validateCmd validates a workflow YAML file
var validateCmd = &cobra.Command{
	Use:   "validate <file>",
	Short: "Validate a workflow YAML file",
	Long: `Validate a workflow YAML file for syntax and structure.

Checks:
- YAML syntax
- Required fields (apiVersion, kind, metadata.name, spec.type)
- Pattern-specific fields
- Nested workflow patterns (for conditional branches)

Exit codes:
  0 - Workflow is valid
  1 - Validation failed`,
	Args: cobra.ExactArgs(1),
	Run:  runValidate,
}

// runCmd executes a workflow
var runCmd = &cobra.Command{
	Use:   "run <file>",
	Short: "Execute a workflow from YAML file",
	Long: `Execute a workflow orchestration from YAML file.

The workflow must reference agents that are available in the system.
Agent IDs in the YAML must match registered agents or be created dynamically.

Flags:
  --threads   Comma-separated list of thread IDs to register (optional)
  --dry-run   Validate workflow without executing
  --timeout   Execution timeout in seconds (default: 3600)

Examples:
  # Execute workflow with default threads
  looms workflow run architecture-debate.yaml

  # Execute with specific threads
  looms workflow run --threads=architect,pragmatist code-review.yaml

  # Validate without executing
  looms workflow run --dry-run feature-pipeline.yaml`,
	Args: cobra.ExactArgs(1),
	Run:  runWorkflow,
}

// listCmd lists available workflow files
var listCmd = &cobra.Command{
	Use:   "list [directory]",
	Short: "List available workflow files",
	Long: `List workflow YAML files in a directory.

Scans for files with .yaml or .yml extension and validates
that they contain workflow definitions (apiVersion: loom/v1, kind: Workflow).

If no directory is specified, searches:
  1. ./workflows
  2. ./examples/workflows
  3. $LOOM_DATA_DIR/workflows

Examples:
  # List workflows in current directory
  looms workflow list .

  # List workflows in specific directory
  looms workflow list examples/workflows

  # List workflows in default locations
  looms workflow list`,
	Args: cobra.MaximumNArgs(1),
	Run:  runList,
}

func init() {
	// Add workflow command to root
	rootCmd.AddCommand(workflowCmd)

	// Add subcommands
	workflowCmd.AddCommand(validateCmd)
	workflowCmd.AddCommand(runCmd)
	workflowCmd.AddCommand(listCmd)

	// Flags for run command
	runCmd.Flags().StringSliceVar(&workflowAgents, "threads", []string{}, "Comma-separated thread IDs to register")
	runCmd.Flags().BoolVar(&workflowDryRun, "dry-run", false, "Validate without executing")
	runCmd.Flags().IntVar(&workflowTimeout, "timeout", 3600, "Execution timeout in seconds")

	// Flags for list command
	listCmd.Flags().StringVarP(&workflowDir, "dir", "d", "", "Directory to search (default: auto-detect)")
}

// runValidate validates a workflow file
func runValidate(cmd *cobra.Command, args []string) {
	filePath := args[0]

	// Load and validate workflow
	pattern, err := orchestration.LoadWorkflowFromYAML(filePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Validation failed: %v\n", err)
		os.Exit(1)
	}

	// Print success
	fmt.Printf("✅ Workflow is valid: %s\n", filePath)
	fmt.Printf("   Pattern type: %T\n", pattern.Pattern)

	// Show pattern details
	printPatternSummary(pattern)
}

// runWorkflow executes a workflow
func runWorkflow(cmd *cobra.Command, args []string) {
	filePath := args[0]

	// Load workflow
	pattern, err := orchestration.LoadWorkflowFromYAML(filePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to load workflow: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("📄 Loaded workflow: %s\n", filePath)
	printPatternSummary(pattern)

	// Dry-run mode
	if workflowDryRun {
		fmt.Println("\n✅ Dry-run successful (workflow not executed)")
		return
	}

	// Initialize LLM provider
	llmProvider, providerName := createLLMProvider()
	fmt.Printf("\n🤖 LLM Provider: %s\n", providerName)
	fmt.Printf("   Model: %s\n\n", llmProvider.Model())

	// Create production logger with INFO level (stack traces only for ERROR level)
	zapConfig := zap.NewProductionConfig()
	zapConfig.Level = zap.NewAtomicLevelAt(zap.InfoLevel)
	logger, err := zapConfig.Build(zap.AddStacktrace(zap.ErrorLevel))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create logger: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = logger.Sync() }()

	// Create tracer (use Hawk if observability enabled)
	var tracer observability.Tracer
	if config.Observability.Enabled && config.Observability.HawkEndpoint != "" {
		hawkTracer, err := observability.NewHawkTracer(observability.HawkConfig{
			Endpoint: config.Observability.HawkEndpoint,
			APIKey:   config.Observability.HawkAPIKey,
		})
		if err != nil {
			logger.Warn("Failed to create Hawk tracer, using no-op tracer", zap.Error(err))
			tracer = observability.NewNoOpTracer()
		} else {
			tracer = hawkTracer
			logger.Info("Observability enabled for workflow", zap.String("endpoint", config.Observability.HawkEndpoint))
		}
	} else {
		tracer = observability.NewNoOpTracer()
	}

	// Create session store for telemetry
	dbPath := config.Database.Path
	if dbPath == "" {
		dbPath = filepath.Join(loomconfig.GetLoomDataDir(), "loom.db")
	}
	sessionStore, err := agent.NewSessionStore(dbPath, tracer)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create session store: %v\n", err)
		os.Exit(1)
	}
	defer sessionStore.Close()

	// Initialize MCP manager if MCP servers are configured
	var mcpMgr *mcpManager
	if len(config.MCP.Servers) > 0 {
		logger.Info("Initializing MCP servers for workflow", zap.Int("count", len(config.MCP.Servers)))
		mcpMgr, err = initializeMCPManager(config, logger)
		if err != nil {
			logger.Warn("Failed to initialize MCP manager", zap.Error(err))
			logger.Warn("Agents will not have access to MCP tools")
		} else {
			logger.Info("MCP manager initialized successfully", zap.Int("servers_started", len(config.MCP.Servers)))
		}
	}

	// Determine config directory for agent YAMLs
	configDir := loomconfig.GetLoomDataDir()

	// Create agent registry to load agent configurations with MCP tools
	var mcpManager *manager.Manager
	if mcpMgr != nil {
		mcpManager = mcpMgr.GetManager()
	}

	registry, err := agent.NewRegistry(agent.RegistryConfig{
		ConfigDir:    configDir,
		DBPath:       dbPath,
		MCPManager:   mcpManager,
		LLMProvider:  llmProvider,
		Logger:       logger,
		Tracer:       tracer,
		SessionStore: sessionStore,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create agent registry: %v\n", err)
		os.Exit(1)
	}

	// Initialize MessageBus and SharedMemory for workflow communication
	// This enables autonomous agent coordination in iterative workflows
	memoryStore := communication.NewMemoryStore(5 * time.Minute)
	messageBus := communication.NewMessageBus(memoryStore, nil, tracer, logger)
	sharedMemory, err := communication.NewSharedMemoryStore(tracer, logger)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create shared memory: %v\n", err)
		os.Exit(1)
	}
	logger.Info("Initialized communication infrastructure",
		zap.Bool("message_bus", true),
		zap.Bool("shared_memory", true))

	// Create LLM concurrency semaphore to prevent rate limiting
	llmConcurrencyLimit := 2
	llmSemaphore := make(chan struct{}, llmConcurrencyLimit)
	logger.Info("LLM concurrency limit configured for workflow execution",
		zap.Int("limit", llmConcurrencyLimit))

	// Create orchestrator with registry and communication infrastructure
	orchestrator := orchestration.NewOrchestrator(orchestration.Config{
		Registry:     registry,
		Logger:       logger,
		Tracer:       tracer,
		LLMProvider:  llmProvider,
		MessageBus:   messageBus,
		SharedMemory: sharedMemory,
		LLMSemaphore: llmSemaphore,
	})

	// Load all agent configs from the directory first
	ctx := context.Background()
	fmt.Println("🔧 Loading agents from registry...")
	if err := registry.LoadAgents(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "   ❌ Failed to load agents: %v\n", err)
		os.Exit(1)
	}

	// Extract agent IDs from workflow
	agentIDs := extractAgentIDs(pattern)

	// Check if this is an iterative workflow with restart enabled
	isIterativeWithRestart := false
	if iterative := pattern.GetIterative(); iterative != nil {
		if iterative.RestartPolicy != nil && iterative.RestartPolicy.Enabled {
			isIterativeWithRestart = true
			logger.Info("Iterative workflow with restart policy detected",
				zap.Int32("max_iterations", iterative.MaxIterations),
				zap.String("restart_topic", iterative.RestartTopic))
		}
	}

	// Create and register each agent needed for the workflow
	fmt.Println("🔧 Creating and registering agents:")
	for _, agentID := range agentIDs {
		// Create agent (builds from config and initializes tools including MCP)
		logger.Info("Creating agent", zap.String("agent", agentID))
		ag, err := registry.CreateAgent(ctx, agentID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "   ❌ Failed to create agent %s: %v\n", agentID, err)
			os.Exit(1)
		}

		// Start the agent (marks as running)
		if err := registry.StartAgent(ctx, agentID); err != nil {
			fmt.Fprintf(os.Stderr, "   ❌ Failed to start agent %s: %v\n", agentID, err)
			os.Exit(1)
		}

		// Auto-inject restart coordination tool for iterative workflows
		if isIterativeWithRestart {
			restartTool := builtin.NewPublishRestartTool(messageBus, agentID)
			ag.RegisterTool(restartTool)
			logger.Info("Injected restart coordination tool",
				zap.String("agent", agentID),
				zap.String("tool", restartTool.Name()))
		}

		// Auto-inject presentation tools for all workflow agents
		// These tools enable agents to query and visualize workflow data
		topNTool := builtin.NewTopNQueryTool(sharedMemory, agentID)
		ag.RegisterTool(topNTool)

		groupByTool := builtin.NewGroupByQueryTool(sharedMemory, agentID)
		ag.RegisterTool(groupByTool)

		vizTool := visualization.NewVisualizationTool()
		ag.RegisterTool(vizTool)

		// Auto-inject file_write tool so agents can save reports/results
		fileWriteTool := builtin.NewFileWriteTool("")
		ag.RegisterTool(fileWriteTool)

		// Auto-inject shared memory tools for hybrid context passing (v3.13+)
		// Agents can read full stage outputs from SharedMemory when truncated context isn't enough
		sharedMemReadTool := builtin.NewSharedMemoryReadTool(sharedMemory, agentID)
		ag.RegisterTool(sharedMemReadTool)

		sharedMemWriteTool := builtin.NewSharedMemoryWriteTool(sharedMemory, agentID)
		ag.RegisterTool(sharedMemWriteTool)

		// Auto-inject pub-sub tools for broadcast communication
		// Note: subscribe/receive_broadcast removed - workflow agents auto-subscribed and messages auto-injected
		publishTool := builtin.NewPublishTool(messageBus, agentID)
		ag.RegisterTool(publishTool)

		logger.Info("Injected presentation and communication tools",
			zap.String("agent", agentID),
			zap.Int("presentation_tools", 4),
			zap.Int("communication_tools", 3))

		orchestrator.RegisterAgent(agentID, ag)
		toolCount := ag.ToolCount()
		if toolCount > 0 {
			fmt.Printf("   - %s (%d tools)\n", agentID, toolCount)
		} else {
			fmt.Printf("   - %s\n", agentID)
		}
	}

	// Execute workflow
	fmt.Println("\n⚡ Executing workflow...")
	startTime := time.Now()

	// Add timeout to context if specified
	if workflowTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(workflowTimeout)*time.Second)
		defer cancel()
	}

	result, err := orchestrator.ExecutePattern(ctx, pattern)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\n❌ Execution failed: %v\n", err)
		os.Exit(1)
	}

	duration := time.Since(startTime)

	// Print results
	fmt.Println("\n" + strings.Repeat("=", 80))
	fmt.Println("✅ WORKFLOW COMPLETED")
	fmt.Println(strings.Repeat("=", 80))
	fmt.Printf("\nDuration: %.2fs\n", duration.Seconds())
	fmt.Printf("Total cost: $%.4f\n", result.Cost.TotalCostUsd)
	fmt.Printf("Total tokens: %d\n", result.Cost.TotalTokens)
	fmt.Printf("LLM calls: %d\n\n", result.Cost.LlmCalls)

	// Print detailed results based on pattern type
	if debateResult := result.GetDebateResult(); debateResult != nil {
		printDebateResults(result, debateResult)
	} else {
		// Print merged output for non-debate patterns
		fmt.Println("📊 Result:")
		fmt.Println(strings.Repeat("-", 80))
		fmt.Println(result.MergedOutput)
		fmt.Println(strings.Repeat("-", 80))
	}
}

// printDebateResults prints detailed debate results with thinking, tool usage, and model info.
func printDebateResults(result *loomv1.WorkflowResult, debateResult *loomv1.DebateResult) {
	fmt.Println("🎭 Debate Results:")
	fmt.Println(strings.Repeat("=", 80))

	// Print each round
	for _, round := range debateResult.Rounds {
		fmt.Printf("\n## Round %d\n\n", round.RoundNumber)

		for _, pos := range round.Positions {
			fmt.Printf("### %s\n", pos.AgentId)

			// Show model info
			if pos.Model != "" {
				fmt.Printf("**Model:** %s", pos.Model)
				if pos.Provider != "" {
					fmt.Printf(" (%s)", pos.Provider)
				}
				fmt.Printf("\n")
			}

			fmt.Printf("**Confidence:** %.0f%%\n\n", pos.Confidence*100)

			// Show tool usage
			if pos.ToolCallCount > 0 {
				fmt.Printf("**Tools Used** (%d calls): %s\n\n",
					pos.ToolCallCount,
					strings.Join(pos.ToolsUsed, ", "))
			}

			// Show thinking process if available
			if pos.Thinking != "" {
				fmt.Printf("**Reasoning:**\n```\n%s\n```\n\n", pos.Thinking)
			}

			fmt.Printf("**Position:** %s\n\n", pos.Position)

			if len(pos.Arguments) > 0 {
				fmt.Printf("**Arguments:**\n")
				for i, arg := range pos.Arguments {
					fmt.Printf("%d. %s\n", i+1, arg)
				}
				fmt.Printf("\n")
			}
		}

		if round.Synthesis != "" {
			fmt.Printf("**Round Synthesis:**\n%s\n\n", round.Synthesis)
		}

		if round.ConsensusReached {
			fmt.Println("✅ Consensus reached in this round!")
		}

		fmt.Println(strings.Repeat("-", 80))
	}

	// Print final consensus
	fmt.Println("\n## Final Consensus")
	fmt.Println()
	fmt.Println(debateResult.Consensus)
	fmt.Println()

	if debateResult.ConsensusAchieved {
		fmt.Println("✅ Full consensus achieved")
	} else {
		fmt.Println("⚠️  No full consensus - showing best synthesis")
	}

	if debateResult.ModeratorSynthesis != "" {
		fmt.Println("\n## Moderator's Synthesis")
		fmt.Println()
		fmt.Println(debateResult.ModeratorSynthesis)
	}

	// Show model summary
	if len(result.ModelsUsed) > 0 {
		fmt.Println("\n## Models Used")
		fmt.Println()
		for agentID, model := range result.ModelsUsed {
			fmt.Printf("- **%s**: %s\n", agentID, model)
		}
		fmt.Println()
	}

	fmt.Println(strings.Repeat("=", 80))
}

// runList lists workflow files
func runList(cmd *cobra.Command, args []string) {
	// Determine directory to search
	searchDir := "."
	if len(args) > 0 {
		searchDir = args[0]
	} else if workflowDir != "" {
		searchDir = workflowDir
	} else {
		// Try default locations
		defaultDirs := []string{
			"./workflows",
			"./examples/workflows",
			filepath.Join(loomconfig.GetLoomDataDir(), "workflows"),
		}

		for _, dir := range defaultDirs {
			if info, err := os.Stat(dir); err == nil && info.IsDir() {
				searchDir = dir
				break
			}
		}
	}

	// Scan for workflow files
	fmt.Printf("📁 Scanning: %s\n\n", searchDir)

	var workflows []workflowInfo
	err := filepath.Walk(searchDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip errors
		}

		// Check if it's a YAML file
		if info.IsDir() || (!strings.HasSuffix(path, ".yaml") && !strings.HasSuffix(path, ".yml")) {
			return nil
		}

		// Try to parse as workflow
		pattern, err := orchestration.LoadWorkflowFromYAML(path)
		if err != nil {
			return nil // Skip invalid files
		}

		// Extract metadata
		relPath, _ := filepath.Rel(searchDir, path)
		wf := workflowInfo{
			Path:    relPath,
			Type:    getPatternType(pattern),
			IsValid: true,
		}

		// Try to read metadata
		if metadata := extractMetadata(path); metadata != nil {
			wf.Name = metadata.Name
			wf.Description = metadata.Description
		}

		workflows = append(workflows, wf)
		return nil
	})

	if err != nil {
		fmt.Fprintf(os.Stderr, "Error scanning directory: %v\n", err)
		os.Exit(1)
	}

	// Print workflows
	if len(workflows) == 0 {
		fmt.Println("No workflow files found.")
		return
	}

	fmt.Printf("Found %d workflow(s):\n\n", len(workflows))
	for _, wf := range workflows {
		fmt.Printf("📝 %s\n", wf.Path)
		if wf.Name != "" {
			fmt.Printf("   Name: %s\n", wf.Name)
		}
		fmt.Printf("   Type: %s\n", wf.Type)
		if wf.Description != "" {
			fmt.Printf("   Description: %s\n", wf.Description)
		}
		fmt.Println()
	}
}

// Helper types and functions

type workflowInfo struct {
	Path        string
	Name        string
	Type        string
	Description string
	IsValid     bool
}

// createLLMProvider creates an LLM provider using config settings (same logic as serve command)
func createLLMProvider() (agent.LLMProvider, string) {
	// First check config file (preferred method)
	if config.LLM.Provider != "" {
		switch config.LLM.Provider {
		case "anthropic":
			if config.LLM.AnthropicAPIKey != "" {
				client := anthropic.NewClient(anthropic.Config{
					APIKey:      config.LLM.AnthropicAPIKey,
					Model:       config.LLM.AnthropicModel,
					Temperature: config.LLM.Temperature,
					MaxTokens:   config.LLM.MaxTokens,
				})
				return client, "Anthropic (config)"
			}

		case "bedrock":
			client, err := bedrock.NewClient(bedrock.Config{
				Profile:         config.LLM.BedrockProfile,
				Region:          config.LLM.BedrockRegion,
				ModelID:         config.LLM.BedrockModelID,
				AccessKeyID:     config.LLM.BedrockAccessKeyID,
				SecretAccessKey: config.LLM.BedrockSecretAccessKey,
				SessionToken:    config.LLM.BedrockSessionToken,
				MaxTokens:       config.LLM.MaxTokens,
				Temperature:     config.LLM.Temperature,
			})
			if err == nil {
				return client, "AWS Bedrock (config)"
			}

		case "ollama":
			client := ollama.NewClient(ollama.Config{
				Endpoint:    config.LLM.OllamaEndpoint,
				Model:       config.LLM.OllamaModel,
				MaxTokens:   config.LLM.MaxTokens,
				Temperature: config.LLM.Temperature,
				Timeout:     time.Duration(config.LLM.Timeout) * time.Second,
			})
			return client, "Ollama (config)"

		case "openai":
			if config.LLM.OpenAIAPIKey != "" {
				client := openai.NewClient(openai.Config{
					APIKey:      config.LLM.OpenAIAPIKey,
					Model:       config.LLM.OpenAIModel,
					MaxTokens:   config.LLM.MaxTokens,
					Temperature: config.LLM.Temperature,
					Timeout:     time.Duration(config.LLM.Timeout) * time.Second,
				})
				return client, "OpenAI (config)"
			}

		case "azure-openai", "azureopenai":
			client, err := azureopenai.NewClient(azureopenai.Config{
				Endpoint:     config.LLM.AzureOpenAIEndpoint,
				DeploymentID: config.LLM.AzureOpenAIDeploymentID,
				APIKey:       config.LLM.AzureOpenAIAPIKey,
				EntraToken:   config.LLM.AzureOpenAIEntraToken,
				MaxTokens:    config.LLM.MaxTokens,
				Temperature:  config.LLM.Temperature,
				Timeout:      time.Duration(config.LLM.Timeout) * time.Second,
			})
			if err == nil {
				return client, "Azure OpenAI (config)"
			}

		case "mistral":
			if config.LLM.MistralAPIKey != "" {
				client := mistral.NewClient(mistral.Config{
					APIKey:      config.LLM.MistralAPIKey,
					Model:       config.LLM.MistralModel,
					MaxTokens:   config.LLM.MaxTokens,
					Temperature: config.LLM.Temperature,
					Timeout:     time.Duration(config.LLM.Timeout) * time.Second,
				})
				return client, "Mistral AI (config)"
			}

		case "gemini":
			if config.LLM.GeminiAPIKey != "" {
				client := gemini.NewClient(gemini.Config{
					APIKey:      config.LLM.GeminiAPIKey,
					Model:       config.LLM.GeminiModel,
					MaxTokens:   config.LLM.MaxTokens,
					Temperature: config.LLM.Temperature,
					Timeout:     time.Duration(config.LLM.Timeout) * time.Second,
				})
				return client, "Google Gemini (config)"
			}

		case "huggingface":
			if config.LLM.HuggingFaceToken != "" {
				client := huggingface.NewClient(huggingface.Config{
					Token:       config.LLM.HuggingFaceToken,
					Model:       config.LLM.HuggingFaceModel,
					MaxTokens:   config.LLM.MaxTokens,
					Temperature: config.LLM.Temperature,
					Timeout:     time.Duration(config.LLM.Timeout) * time.Second,
				})
				return client, "HuggingFace (config)"
			}
		}
	}

	// Fall back to environment variables
	// Try Anthropic first
	if apiKey := os.Getenv("ANTHROPIC_API_KEY"); apiKey != "" {
		client := anthropic.NewClient(anthropic.Config{
			APIKey:      apiKey,
			Model:       "claude-sonnet-4-5-20250929",
			Temperature: 1.0,
			MaxTokens:   4096,
		})
		return client, "Anthropic (env)"
	}

	// Try Bedrock
	bedrockClient, err := bedrock.NewClient(bedrock.Config{
		Profile: "bedrock",
		Region:  "us-west-2",
		ModelID: "anthropic.claude-sonnet-4-5-20250929-v1:0",
	})
	if err == nil {
		return bedrockClient, "AWS Bedrock (env)"
	}

	fmt.Fprint(os.Stderr, `
❌ No LLM provider configured!

Please configure an LLM provider:

Option 1: Configure in $LOOM_DATA_DIR/looms.yaml (recommended)
  looms config init

Option 2: Anthropic Direct API (env var)
  export ANTHROPIC_API_KEY=your_key

Option 3: AWS Bedrock (env var)
  Configure AWS credentials with profile 'bedrock' in ~/.aws/credentials

Supported providers:
  - anthropic    (Anthropic Claude API)
  - bedrock      (AWS Bedrock)
  - ollama       (Local Ollama)
  - openai       (OpenAI GPT models)
  - azure-openai (Azure OpenAI Service)
  - mistral      (Mistral AI)
  - gemini       (Google Gemini)
  - huggingface  (HuggingFace Inference)

Then run again.
`)
	os.Exit(1)
	return nil, ""
}

// extractAgentIDs extracts all agent IDs from a workflow pattern
func extractAgentIDs(pattern *loomv1.WorkflowPattern) []string {
	ids := make(map[string]bool)

	switch p := pattern.Pattern.(type) {
	case *loomv1.WorkflowPattern_Debate:
		for _, id := range p.Debate.AgentIds {
			ids[id] = true
		}
		if p.Debate.ModeratorAgentId != "" {
			ids[p.Debate.ModeratorAgentId] = true
		}
	case *loomv1.WorkflowPattern_ForkJoin:
		for _, id := range p.ForkJoin.AgentIds {
			ids[id] = true
		}
	case *loomv1.WorkflowPattern_Pipeline:
		for _, stage := range p.Pipeline.Stages {
			ids[stage.AgentId] = true
		}
	case *loomv1.WorkflowPattern_Parallel:
		for _, task := range p.Parallel.Tasks {
			ids[task.AgentId] = true
		}
	case *loomv1.WorkflowPattern_Conditional:
		ids[p.Conditional.ConditionAgentId] = true
		// Extract from branches (recursively)
		for _, branch := range p.Conditional.Branches {
			for _, id := range extractAgentIDs(branch) {
				ids[id] = true
			}
		}
		if p.Conditional.DefaultBranch != nil {
			for _, id := range extractAgentIDs(p.Conditional.DefaultBranch) {
				ids[id] = true
			}
		}
	}

	result := make([]string, 0, len(ids))
	for id := range ids {
		result = append(result, id)
	}
	return result
}

// getPatternType returns the pattern type as a string
func getPatternType(pattern *loomv1.WorkflowPattern) string {
	switch pattern.Pattern.(type) {
	case *loomv1.WorkflowPattern_Debate:
		return "debate"
	case *loomv1.WorkflowPattern_ForkJoin:
		return "fork-join"
	case *loomv1.WorkflowPattern_Pipeline:
		return "pipeline"
	case *loomv1.WorkflowPattern_Parallel:
		return "parallel"
	case *loomv1.WorkflowPattern_Conditional:
		return "conditional"
	default:
		return "unknown"
	}
}

// printPatternSummary prints a summary of the pattern
func printPatternSummary(pattern *loomv1.WorkflowPattern) {
	fmt.Printf("   Pattern: %s\n", getPatternType(pattern))
	agentIDs := extractAgentIDs(pattern)
	fmt.Printf("   Agents: %d (%s)\n", len(agentIDs), strings.Join(agentIDs, ", "))
}

// extractMetadata reads metadata from YAML file
func extractMetadata(path string) *struct {
	Name        string
	Description string
} {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	var config struct {
		Metadata struct {
			Name        string `yaml:"name"`
			Description string `yaml:"description"`
		} `yaml:"metadata"`
	}

	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil
	}

	return &struct {
		Name        string
		Description string
	}{
		Name:        config.Metadata.Name,
		Description: config.Metadata.Description,
	}
}
