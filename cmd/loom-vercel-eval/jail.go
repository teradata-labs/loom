package main

import (
	"context"
	"fmt"
	"time"

	"github.com/teradata-labs/loom/pkg/shelljail"
	"github.com/teradata-labs/loom/pkg/shellpolicy"
	"github.com/teradata-labs/loom/pkg/shuttle/builtin"
)

// Shell modes for the agent's shell_execute tool.
const (
	shellJailed = "jailed" // the production containment: jailed runner, scratchpad as workspace
	shellBash   = "bash"   // unconfined bash, for comparison with earlier runs
)

// evalPolicyPrograms is the allowlist for the eval's shell. It covers the
// tools the task set needs: the Go toolchain, sqlite3, text tools and file
// operations. Every program is path-confined by the analyzer; nothing here
// can read or write outside the session.
var evalPolicyPrograms = []string{
	"go", "gofmt", "sqlite3", "bash", "grep", "sed", "awk", "cat", "ls", "find",
	"mkdir", "cp", "mv", "rm", "touch", "chmod", "wc", "head", "tail", "sort",
	"cut", "diff", "echo", "printf", "tr", "uniq", "date", "test", "true", "false",
}

// evalSearchPath lists where the allowlisted programs are found on this
// host. Homebrew's go lives outside the jail's default search path.
var evalSearchPath = []string{"/opt/homebrew/bin", "/usr/local/bin", "/usr/bin", "/bin", "/sbin"}

// evalPolicy builds the allowlist policy the jailed shell runs under.
func evalPolicy() (*shellpolicy.Policy, error) {
	policies, err := shellpolicy.Build([]shellpolicy.Spec{{
		Name:  "eval",
		Allow: evalPolicyPrograms,
	}})
	if err != nil {
		return nil, fmt.Errorf("eval shell policy: %w", err)
	}
	return policies["eval"], nil
}

// armJail makes every shell_execute tool created afterwards run in the mode
// the run asked for. It must be called before the agent's tools are built,
// since the tool reads the runner when it is constructed. The jailed runner is
// rooted at dataDir, so the session's scratchpad (the workspace) lives under
// the run's own directory.
func armJail(ctx context.Context, mode, dataDir string) error {
	if mode == shellBash {
		builtin.SetShellJail(nil)
		return nil
	}
	if mode != shellJailed {
		return fmt.Errorf("unknown shell mode %q", mode)
	}
	pol, err := evalPolicy()
	if err != nil {
		return err
	}
	runner, _, err := shelljail.NewRunner(shelljail.Config{
		Policy:      pol,
		SearchPath:  evalSearchPath,
		LoomDataDir: dataDir,
		Limits:      shelljail.Limits{MemoryBytes: shelljail.DefaultMemoryBytes, FileBytes: shelljail.DefaultFileBytes},
	})
	if err != nil {
		return fmt.Errorf("jailed shell: %w", err)
	}
	selfCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := runner.SelfTest(selfCtx); err != nil {
		return fmt.Errorf("jailed shell self-test: %w", err)
	}
	builtin.SetShellJail(runner)
	return nil
}
