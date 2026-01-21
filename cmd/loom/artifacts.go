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
	"github.com/teradata-labs/loom/pkg/tui/client"
)

var (
	artifactsLimit          int32
	artifactsOffset         int32
	artifactsSource         string
	artifactsContentType    string
	artifactsTags           []string
	artifactsIncludeDeleted bool
	artifactsHardDelete     bool
	artifactsPurpose        string
	artifactsSearchLimit    int32
	artifactsOutputFile     string
)

var artifactsCmd = &cobra.Command{
	Use:   "artifacts",
	Short: "Manage artifacts",
	Long:  `List, search, upload, download, and delete artifacts.`,
}

var artifactsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List artifacts",
	Long: `List all artifacts with optional filtering.

Examples:
  loom artifacts list
  loom artifacts list --limit 50
  loom artifacts list --source user
  loom artifacts list --content-type "text/csv"
  loom artifacts list --tags sql,report
`,
	Run: runArtifactsListCommand,
}

var artifactsSearchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "Search artifacts with full-text search",
	Long: `Search artifacts using FTS5 full-text search with BM25 ranking.

Examples:
  loom artifacts search "sales report"
  loom artifacts search "excel AND quarterly"
  loom artifacts search --limit 50 "csv data"
`,
	Args: cobra.ExactArgs(1),
	Run:  runArtifactsSearchCommand,
}

var artifactsShowCmd = &cobra.Command{
	Use:   "show <artifact-id-or-name>",
	Short: "Show artifact details",
	Long: `Show detailed information about a specific artifact.

Examples:
  loom artifacts show art_abc123def456
  loom artifacts show data.csv
`,
	Args: cobra.ExactArgs(1),
	Run:  runArtifactsShowCommand,
}

var artifactsUploadCmd = &cobra.Command{
	Use:   "upload <file-path>",
	Short: "Upload a file as an artifact",
	Long: `Upload a file to artifacts storage.

Examples:
  loom artifacts upload ~/data.csv
  loom artifacts upload report.pdf --purpose "Q4 sales report"
  loom artifacts upload data.xlsx --tags excel,sales,2024
`,
	Args: cobra.ExactArgs(1),
	Run:  runArtifactsUploadCommand,
}

var artifactsDownloadCmd = &cobra.Command{
	Use:   "download <artifact-id-or-name>",
	Short: "Download artifact content",
	Long: `Download artifact content to a file or stdout.

Examples:
  loom artifacts download art_abc123def456
  loom artifacts download data.csv --output ~/Downloads/data.csv
`,
	Args: cobra.ExactArgs(1),
	Run:  runArtifactsDownloadCommand,
}

var artifactsDeleteCmd = &cobra.Command{
	Use:   "delete <artifact-id-or-name>",
	Short: "Delete an artifact",
	Long: `Delete an artifact (soft delete by default, use --hard for permanent deletion).

Examples:
  loom artifacts delete art_abc123def456
  loom artifacts delete data.csv --hard
`,
	Args: cobra.ExactArgs(1),
	Run:  runArtifactsDeleteCommand,
}

var artifactsStatsCmd = &cobra.Command{
	Use:   "stats",
	Short: "Show artifact storage statistics",
	Long: `Show storage statistics including total files, sizes, and breakdown by source.

Examples:
  loom artifacts stats
`,
	Run: runArtifactsStatsCommand,
}

func init() {
	// Add subcommands
	artifactsCmd.AddCommand(artifactsListCmd)
	artifactsCmd.AddCommand(artifactsSearchCmd)
	artifactsCmd.AddCommand(artifactsShowCmd)
	artifactsCmd.AddCommand(artifactsUploadCmd)
	artifactsCmd.AddCommand(artifactsDownloadCmd)
	artifactsCmd.AddCommand(artifactsDeleteCmd)
	artifactsCmd.AddCommand(artifactsStatsCmd)

	// Flags for list command
	artifactsListCmd.Flags().Int32VarP(&artifactsLimit, "limit", "n", 20, "Maximum number of artifacts to return")
	artifactsListCmd.Flags().Int32Var(&artifactsOffset, "offset", 0, "Number of artifacts to skip")
	artifactsListCmd.Flags().StringVar(&artifactsSource, "source", "", "Filter by source (user, generated, agent)")
	artifactsListCmd.Flags().StringVar(&artifactsContentType, "content-type", "", "Filter by MIME type")
	artifactsListCmd.Flags().StringSliceVar(&artifactsTags, "tags", []string{}, "Filter by tags (comma-separated)")
	artifactsListCmd.Flags().BoolVar(&artifactsIncludeDeleted, "include-deleted", false, "Include soft-deleted artifacts")

	// Flags for search command
	artifactsSearchCmd.Flags().Int32VarP(&artifactsSearchLimit, "limit", "n", 20, "Maximum number of results to return")

	// Flags for upload command
	artifactsUploadCmd.Flags().StringVar(&artifactsPurpose, "purpose", "", "Purpose or description of the artifact")
	artifactsUploadCmd.Flags().StringSliceVar(&artifactsTags, "tags", []string{}, "Tags for the artifact (comma-separated)")

	// Flags for download command
	artifactsDownloadCmd.Flags().StringVarP(&artifactsOutputFile, "output", "o", "", "Output file path (default: stdout or original filename)")

	// Flags for delete command
	artifactsDeleteCmd.Flags().BoolVar(&artifactsHardDelete, "hard", false, "Permanently delete artifact (cannot be undone)")
}

func runArtifactsListCommand(cmd *cobra.Command, args []string) {
	// Connect to server
	c, err := client.NewClient(client.Config{
		ServerAddr:    serverAddr,
		TLSEnabled:    tlsEnabled,
		TLSInsecure:   tlsInsecure,
		TLSCAFile:     tlsCAFile,
		TLSServerName: tlsServerName,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to connect to Loom server at %s\n", serverAddr)
		fmt.Fprintf(os.Stderr, "Error: %v\n\n", err)
		fmt.Fprintf(os.Stderr, "Make sure the server is running:\n")
		if tlsEnabled {
			fmt.Fprintf(os.Stderr, "  looms serve --config <config-with-tls>\n\n")
		} else {
			fmt.Fprintf(os.Stderr, "  looms serve\n\n")
		}
		os.Exit(1)
	}
	defer c.Close()

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// List artifacts
	artifacts, totalCount, err := c.ListArtifacts(ctx, artifactsSource, artifactsContentType, artifactsTags, artifactsLimit, artifactsOffset, artifactsIncludeDeleted)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error listing artifacts: %v\n", err)
		os.Exit(1)
	}

	if len(artifacts) == 0 {
		fmt.Println("No artifacts found.")
		return
	}

	// Print header
	fmt.Printf("%-25s %-30s %-15s %-15s %s\n", "ID", "NAME", "SOURCE", "CONTENT TYPE", "SIZE")
	fmt.Println(strings.Repeat("-", 95))

	// Print artifacts
	for _, artifact := range artifacts {
		name := artifact.Name
		if len(name) > 30 {
			name = name[:27] + "..."
		}

		source := artifact.Source
		if source == "" {
			source = "unknown"
		}

		contentType := artifact.ContentType
		if contentType == "" {
			contentType = "unknown"
		}
		if len(contentType) > 15 {
			contentType = contentType[:12] + "..."
		}

		size := formatBytes(artifact.SizeBytes)

		fmt.Printf("%-25s %-30s %-15s %-15s %s\n",
			artifact.Id,
			name,
			source,
			contentType,
			size,
		)
	}

	// Print footer
	fmt.Printf("\nShowing %d of %d artifact(s)", len(artifacts), totalCount)
	if artifactsOffset > 0 {
		fmt.Printf(" (offset: %d)", artifactsOffset)
	}
	fmt.Println()
}

func runArtifactsSearchCommand(cmd *cobra.Command, args []string) {
	query := args[0]

	// Connect to server
	c, err := client.NewClient(client.Config{
		ServerAddr:    serverAddr,
		TLSEnabled:    tlsEnabled,
		TLSInsecure:   tlsInsecure,
		TLSCAFile:     tlsCAFile,
		TLSServerName: tlsServerName,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to connect to Loom server at %s\n", serverAddr)
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer c.Close()

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Search artifacts
	artifacts, err := c.SearchArtifacts(ctx, query, artifactsSearchLimit)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error searching artifacts: %v\n", err)
		os.Exit(1)
	}

	if len(artifacts) == 0 {
		fmt.Printf("No artifacts found matching query: %s\n", query)
		return
	}

	// Print header
	fmt.Printf("%-25s %-30s %-15s %s\n", "ID", "NAME", "SOURCE", "PURPOSE")
	fmt.Println(strings.Repeat("-", 95))

	// Print artifacts
	for _, artifact := range artifacts {
		name := artifact.Name
		if len(name) > 30 {
			name = name[:27] + "..."
		}

		source := artifact.Source
		if source == "" {
			source = "unknown"
		}

		purpose := artifact.Purpose
		if purpose == "" {
			purpose = "-"
		}
		if len(purpose) > 30 {
			purpose = purpose[:27] + "..."
		}

		fmt.Printf("%-25s %-30s %-15s %s\n",
			artifact.Id,
			name,
			source,
			purpose,
		)
	}

	// Print footer
	fmt.Printf("\nFound %d artifact(s) matching: %s\n", len(artifacts), query)
}

func runArtifactsShowCommand(cmd *cobra.Command, args []string) {
	idOrName := args[0]

	// Connect to server
	c, err := client.NewClient(client.Config{
		ServerAddr:    serverAddr,
		TLSEnabled:    tlsEnabled,
		TLSInsecure:   tlsInsecure,
		TLSCAFile:     tlsCAFile,
		TLSServerName: tlsServerName,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to connect to Loom server at %s\n", serverAddr)
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer c.Close()

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Get artifact (try as ID first, then as name)
	var artifact *loomv1.Artifact
	if strings.HasPrefix(idOrName, "art_") {
		artifact, err = c.GetArtifact(ctx, idOrName, "")
	} else {
		artifact, err = c.GetArtifact(ctx, "", idOrName)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting artifact: %v\n", err)
		os.Exit(1)
	}

	// Print artifact details
	fmt.Printf("ID: %s\n", artifact.Id)
	fmt.Printf("Name: %s\n", artifact.Name)
	fmt.Printf("Path: %s\n", artifact.Path)
	fmt.Printf("Source: %s\n", artifact.Source)
	if artifact.SourceAgentId != "" {
		fmt.Printf("Source Agent: %s\n", artifact.SourceAgentId)
	}
	if artifact.Purpose != "" {
		fmt.Printf("Purpose: %s\n", artifact.Purpose)
	}
	fmt.Printf("Content Type: %s\n", artifact.ContentType)
	fmt.Printf("Size: %s (%d bytes)\n", formatBytes(artifact.SizeBytes), artifact.SizeBytes)
	fmt.Printf("Checksum: %s\n", artifact.Checksum)

	if artifact.CreatedAt > 0 {
		t := time.Unix(artifact.CreatedAt, 0)
		fmt.Printf("Created: %s (%s)\n", t.Format(time.RFC3339), formatTimeAgo(t))
	}

	if artifact.UpdatedAt > 0 {
		t := time.Unix(artifact.UpdatedAt, 0)
		fmt.Printf("Updated: %s (%s)\n", t.Format(time.RFC3339), formatTimeAgo(t))
	}

	if artifact.LastAccessedAt > 0 {
		t := time.Unix(artifact.LastAccessedAt, 0)
		fmt.Printf("Last Accessed: %s (%s)\n", t.Format(time.RFC3339), formatTimeAgo(t))
	}

	if artifact.AccessCount > 0 {
		fmt.Printf("Access Count: %d\n", artifact.AccessCount)
	}

	if len(artifact.Tags) > 0 {
		fmt.Printf("Tags: %s\n", strings.Join(artifact.Tags, ", "))
	}

	// Print metadata if available
	if len(artifact.Metadata) > 0 {
		fmt.Println("\nMetadata:")
		for k, v := range artifact.Metadata {
			fmt.Printf("  %s: %s\n", k, v)
		}
	}
}

func runArtifactsUploadCommand(cmd *cobra.Command, args []string) {
	filePath := args[0]

	// Expand home directory
	if strings.HasPrefix(filePath, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			filePath = filepath.Join(home, filePath[2:])
		}
	}

	// Connect to server
	c, err := client.NewClient(client.Config{
		ServerAddr:    serverAddr,
		TLSEnabled:    tlsEnabled,
		TLSInsecure:   tlsInsecure,
		TLSCAFile:     tlsCAFile,
		TLSServerName: tlsServerName,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to connect to Loom server at %s\n", serverAddr)
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer c.Close()

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Upload artifact
	artifact, err := c.UploadArtifactFromFile(ctx, filePath, artifactsPurpose, artifactsTags)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error uploading artifact: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Uploaded artifact: %s\n", artifact.Name)
	fmt.Printf("  ID: %s\n", artifact.Id)
	fmt.Printf("  Size: %s\n", formatBytes(artifact.SizeBytes))
	fmt.Printf("  Content Type: %s\n", artifact.ContentType)
	if len(artifact.Tags) > 0 {
		fmt.Printf("  Tags: %s\n", strings.Join(artifact.Tags, ", "))
	}
}

func runArtifactsDownloadCommand(cmd *cobra.Command, args []string) {
	idOrName := args[0]

	// Connect to server
	c, err := client.NewClient(client.Config{
		ServerAddr:    serverAddr,
		TLSEnabled:    tlsEnabled,
		TLSInsecure:   tlsInsecure,
		TLSCAFile:     tlsCAFile,
		TLSServerName: tlsServerName,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to connect to Loom server at %s\n", serverAddr)
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer c.Close()

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Get artifact metadata
	var artifact *loomv1.Artifact
	if strings.HasPrefix(idOrName, "art_") {
		artifact, err = c.GetArtifact(ctx, idOrName, "")
	} else {
		artifact, err = c.GetArtifact(ctx, "", idOrName)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting artifact: %v\n", err)
		os.Exit(1)
	}

	// Get artifact content
	content, _, err := c.GetArtifactContent(ctx, artifact.Id, "", 100) // 100MB limit
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error downloading artifact content: %v\n", err)
		os.Exit(1)
	}

	// Determine output path
	outputPath := artifactsOutputFile
	if outputPath == "" {
		outputPath = artifact.Name
	}

	// Expand home directory
	if strings.HasPrefix(outputPath, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			outputPath = filepath.Join(home, outputPath[2:])
		}
	}

	// Write to file
	if err := os.WriteFile(outputPath, content, 0600); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing file: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Downloaded artifact to: %s\n", outputPath)
	fmt.Printf("  Size: %s\n", formatBytes(artifact.SizeBytes))
}

func runArtifactsDeleteCommand(cmd *cobra.Command, args []string) {
	idOrName := args[0]

	// Connect to server
	c, err := client.NewClient(client.Config{
		ServerAddr:    serverAddr,
		TLSEnabled:    tlsEnabled,
		TLSInsecure:   tlsInsecure,
		TLSCAFile:     tlsCAFile,
		TLSServerName: tlsServerName,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to connect to Loom server at %s\n", serverAddr)
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer c.Close()

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Get artifact ID
	var artifactID string
	if strings.HasPrefix(idOrName, "art_") {
		artifactID = idOrName
	} else {
		artifact, err := c.GetArtifact(ctx, "", idOrName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error finding artifact: %v\n", err)
			os.Exit(1)
		}
		artifactID = artifact.Id
	}

	// Delete artifact
	err = c.DeleteArtifact(ctx, artifactID, artifactsHardDelete)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error deleting artifact: %v\n", err)
		os.Exit(1)
	}

	if artifactsHardDelete {
		fmt.Printf("Permanently deleted artifact: %s\n", idOrName)
	} else {
		fmt.Printf("Soft deleted artifact: %s (can be recovered within 30 days)\n", idOrName)
	}
}

func runArtifactsStatsCommand(cmd *cobra.Command, args []string) {
	// Connect to server
	c, err := client.NewClient(client.Config{
		ServerAddr:    serverAddr,
		TLSEnabled:    tlsEnabled,
		TLSInsecure:   tlsInsecure,
		TLSCAFile:     tlsCAFile,
		TLSServerName: tlsServerName,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to connect to Loom server at %s\n", serverAddr)
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer c.Close()

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Get stats
	stats, err := c.GetArtifactStats(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting artifact stats: %v\n", err)
		os.Exit(1)
	}

	// Print stats
	fmt.Println("Artifact Storage Statistics")
	fmt.Println(strings.Repeat("=", 50))
	fmt.Printf("Total Files:     %d\n", stats.TotalFiles)
	fmt.Printf("Total Size:      %s (%d bytes)\n", formatBytes(stats.TotalSizeBytes), stats.TotalSizeBytes)
	fmt.Printf("User Files:      %d\n", stats.UserFiles)
	fmt.Printf("Generated Files: %d\n", stats.GeneratedFiles)
	if stats.DeletedFiles > 0 {
		fmt.Printf("Deleted Files:   %d (soft-deleted, recoverable)\n", stats.DeletedFiles)
	}
}

// formatBytes formats byte count as human-readable string
func formatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}
