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
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/teradata-labs/loom/internal/sqlitedriver"
	"github.com/teradata-labs/loom/pkg/decision"
	decisionllm "github.com/teradata-labs/loom/pkg/decision/llm"
	decisionmock "github.com/teradata-labs/loom/pkg/decision/mock"
	"github.com/teradata-labs/loom/pkg/decision/report"
	"github.com/teradata-labs/loom/pkg/decision/sites"
	"github.com/teradata-labs/loom/pkg/llm/factory"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/storage"
	"github.com/teradata-labs/loom/pkg/storage/sqlite"
	"github.com/teradata-labs/loom/pkg/types"
)

var decisionCmd = &cobra.Command{
	Use:   "decision",
	Short: "Replay and report typed-decision shadow comparisons",
	Long: `Shadow-evaluate the typed decision layer (pkg/decision) against Loom's own
telemetry, and report how well a decider agrees with what Loom decides today.

  replay  runs recorded tool executions through the failure-kind classifier and
          stores one shadow row per question, comparing the decider's answer
          with the Success flag plus fabric.InferErrorType.
  report  reads shadow rows for a site and prints agreement, expected
          calibration error, confusion and latency percentiles.

Agreement is with the existing mechanism, not with ground truth. The report is
the evidence for a site's confidence band; nothing branches on a decider until
a band is configured.`,
}

var (
	decisionDBPath   string
	decisionSite     string
	decisionLimit    int
	decisionSince    string
	decisionDecider  string
	decisionProvider string
	decisionModel    string
	decisionDryRun   bool
	decisionErrors   bool
)

var decisionReplayCmd = &cobra.Command{
	Use:   "replay",
	Short: "Run recorded tool executions through the failure-kind classifier",
	RunE:  runDecisionReplay,
}

var decisionReportCmd = &cobra.Command{
	Use:   "report",
	Short: "Summarize shadow rows for a site",
	RunE:  runDecisionReport,
}

func init() {
	decisionCmd.PersistentFlags().StringVar(&decisionDBPath, "db", "", "Path to loom.db (default: $LOOM_DATA_DIR/loom.db)")

	decisionReplayCmd.Flags().IntVar(&decisionLimit, "limit", 1000, "Maximum tool executions to replay, newest first")
	decisionReplayCmd.Flags().StringVar(&decisionDecider, "decider", "llm", "Decider to shadow: llm | mock")
	decisionReplayCmd.Flags().StringVar(&decisionProvider, "provider", "", "LLM provider for --decider llm (default: $LOOM_LLM_PROVIDER or anthropic)")
	decisionReplayCmd.Flags().StringVar(&decisionModel, "model", "", "LLM model for --decider llm (default: provider default)")
	decisionReplayCmd.Flags().BoolVar(&decisionErrors, "errors-only", false, "Replay only executions that recorded an error")
	decisionReplayCmd.Flags().BoolVar(&decisionDryRun, "dry-run", false, "Build requests and references but call no decider and write nothing")

	decisionReportCmd.Flags().StringVar(&decisionSite, "site", sites.SiteFailureKind, "Site to report on (empty = all sites)")
	decisionReportCmd.Flags().IntVar(&decisionLimit, "limit", 0, "Maximum shadow rows to read, newest first (0 = store default)")
	decisionReportCmd.Flags().StringVar(&decisionSince, "since", "", "Only rows recorded after this duration ago (e.g. 24h, 7d) or RFC3339 time")

	decisionCmd.AddCommand(decisionReplayCmd)
	decisionCmd.AddCommand(decisionReportCmd)
}

// toolExecutionRow is the slice of tool_executions the replay needs. Result
// payloads are deliberately not read: the classifier's state carries only
// the error text and an input digest.
type toolExecutionRow struct {
	sessionID string
	toolName  string
	inputJSON sql.NullString
	errorText sql.NullString
	timestamp int64
}

func openDecisionDB() (*sql.DB, string, error) {
	path := decisionDBPath
	if path == "" {
		path = storage.GetDefaultLoomDBPath()
	}
	if _, err := os.Stat(path); err != nil {
		return nil, path, fmt.Errorf("database %s: %w", path, err)
	}
	dsn := sqlitedriver.DSN(path, sqlitedriver.Options{BusyTimeoutMS: 5000, WAL: true, ForeignKeys: true})
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, path, fmt.Errorf("open %s: %w", path, err)
	}
	return db, path, nil
}

func runDecisionReplay(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	db, path, err := openDecisionDB()
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	tracer := observability.NewNoOpTracer()
	// The shadow table must exist; MigrateUp is idempotent and refuses to
	// run against a newer schema, so this is safe on a live database.
	migrator, err := sqlite.NewMigrator(db, tracer)
	if err != nil {
		return fmt.Errorf("migrator: %w", err)
	}
	if !decisionDryRun {
		if err := migrator.MigrateUp(ctx); err != nil {
			return fmt.Errorf("migrate %s: %w", path, err)
		}
	}

	rows, err := loadToolExecutions(ctx, db, decisionLimit, decisionErrors)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		emitf(cmd.OutOrStdout(), "no tool executions found in %s\n", path)
		return nil
	}

	var decider decision.Decider
	if !decisionDryRun {
		decider, err = buildReplayDecider(ctx)
		if err != nil {
			return err
		}
	}
	router := decision.NewRouter(decision.NewInstrumented(decider, tracer), decision.WithTracer(tracer))
	store := sqlite.NewDecisionShadowStore(db, tracer)
	recorder := decision.NewShadowRecorder(store, tracer)

	emitf(cmd.OutOrStdout(), "replaying %d tool executions from %s through %s", len(rows), path, sites.SiteFailureKind)
	if decisionDryRun {
		emit(cmd.OutOrStdout(), " (dry run)")
	} else {
		emitf(cmd.OutOrStdout(), " with %s", decider.Name())
	}
	emit(cmd.OutOrStdout(), "\n")

	var written, errored, skipped int
	start := time.Now()
	for i, row := range rows {
		input := map[string]any{}
		if row.inputJSON.Valid && row.inputJSON.String != "" {
			_ = json.Unmarshal([]byte(row.inputJSON.String), &input)
		}
		errText := ""
		if row.errorText.Valid {
			errText = row.errorText.String
		}
		success := errText == ""
		req, err := sites.FailureKindRequest(row.toolName, "", errText, input)
		if err != nil {
			skipped++
			continue
		}
		refs := sites.FailureKindReference(success, "", errText)
		if decisionDryRun {
			written += len(refs)
			continue
		}
		out := router.Decide(decision.WithSessionID(ctx, row.sessionID), req)
		if out.Err != nil {
			errored++
		}
		records := decision.BuildShadowRecords(req, out, decider.Name(), row.sessionID, refs)
		if err := recorder.Record(ctx, records); err != nil {
			return fmt.Errorf("record shadow rows: %w", err)
		}
		written += len(records)
		if (i+1)%50 == 0 {
			emitf(cmd.OutOrStdout(), "  %d/%d executions, %d rows, %d decider errors, %s elapsed\n",
				i+1, len(rows), written, errored, time.Since(start).Round(time.Second))
		}
	}
	emitf(cmd.OutOrStdout(), "done: %d executions, %d shadow rows written, %d decider errors, %d skipped, %s\n",
		len(rows), written, errored, skipped, time.Since(start).Round(time.Millisecond))
	if !decisionDryRun {
		emitf(cmd.OutOrStdout(), "next: loom decision report --site %s --db %s\n", sites.SiteFailureKind, path)
	}
	return nil
}

func loadToolExecutions(ctx context.Context, db *sql.DB, limit int, errorsOnly bool) ([]toolExecutionRow, error) {
	if limit <= 0 {
		limit = 1000
	}
	where := ""
	if errorsOnly {
		where = "WHERE error IS NOT NULL AND error <> ''"
	}
	rows, err := db.QueryContext(ctx, `
		SELECT session_id, tool_name, input_json, error, timestamp
		FROM tool_executions `+where+`
		ORDER BY timestamp DESC, id DESC
		LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("read tool_executions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []toolExecutionRow
	for rows.Next() {
		var r toolExecutionRow
		if err := rows.Scan(&r.sessionID, &r.toolName, &r.inputJSON, &r.errorText, &r.timestamp); err != nil {
			return nil, fmt.Errorf("scan tool_executions: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// buildReplayDecider resolves --decider. "mock" scripts a fixed answer so the
// pipeline can be exercised without any provider; "llm" adapts a provider
// built from the environment the way the agent CLI does.
func buildReplayDecider(_ context.Context) (decision.Decider, error) {
	switch strings.ToLower(decisionDecider) {
	case "mock":
		m := decisionmock.New().
			AnswerChoice(sites.QFailureKind, map[string]float64{
				sites.KindNotAFailure: 1, sites.KindTransient: 0, sites.KindServerSaturated: 0,
				sites.KindAuth: 0, sites.KindBadInput: 0, sites.KindNotFound: 0, sites.KindOther: 0,
			}).
			AnswerNoul(sites.QRetryHelps, 0.1)
		return m, nil
	case "llm":
		provider := decisionProvider
		if provider == "" {
			provider = os.Getenv("LOOM_LLM_PROVIDER")
		}
		if provider == "" {
			provider = "anthropic"
		}
		f := factory.NewProviderFactory(factory.FactoryConfig{
			DefaultProvider:  provider,
			DefaultModel:     decisionModel,
			AnthropicAPIKey:  os.Getenv("ANTHROPIC_API_KEY"),
			OpenAIAPIKey:     os.Getenv("OPENAI_API_KEY"),
			GeminiAPIKey:     os.Getenv("GEMINI_API_KEY"),
			MistralAPIKey:    os.Getenv("MISTRAL_API_KEY"),
			HuggingFaceToken: os.Getenv("HF_TOKEN"),
			OllamaEndpoint:   os.Getenv("OLLAMA_ENDPOINT"),
			LiteLLMEndpoint:  os.Getenv("LITELLM_ENDPOINT"),
			LiteLLMAPIKey:    os.Getenv("LITELLM_API_KEY"),
			BedrockRegion:    os.Getenv("AWS_REGION"),
		})
		raw, err := f.CreateProvider(provider, decisionModel)
		if err != nil {
			return nil, fmt.Errorf("create %s provider: %w", provider, err)
		}
		llmProvider, ok := raw.(types.LLMProvider)
		if !ok {
			return nil, fmt.Errorf("provider %s does not implement the LLM provider interface", provider)
		}
		return decisionllm.New(llmProvider), nil
	default:
		return nil, fmt.Errorf("--decider %q: must be llm or mock", decisionDecider)
	}
}

func runDecisionReport(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	db, _, err := openDecisionDB()
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	q := decision.ShadowQuery{Site: decisionSite, Limit: decisionLimit}
	if decisionSince != "" {
		since, err := parseSince(decisionSince)
		if err != nil {
			return err
		}
		q.Since = since
	}
	store := sqlite.NewDecisionShadowStore(db, observability.NewNoOpTracer())
	rows, err := store.QueryShadow(ctx, q)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		emitf(cmd.OutOrStdout(), "no shadow rows for site %q; run `loom decision replay` or enable a decision provider on an agent\n", decisionSite)
		return nil
	}
	emit(cmd.OutOrStdout(), report.RenderMarkdown(report.Summarize(decisionSite, rows)))
	return nil
}

// emitf and emit write CLI output; a failed write to stdout is not an error
// the command can act on.
func emitf(w io.Writer, format string, a ...any) { _, _ = fmt.Fprintf(w, format, a...) }
func emit(w io.Writer, s string)                 { _, _ = fmt.Fprint(w, s) }

// parseSince accepts a Go duration, a "Nd" day count, or an RFC3339 time.
func parseSince(s string) (time.Time, error) {
	if strings.HasSuffix(s, "d") {
		var days int
		if _, err := fmt.Sscanf(s, "%dd", &days); err == nil && days > 0 {
			return time.Now().Add(-time.Duration(days) * 24 * time.Hour), nil
		}
	}
	if d, err := time.ParseDuration(s); err == nil {
		return time.Now().Add(-d), nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("--since %q: use a duration (24h), days (7d), or RFC3339", s)
}
