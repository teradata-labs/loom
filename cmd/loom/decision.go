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
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/internal/sqlitedriver"
	"github.com/teradata-labs/loom/pkg/decision"
	"github.com/teradata-labs/loom/pkg/decision/jev"
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
	decisionDBPath      string
	decisionSite        string
	decisionLimit       int
	decisionSince       string
	decisionDecider     string
	decisionProvider    string
	decisionModel       string
	decisionDryRun      bool
	decisionErrors      bool
	decisionConcurrency int
	decisionSample      string
	decisionBaseURL     string
	decisionSeed        int64
	decisionRPM         float64
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
	decisionReplayCmd.Flags().StringVar(&decisionDecider, "decider", "llm", "Decider to shadow: jev | llm | mock (jev reads TYPESAFE_API_KEY or AI_GATEWAY_API_KEY)")
	decisionReplayCmd.Flags().StringVar(&decisionBaseURL, "base-url", "", "Endpoint base URL for --decider jev (default: TypeSafe direct, or the Vercel AI Gateway when only AI_GATEWAY_API_KEY is set)")
	decisionReplayCmd.Flags().StringVar(&decisionProvider, "provider", "", "LLM provider for --decider llm: anthropic | bedrock | azure-openai | openai | gemini | mistral | ollama | litellm (default: $LOOM_LLM_PROVIDER or anthropic; credentials from the usual env vars)")
	decisionReplayCmd.Flags().StringVar(&decisionModel, "model", "", "LLM model for --decider llm (default: provider default)")
	decisionReplayCmd.Flags().BoolVar(&decisionErrors, "errors-only", false, "Replay only executions that recorded an error")
	decisionReplayCmd.Flags().BoolVar(&decisionDryRun, "dry-run", false, "Build requests and references but call no decider and write nothing")
	decisionReplayCmd.Flags().IntVar(&decisionConcurrency, "concurrency", 4, "Decider calls in flight at once (1 = sequential)")
	decisionReplayCmd.Flags().StringVar(&decisionSample, "sample", sampleNewest, "Which executions to replay: newest | random (random reaches across the whole history)")
	decisionReplayCmd.Flags().Int64Var(&decisionSeed, "seed", 0, "With --sample random: a deterministic order so two deciders score the identical rows (0 = truly random)")
	decisionReplayCmd.Flags().Float64Var(&decisionRPM, "rpm", 0, "For --decider jev: cap requests per minute (0 = client default; the Vercel AI Gateway free tier allows 30)")

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

	rows, err := loadToolExecutions(ctx, db, decisionLimit, decisionErrors, decisionSample, decisionSeed)
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

	workers := decisionConcurrency
	if workers < 1 || decisionDryRun {
		workers = 1
	}
	deciderName := ""
	if decider != nil {
		deciderName = decider.Name()
	}

	// Workers evaluate; the main goroutine is the single writer to the store
	// and the only one printing progress. Results arrive in completion order,
	// which is fine: rows are independent and the report does not care.
	jobs := make(chan toolExecutionRow)
	results := make(chan replayResult)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for row := range jobs {
				results <- replayOne(ctx, router, deciderName, row, decisionDryRun)
			}
		}()
	}
	go func() {
		for _, row := range rows {
			jobs <- row
		}
		close(jobs)
		wg.Wait()
		close(results)
	}()

	var written, errored, skipped, done int
	// Distinct decider errors, so a failing run says why instead of only how
	// often. Bounded: a broken key produces one message thousands of times.
	const maxDistinctErrors = 3
	distinctErrors := map[string]int{}
	start := time.Now()
	for res := range results {
		done++
		switch {
		case res.skipped:
			skipped++
		case decisionDryRun:
			written += len(res.records)
		default:
			if res.errored {
				errored++
				if _, seen := distinctErrors[res.errMsg]; seen || len(distinctErrors) < maxDistinctErrors {
					distinctErrors[res.errMsg]++
				}
			}
			if err := recorder.Record(ctx, res.records); err != nil {
				return fmt.Errorf("record shadow rows: %w", err)
			}
			written += len(res.records)
		}
		if done%50 == 0 {
			emitf(cmd.OutOrStdout(), "  %d/%d executions, %d rows, %d decider errors, %s elapsed\n",
				done, len(rows), written, errored, time.Since(start).Round(time.Second))
		}
	}
	emitf(cmd.OutOrStdout(), "done: %d executions, %d shadow rows written, %d decider errors, %d skipped, %d workers, %s\n",
		len(rows), written, errored, skipped, workers, time.Since(start).Round(time.Millisecond))
	if len(distinctErrors) > 0 {
		msgs := make([]string, 0, len(distinctErrors))
		for m := range distinctErrors {
			msgs = append(msgs, m)
		}
		sort.Strings(msgs)
		emit(cmd.OutOrStdout(), "decider errors (distinct, first seen):\n")
		for _, m := range msgs {
			emitf(cmd.OutOrStdout(), "  %dx %s\n", distinctErrors[m], m)
		}
	}
	if !decisionDryRun {
		emitf(cmd.OutOrStdout(), "next: loom decision report --site %s --db %s\n", sites.SiteFailureKind, path)
	}
	return nil
}

// replayResult is one execution's outcome from replayOne.
type replayResult struct {
	records []*loomv1.DecisionShadowRecord
	skipped bool
	errored bool
	errMsg  string
}

// replayOne builds the failure-kind request and reference for one recorded
// execution and, unless dry-running, asks the router. It never returns an
// error: a decider failure becomes ERROR-path rows, a malformed row is
// skipped.
func replayOne(ctx context.Context, router *decision.Router, deciderName string, row toolExecutionRow, dryRun bool) (res replayResult) {
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
		res.skipped = true
		return res
	}
	refs := sites.FailureKindReference(success, "", errText)
	if dryRun {
		// Nothing is written on a dry run; the count stands in for rows.
		res.records = make([]*loomv1.DecisionShadowRecord, len(refs))
		return res
	}
	out := router.Decide(decision.WithSessionID(ctx, row.sessionID), req)
	if out.Err != nil {
		res.errored = true
		res.errMsg = out.Err.Error()
	}
	res.records = decision.BuildShadowRecords(req, out, deciderName, row.sessionID, refs)
	return res
}

// Sampling orders for loadToolExecutions.
const (
	sampleNewest = "newest"
	sampleRandom = "random"
)

func loadToolExecutions(ctx context.Context, db *sql.DB, limit int, errorsOnly bool, sample string, seed int64) ([]toolExecutionRow, error) {
	if limit <= 0 {
		limit = 1000
	}
	where := ""
	if errorsOnly {
		where = "WHERE error IS NOT NULL AND error <> ''"
	}
	// Newest-first reads the tail of whatever campaign ran last, which can be
	// one failure mode repeated thousands of times. Random reaches across the
	// whole history and is what a class-balanced report wants. A seed makes
	// the random order deterministic (a multiplicative hash of the row id),
	// so two deciders can be scored on the identical rows.
	order := "ORDER BY timestamp DESC, id DESC"
	args := []any{}
	switch strings.ToLower(sample) {
	case "", sampleNewest:
	case sampleRandom:
		if seed > 0 {
			order = "ORDER BY ((id * 2654435761) + ?) % 4294967296, id"
			args = append(args, seed)
		} else {
			order = "ORDER BY RANDOM()"
		}
	default:
		return nil, fmt.Errorf("--sample %q: must be newest or random", sample)
	}
	args = append(args, limit)
	rows, err := db.QueryContext(ctx, `
		SELECT session_id, tool_name, input_json, error, timestamp
		FROM tool_executions `+where+`
		`+order+`
		LIMIT ?`, args...)
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
	case "jev":
		cfg, err := jev.FromDecisionConfig(&loomv1.DecisionConfig{BaseUrl: decisionBaseURL, Model: decisionModel})
		if err != nil {
			return nil, err
		}
		if decisionRPM > 0 {
			cfg.RequestsPerMinute = decisionRPM
		}
		client, err := jev.New(cfg)
		if err != nil {
			return nil, err
		}
		return client, nil
	case "mock-error":
		// A decider that always fails, for exercising the error summary.
		return decisionmock.New().SetError(decision.ErrUnauthorized), nil
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
			DefaultProvider:         provider,
			DefaultModel:            decisionModel,
			AnthropicAPIKey:         os.Getenv("ANTHROPIC_API_KEY"),
			OpenAIAPIKey:            os.Getenv("OPENAI_API_KEY"),
			GeminiAPIKey:            os.Getenv("GEMINI_API_KEY"),
			MistralAPIKey:           os.Getenv("MISTRAL_API_KEY"),
			HuggingFaceToken:        os.Getenv("HF_TOKEN"),
			OllamaEndpoint:          os.Getenv("OLLAMA_ENDPOINT"),
			LiteLLMEndpoint:         os.Getenv("LITELLM_ENDPOINT"),
			LiteLLMAPIKey:           os.Getenv("LITELLM_API_KEY"),
			BedrockRegion:           os.Getenv("AWS_REGION"),
			BedrockProfile:          os.Getenv("AWS_PROFILE"),
			AzureOpenAIEndpoint:     os.Getenv("AZURE_OPENAI_ENDPOINT"),
			AzureOpenAIDeploymentID: os.Getenv("AZURE_OPENAI_DEPLOYMENT"),
			AzureOpenAIAPIKey:       os.Getenv("AZURE_OPENAI_API_KEY"),
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
		return nil, fmt.Errorf("--decider %q: must be jev, llm or mock", decisionDecider)
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
