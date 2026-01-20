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
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/teradata-labs/loom/pkg/versionmgr"
)

const usage = `Loom Version Manager

Manages semantic versions across all Loom project files.

Usage:
  version-manager <command> [flags]

Commands:
  show       Show current version from VERSION file
  verify     Check if all files have consistent versions
  bump       Bump version (major/minor/patch)
  set        Set specific version
  sync       Sync all files to VERSION file (fix drift)

Flags:
  --dry-run      Show changes without applying them
  --commit       Create git commit after update
  --tag          Create git tag after bump (implies --commit)
  --verbose      Show detailed update information

Examples:
  version-manager show
  version-manager verify
  version-manager bump patch
  version-manager bump minor --commit --tag
  version-manager set 1.2.3 --dry-run
  version-manager sync --commit
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(1)
	}

	command := os.Args[1]

	// Separate flags from positional args
	// The Go flag package stops at the first non-flag arg, so we need to handle this manually
	var cmdArgs []string
	var flagArgs []string
	for i := 2; i < len(os.Args); i++ {
		arg := os.Args[i]
		if strings.HasPrefix(arg, "-") {
			flagArgs = append(flagArgs, arg)
		} else {
			cmdArgs = append(cmdArgs, arg)
		}
	}

	// Create a new flag set for this command
	fs := flag.NewFlagSet(command, flag.ExitOnError)
	dryRun := fs.Bool("dry-run", false, "Show changes without applying")
	commit := fs.Bool("commit", false, "Create git commit after update")
	tag := fs.Bool("tag", false, "Create git tag after bump (implies --commit)")
	verbose := fs.Bool("verbose", false, "Show detailed information")

	// Parse only the flags
	fs.Parse(flagArgs)

	// If --tag is set, enable --commit
	if *tag {
		*commit = true
	}

	// Get repository root
	repoRoot, err := getRepoRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	switch command {
	case "show":
		if err := cmdShow(repoRoot); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "verify":
		if err := cmdVerify(repoRoot, *verbose); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "bump":
		if len(cmdArgs) < 1 {
			fmt.Fprintf(os.Stderr, "Error: bump requires argument (major/minor/patch)\n")
			os.Exit(1)
		}
		bumpType := cmdArgs[0]
		if err := cmdBump(repoRoot, bumpType, *dryRun, *commit, *tag, *verbose); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "set":
		if len(cmdArgs) < 1 {
			fmt.Fprintf(os.Stderr, "Error: set requires version argument (e.g., 1.2.3)\n")
			os.Exit(1)
		}
		versionStr := cmdArgs[0]
		if err := cmdSet(repoRoot, versionStr, *dryRun, *commit, *verbose); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "sync":
		if err := cmdSync(repoRoot, *dryRun, *commit, *verbose); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "help", "--help", "-h":
		fmt.Print(usage)

	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n\n", command)
		fmt.Fprint(os.Stderr, usage)
		os.Exit(1)
	}
}

func cmdShow(repoRoot string) error {
	version, err := versionmgr.ReadCanonicalVersion(repoRoot)
	if err != nil {
		return err
	}

	fmt.Printf("%s\n", version.WithV())
	return nil
}

func cmdVerify(repoRoot string, verbose bool) error {
	fmt.Println("Checking version consistency...")

	report, err := versionmgr.DetectDrift(repoRoot)
	if err != nil {
		return err
	}

	report.PrintReport(verbose)

	os.Exit(report.ExitCode())
	return nil
}

func cmdBump(repoRoot, bumpType string, dryRun, commit, tag, verbose bool) error {
	// Read current version
	current, err := versionmgr.ReadCanonicalVersion(repoRoot)
	if err != nil {
		return err
	}

	// Calculate new version
	var newVersion versionmgr.Version
	switch strings.ToLower(bumpType) {
	case "major":
		newVersion = current.BumpMajor()
	case "minor":
		newVersion = current.BumpMinor()
	case "patch":
		newVersion = current.BumpPatch()
	default:
		return fmt.Errorf("invalid bump type: %s (must be major/minor/patch)", bumpType)
	}

	fmt.Printf("Bumping version: %s → %s\n\n", current.WithV(), newVersion.WithV())

	// Update all files
	updater := versionmgr.NewUpdater(repoRoot, dryRun, verbose)
	results, err := updater.UpdateAllFiles(newVersion)
	if err != nil {
		return err
	}

	if dryRun {
		fmt.Printf("\n✅ Dry run complete. Run without --dry-run to apply changes.\n")
		return nil
	}

	// Print summary
	fmt.Printf("\n✅ Successfully updated %d files to %s\n", len(results), newVersion.WithV())

	// Create git commit and tag if requested
	if commit {
		if err := gitCommit(repoRoot, newVersion); err != nil {
			return fmt.Errorf("failed to create commit: %w", err)
		}
		fmt.Printf("✓ Created commit: \"Bump version to %s\"\n", newVersion.WithV())
	}

	if tag {
		if err := gitTag(repoRoot, newVersion); err != nil {
			return fmt.Errorf("failed to create tag: %w", err)
		}
		fmt.Printf("✓ Created tag: %s\n", newVersion.WithV())
	}

	if commit || tag {
		fmt.Printf("\n💡 To push changes: git push origin main --tags\n")
	}

	return nil
}

func cmdSet(repoRoot, versionStr string, dryRun, commit, verbose bool) error {
	// Parse new version
	newVersion, err := versionmgr.ParseVersion(versionStr)
	if err != nil {
		return fmt.Errorf("invalid version: %w", err)
	}

	// Read current version
	current, err := versionmgr.ReadCanonicalVersion(repoRoot)
	if err != nil {
		return err
	}

	fmt.Printf("Setting version: %s → %s\n\n", current.WithV(), newVersion.WithV())

	// Update all files
	updater := versionmgr.NewUpdater(repoRoot, dryRun, verbose)
	results, err := updater.UpdateAllFiles(newVersion)
	if err != nil {
		return err
	}

	if dryRun {
		fmt.Printf("\n✅ Dry run complete. Run without --dry-run to apply changes.\n")
		return nil
	}

	// Print summary
	fmt.Printf("\n✅ Successfully updated %d files to %s\n", len(results), newVersion.WithV())

	// Create git commit if requested
	if commit {
		if err := gitCommit(repoRoot, newVersion); err != nil {
			return fmt.Errorf("failed to create commit: %w", err)
		}
		fmt.Printf("✓ Created commit: \"Set version to %s\"\n", newVersion.WithV())
		fmt.Printf("\n💡 To push changes: git push origin main\n")
	}

	return nil
}

func cmdSync(repoRoot string, dryRun, commit, verbose bool) error {
	// Read canonical version
	canonical, err := versionmgr.ReadCanonicalVersion(repoRoot)
	if err != nil {
		return err
	}

	fmt.Printf("Syncing all files to canonical version: %s\n\n", canonical.WithV())

	// Update all files
	updater := versionmgr.NewUpdater(repoRoot, dryRun, verbose)
	results, err := updater.UpdateAllFiles(canonical)
	if err != nil {
		return err
	}

	if dryRun {
		fmt.Printf("\n✅ Dry run complete. Run without --dry-run to apply changes.\n")
		return nil
	}

	// Print summary
	fmt.Printf("\n✅ Successfully synced %d files to %s\n", len(results), canonical.WithV())

	// Verify no drift remains
	fmt.Println("\nVerifying consistency...")
	report, err := versionmgr.DetectDrift(repoRoot)
	if err != nil {
		return err
	}

	if report.HasDrift {
		return fmt.Errorf("drift still detected after sync")
	}

	fmt.Println("✅ All files are now in sync!")

	// Create git commit if requested
	if commit {
		if err := gitCommit(repoRoot, canonical); err != nil {
			return fmt.Errorf("failed to create commit: %w", err)
		}
		fmt.Printf("\n✓ Created commit: \"Sync all files to version %s\"\n", canonical.WithV())
		fmt.Printf("\n💡 To push changes: git push origin main\n")
	}

	return nil
}

// getRepoRoot finds the repository root by looking for .git directory
func getRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}

	for {
		gitDir := filepath.Join(dir, ".git")
		if _, err := os.Stat(gitDir); err == nil {
			return dir, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("not in a git repository")
		}
		dir = parent
	}
}

// gitCommit creates a git commit with the version change
func gitCommit(repoRoot string, version versionmgr.Version) error {
	cmd := exec.Command("git", "add", "-A")
	cmd.Dir = repoRoot
	if err := cmd.Run(); err != nil {
		return err
	}

	commitMsg := fmt.Sprintf("Bump version to %s\n\nCo-Authored-By: Loom Version Manager <noreply@teradata.com>", version.WithV())
	cmd = exec.Command("git", "commit", "-m", commitMsg)
	cmd.Dir = repoRoot
	return cmd.Run()
}

// gitTag creates a git tag for the version
func gitTag(repoRoot string, version versionmgr.Version) error {
	tagName := version.WithV()
	tagMsg := fmt.Sprintf("Release %s", tagName)

	cmd := exec.Command("git", "tag", "-a", tagName, "-m", tagMsg)
	cmd.Dir = repoRoot
	return cmd.Run()
}
