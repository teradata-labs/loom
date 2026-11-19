package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/artifacts"
	"github.com/teradata-labs/loom/pkg/llm/vercelgateway"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/shuttle/builtin"
)

// arm is one gateway arm. Only the slug varies between arms.
type arm struct {
	ID   string
	Slug string
}

var arms = map[string]arm{
	"G-S": {ID: "G-S", Slug: "anthropic/claude-sonnet-5.5"},
	"G-O": {ID: "G-O", Slug: "openai/gpt-5.5"},
}

// systemPrompt is shared by every arm and task. It describes the working
// directory and nothing else, so the arms differ only in the model.
const systemPrompt = "Work in the project directory. Paths in tool calls are relative to its root. " +
	"Stay inside the project directory: do not read or search files outside it. " +
	"Use the tools to read and change files and to run commands. " +
	"When the task asks for an answer, give it in the final message."

// turnRecord is one user turn and the assistant reply to it.
type turnRecord struct {
	User      string  `json:"user"`
	Assistant string  `json:"assistant"`
	LLMCalls  int     `json:"llm_calls"`
	CostUSD   float64 `json:"cost_usd"`
	Error     string  `json:"error,omitempty"`
}

// toolRecord is one tool execution as the agent reported it.
type toolRecord struct {
	Turn  int                    `json:"turn"`
	Name  string                 `json:"name"`
	Input map[string]interface{} `json:"input,omitempty"`
	Error string                 `json:"error,omitempty"`
}

// runRecord is the raw result of one task run. It is written as JSON and
// never overwritten.
type runRecord struct {
	TaskID       string        `json:"task_id"`
	Arm          string        `json:"arm"`
	Model        string        `json:"model"`
	Rep          int           `json:"rep"`
	StartedAt    time.Time     `json:"started_at"`
	WallMS       int64         `json:"wall_ms"`
	Shell        string        `json:"shell"`
	Workspace    string        `json:"workspace"`
	Turns        []turnRecord  `json:"turns"`
	ToolCalls    []toolRecord  `json:"tool_calls"`
	InputTokens  int           `json:"input_tokens"`
	OutputTokens int           `json:"output_tokens"`
	CacheRead    int           `json:"cache_read_tokens"`
	CacheWrite   int           `json:"cache_write_tokens"`
	CostUSD      float64       `json:"cost_usd"`
	Checks       []checkResult `json:"checks"`
	Pass         bool          `json:"pass"`
	Error        string        `json:"error,omitempty"`
}

// runTasksCmd runs the task set against the chosen arms and repetitions.
func runTasksCmd(args []string) error {
	fs := flag.NewFlagSet("tasks", flag.ContinueOnError)
	out := fs.String("out", "", "results directory (required)")
	armList := fs.String("arm", "G-S,G-O", "comma-separated arm ids")
	reps := fs.Int("reps", 3, "repetitions per task per arm")
	only := fs.String("only", "", "comma-separated task ids (default all)")
	budget := fs.Float64("budget", 100, "whole-run spend ceiling in USD; the run stops when it is reached")
	taskTimeout := fs.Duration("task-timeout", 5*time.Minute, "per task run")
	shell := fs.String("shell", shellJailed, "shell_execute mode: jailed (production containment) or bash (unconfined, earlier runs)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		return fmt.Errorf("-out is required")
	}

	selected := selectTasks(*only)
	if len(selected) == 0 {
		return fmt.Errorf("no tasks match -only %q", *only)
	}
	if *shell != shellJailed && *shell != shellBash {
		return fmt.Errorf("-shell must be %q or %q", shellJailed, shellBash)
	}
	if err := sandboxHome(*out); err != nil {
		return err
	}

	var spent float64
	for _, armID := range strings.Split(*armList, ",") {
		a, ok := arms[strings.TrimSpace(armID)]
		if !ok {
			return fmt.Errorf("unknown arm %q", armID)
		}
		for _, t := range selected {
			for rep := 1; rep <= *reps; rep++ {
				if spent >= *budget {
					fmt.Printf("budget ceiling $%.2f reached at $%.4f; stopping\n", *budget, spent)
					return nil
				}
				rec, err := runOne(context.Background(), *out, a, t, rep, *taskTimeout, *shell)
				if err != nil {
					return fmt.Errorf("%s %s rep %d: %w", a.ID, t.ID, rep, err)
				}
				spent += rec.CostUSD
				status := "FAIL"
				if rec.Pass {
					status = "PASS"
				}
				fmt.Printf("%s %-4s rep%d %s cost=$%.5f wall=%dms spent=$%.4f", a.ID, t.ID, rep, status, rec.CostUSD, rec.WallMS, spent)
				if rec.Error != "" {
					fmt.Printf(" error=%q", rec.Error)
				}
				fmt.Println()
			}
		}
	}
	fmt.Printf("done. spent $%.4f of $%.2f\n", spent, *budget)
	return nil
}

// runSession is the session id a run uses. Its characters are valid for the
// artifact store's session path rules.
func runSession(a arm, t task, rep int) string {
	return fmt.Sprintf("%s-%s-rep%d", a.ID, t.ID, rep)
}

func selectTasks(only string) []task {
	if strings.TrimSpace(only) == "" {
		return allTasks()
	}
	want := map[string]bool{}
	for _, id := range strings.Split(only, ",") {
		want[strings.TrimSpace(id)] = true
	}
	var out []task
	for _, t := range allTasks() {
		if want[t.ID] {
			out = append(out, t)
		}
	}
	return out
}

// runOne runs one task once for one arm. The workspace is a new directory:
// if it already exists the run is refused, so raw results are never replaced.
func runOne(parent context.Context, outDir string, a arm, t task, rep int, timeout time.Duration, shell string) (*runRecord, error) {
	runDir := filepath.Join(outDir, "tasks", a.ID, t.ID, fmt.Sprintf("rep%d", rep))
	orig := filepath.Join(runDir, "fixture")
	dataDir := filepath.Join(runDir, "data")
	if _, err := os.Stat(runDir); err == nil {
		return nil, fmt.Errorf("run directory already exists, refusing to overwrite: %s", runDir)
	}

	// Jailed runs use the session scratchpad as the workspace: the jail's
	// working directory and its only writable root are that directory, so the
	// file tools, the shell and the checkers all see the same files. The run
	// gets its own data directory, so the scratchpad is inside the run.
	session := runSession(a, t, rep)
	var ws string
	switch shell {
	case shellJailed:
		if err := os.Setenv("LOOM_DATA_DIR", dataDir); err != nil {
			return nil, err
		}
		sp, err := artifacts.GetScratchpadDir(session)
		if err != nil {
			return nil, fmt.Errorf("scratchpad: %w", err)
		}
		ws = sp
	case shellBash:
		ws = filepath.Join(runDir, "workspace")
	default:
		return nil, fmt.Errorf("unknown shell mode %q", shell)
	}
	if err := writeFiles(ws, t.Files); err != nil {
		return nil, err
	}
	if err := writeFiles(orig, t.Files); err != nil {
		return nil, err
	}

	rec := &runRecord{TaskID: t.ID, Arm: a.ID, Model: a.Slug, Rep: rep, StartedAt: time.Now(), Shell: shell, Workspace: ws}

	for _, script := range t.Setup {
		if ok, detail := runShell(ws, script); !ok {
			rec.Error = "setup failed: " + detail
			return finish(runDir, rec, nil)
		}
	}

	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	client, err := vercelgateway.NewClient(vercelgateway.Config{
		Model:             a.Slug,
		APIKey:            vercelgateway.ResolveAPIKey(""),
		MaxTokens:         4096,
		RateLimiterConfig: rateLimiterFor(),
	})
	if err != nil {
		rec.Error = "client: " + err.Error()
		return finish(runDir, rec, nil)
	}

	// Bash mode: the shell tool starts in LOOM_SANDBOX_DIR, not in its base
	// directory, so the sandbox is pointed at this run's workspace. Runs are
	// sequential, so setting it per run cannot leak between runs.
	if shell == shellBash {
		if err := os.Setenv("LOOM_SANDBOX_DIR", ws); err != nil {
			return nil, err
		}
	}
	// The jail must be armed before the shell tool is built.
	if err := armJail(ctx, shell, dataDir); err != nil {
		rec.Error = "shell: " + err.Error()
		return finish(runDir, rec, nil)
	}

	ag := agent.NewAgent(noBackend{}, client, agent.WithSystemPrompt(systemPrompt))
	ag.RegisterTools(
		builtin.NewFileReadTool(ws),
		builtin.NewFileWriteTool(ws),
		builtin.NewEditFilesTool(ws),
		builtin.NewShellExecuteTool(ws),
	)

	for i, user := range t.Turns {
		tr := turnRecord{User: user}
		resp, err := ag.Chat(ctx, session, user)
		if err != nil {
			tr.Error = err.Error()
			rec.Turns = append(rec.Turns, tr)
			rec.Error = fmt.Sprintf("turn %d: %v", i+1, err)
			break
		}
		tr.Assistant = resp.Content
		tr.LLMCalls = resp.TurnLLMCalls
		tr.CostUSD = resp.TurnUsage.CostUSD
		rec.Turns = append(rec.Turns, tr)
		rec.InputTokens += resp.TurnUsage.InputTokens
		rec.OutputTokens += resp.TurnUsage.OutputTokens
		rec.CacheRead += resp.TurnUsage.CacheReadInputTokens
		rec.CacheWrite += resp.TurnUsage.CacheCreationInputTokens
		rec.CostUSD += resp.TurnUsage.CostUSD
		for _, ex := range resp.ToolExecutions {
			rec.ToolCalls = append(rec.ToolCalls, toolRecordOf(i+1, ex))
		}
	}
	return finish(runDir, rec, &t)
}

func toolRecordOf(turn int, ex agent.ToolExecution) toolRecord {
	tr := toolRecord{Turn: turn, Name: ex.ToolName, Input: ex.Input}
	if ex.Error != nil {
		tr.Error = ex.Error.Error()
	} else if ex.Result != nil && !ex.Result.Success {
		tr.Error = describeResultError(ex.Result)
	}
	return tr
}

// describeResultError returns the tool's own error text when it reports one.
func describeResultError(r *shuttle.Result) string {
	if r.Error != nil {
		return r.Error.Message
	}
	return "tool reported failure"
}

// finish evaluates the checks, writes the record and returns it.
func finish(runDir string, rec *runRecord, t *task) (*runRecord, error) {
	rec.WallMS = time.Since(rec.StartedAt).Milliseconds()
	if t != nil && rec.Error == "" {
		rec.Checks = evaluateChecks(runDir, t, rec)
		rec.Pass = allPassed(rec.Checks)
	}
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return nil, err
	}
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(runDir, "result.json"), b, 0o644); err != nil {
		return nil, err
	}
	return rec, nil
}

// allPassed reports whether every in-run check passed. Deferred checks are
// excluded here and decided by the judge pass.
func allPassed(cs []checkResult) bool {
	n := 0
	for _, c := range cs {
		if c.Deferred {
			continue
		}
		n++
		if !c.Pass {
			return false
		}
	}
	return n > 0
}

// writeFiles creates each file under dir. It refuses to overwrite.
func writeFiles(dir string, files map[string]string) error {
	for rel, content := range files {
		p := filepath.Join(dir, rel)
		if _, err := os.Stat(p); err == nil {
			return fmt.Errorf("refusing to overwrite %s", p)
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}
