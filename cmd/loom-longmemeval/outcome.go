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

//go:build fts5

package main

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"google.golang.org/grpc/codes"
)

// Process exit statuses of `loom-longmemeval run`.
//
// The AKS slice loop (deploy/longmemeval/runner-script.yaml) keys its retry
// budget off these, so their values are a contract: runner-script.yaml sets
// HARNESS_RC_TRANSIENT to ExitTransient, and TestRunnerScriptTransientExitCode
// pins that the two agree.
const (
	// ExitOK: every entry completed, or the only failures are not
	// retryable (the caller validates the per-entry results).
	ExitOK = 0
	// ExitError: the run could not start, or was aborted (e.g. the server
	// rejects occurred_at).
	ExitError = 1
	// ExitTransient: the run finished, at least one entry failed, and EVERY
	// failure carries a retryable gRPC status (see retryableCodes). Nothing
	// in the results says the failing entries are broken, so a retry is the
	// right response. Value 75 is EX_TEMPFAIL from sysexits.h.
	ExitTransient = 75
)

// retryableCodes are the gRPC statuses that describe the infrastructure
// rather than the entry: the server or its transport is down or restarting
// (Unavailable), the per-call deadline elapsed (DeadlineExceeded), or the
// server shed load (ResourceExhausted, the conversation-door backpressure
// code).
//
// The LLM provider's capacity failures land here too. When a turn fails
// because the provider throttled it or had a temporary server fault past
// looms' own retries, looms returns ResourceExhausted (throttle) or
// Unavailable (5xx/overloaded, or a provider stream timeout) instead of
// Internal (wrapAgentError in pkg/server). A deterministic provider refusal
// stays Internal and is reported as an ordinary failure.
var retryableCodes = map[codes.Code]bool{
	codes.Unavailable:       true,
	codes.DeadlineExceeded:  true,
	codes.ResourceExhausted: true,
}

// TransientFailureError reports a finished run whose failed entries all
// carry a retryable gRPC status. The CLI maps it to ExitTransient.
type TransientFailureError struct {
	Failed int                // entries with a non-empty Error
	Total  int                // entries that produced a result
	Codes  map[codes.Code]int // failures per retryable status
}

// Error summarizes the failures by status code in a stable order.
func (e *TransientFailureError) Error() string {
	parts := make([]string, 0, len(e.Codes))
	for c, n := range e.Codes {
		parts = append(parts, fmt.Sprintf("%s=%d", c, n))
	}
	sort.Strings(parts)
	return fmt.Sprintf("%d of %d entries failed, all with retryable gRPC statuses (%s); retry the run",
		e.Failed, e.Total, strings.Join(parts, ", "))
}

// classifyOutcome turns a finished run into the CLI's result. runErr is the
// error Runner.Run returned.
//
//   - A run aborted by Runner.Run (anything but a user interrupt) is an
//     error, exactly as before.
//   - A run whose failed entries ALL carry a retryable status returns a
//     *TransientFailureError, so a caller can retry without treating the
//     entries as broken.
//   - Everything else — success, a user interrupt, or any failure without a
//     retryable status (one deterministic failure is enough) — is nil: the
//     caller judges the per-entry results, as it always has.
func classifyOutcome(results []EntryResult, runErr error) error {
	if runErr != nil && !errors.Is(runErr, context.Canceled) {
		return fmt.Errorf("benchmark run aborted: %w", runErr)
	}

	failed := 0
	byCode := make(map[codes.Code]int)
	for _, r := range results {
		if r.Error == "" {
			continue
		}
		failed++
		if !retryableCodes[r.grpcCode] {
			return nil
		}
		byCode[r.grpcCode]++
	}
	if failed == 0 {
		return nil
	}
	return &TransientFailureError{Failed: failed, Total: len(results), Codes: byCode}
}

// exitCodeFor maps the error returned by the root command to a process exit
// status.
func exitCodeFor(err error) int {
	if err == nil {
		return ExitOK
	}
	var transient *TransientFailureError
	if errors.As(err, &transient) {
		return ExitTransient
	}
	return ExitError
}
