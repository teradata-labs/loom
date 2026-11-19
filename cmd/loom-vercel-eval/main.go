// Command loom-vercel-eval runs the Vercel AI Gateway evaluation described in
// docs/plans/vercel-gateway-eval-plan.md. It drives loom's own packages
// (pkg/llm/vercelgateway, pkg/decision/jev) directly.
//
// Subcommands are added phase by phase. Every run writes raw output under
// the results directory given by -out, which is never deleted or overwritten
// by this tool.
package main

import (
	"fmt"
	"os"

	"github.com/teradata-labs/loom/pkg/shelljail"
)

func main() {
	// Must run before anything else: when this process was started as a
	// jailed shell child, it runs the jail and exits here.
	shelljail.Main()
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "jev-probe":
		err = runJevProbe(os.Args[2:])
	case "calibrate":
		err = runCalibrateCmd(os.Args[2:])
	case "tasks":
		err = runTasksCmd(os.Args[2:])
	case "judge":
		err = runJudgeCmd(os.Args[2:])
	case "report":
		err = runReportCmd(os.Args[2:])
	case "routing":
		err = runRoutingCmd(os.Args[2:])
	case "latency":
		err = runLatencyCmd(os.Args[2:])
	case "latency-summary":
		err = runLatencySummaryCmd(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: loom-vercel-eval <command> [flags]

commands:
  jev-probe   call Jev through the Vercel gateway with a pinned and a floating model id
  calibrate   score 24 known-answer items with Jev and apply the calibration gate
  tasks       run the task set against the gateway arms and check each result in code
  judge       score Jev-deferred checks after the arm runs; requires a passed calibration
  report      aggregate every run into a new summary file
  routing     probe routing options (order, only, sort, fallback, zdr) and record gateway metadata
  latency     time streamed calls per arm and prompt size (run alone, no other traffic)
  latency-summary  median, p95 and bootstrap CI from latency.jsonl`)
}
