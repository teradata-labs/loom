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
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/fabric"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/server"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

// bedrockFailure is the error chain the AWS SDK returns for a failed
// bedrockruntime call with the given status and modeled exception.
func bedrockFailure(statusCode int, exception error) error {
	return &smithy.OperationError{
		ServiceID:     "Bedrock Runtime",
		OperationName: "Converse",
		Err: &awshttp.ResponseError{
			ResponseError: &smithyhttp.ResponseError{
				Response: &smithyhttp.Response{Response: &http.Response{StatusCode: statusCode}},
				Err:      exception,
			},
			RequestID: "req-1",
		},
	}
}

// outageProvider is an LLM provider whose every call fails with err, the way
// the Bedrock client surfaces a failure that outlasted its own retries.
type outageProvider struct{ err error }

func (p *outageProvider) Chat(context.Context, []llmtypes.Message, []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	return nil, fmt.Errorf("bedrock converse failed: %w", p.err)
}
func (p *outageProvider) Name() string  { return "outage" }
func (p *outageProvider) Model() string { return "outage-model" }

// nopBackend is the minimal ExecutionBackend the agent requires.
type nopBackend struct{}

func (nopBackend) Name() string { return "nop" }
func (nopBackend) ExecuteQuery(context.Context, string) (*fabric.QueryResult, error) {
	return &fabric.QueryResult{}, nil
}
func (nopBackend) GetSchema(context.Context, string) (*fabric.Schema, error) {
	return &fabric.Schema{}, nil
}
func (nopBackend) GetMetadata(context.Context, string) (map[string]interface{}, error) {
	return map[string]interface{}{}, nil
}
func (nopBackend) ListResources(context.Context, map[string]string) ([]fabric.Resource, error) {
	return nil, nil
}
func (nopBackend) Capabilities() *fabric.Capabilities { return fabric.NewCapabilities() }
func (nopBackend) Close() error                       { return nil }
func (nopBackend) Ping(context.Context) error         { return nil }
func (nopBackend) ExecuteCustomOperation(context.Context, string, map[string]interface{}) (interface{}, error) {
	return nil, nil
}

// startOutageServer serves a real looms MultiAgentServer over an in-memory
// listener, its only agent backed by a provider that always fails with
// providerErr, and returns a client for it.
func startOutageServer(t *testing.T, providerErr error) loomv1.LoomServiceClient {
	t.Helper()
	cfg := agent.DefaultConfig()
	cfg.Retry.InitialDelay = time.Millisecond
	cfg.Retry.MaxDelay = 2 * time.Millisecond
	ag := agent.NewAgent(nopBackend{}, &outageProvider{err: providerErr}, agent.WithConfig(cfg))
	srv := server.NewMultiAgentServer(map[string]*agent.Agent{"lme": ag}, nil)

	lis := bufconn.Listen(1 << 20)
	gs := grpc.NewServer()
	loomv1.RegisterLoomServiceServer(gs, srv)
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return loomv1.NewLoomServiceClient(conn)
}

// A Bedrock throttle or outage that outlasts looms' retries must reach the
// harness as a retryable status, so `run` exits ExitTransient and the slice
// loop does not charge the chunk budget. A deterministic provider refusal
// must still exit OK for validation to judge.
func TestProviderOutageExitsTransient(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode codes.Code
		wantExit int
	}{
		{"Bedrock throttling", bedrockFailure(429, &brtypes.ThrottlingException{Message: aws.String("Too many tokens")}), codes.ResourceExhausted, ExitTransient},
		{"Bedrock service unavailable", bedrockFailure(503, &brtypes.ServiceUnavailableException{Message: aws.String("unavailable")}), codes.Unavailable, ExitTransient},
		{"Bedrock internal server error", bedrockFailure(500, &brtypes.InternalServerException{Message: aws.String("internal")}), codes.Unavailable, ExitTransient},
		{"Bedrock validation error", bedrockFailure(400, &brtypes.ValidationException{Message: aws.String("malformed input")}), codes.Internal, ExitOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &Runner{
				config: RunConfig{Mode: ModeContextStuffing, Concurrency: 1},
				logger: zap.NewNop(),
				client: startOutageServer(t, tt.err),
			}
			entries := []Entry{testEntry("q1")}
			resultCh := make(chan EntryResult, len(entries))
			runErr := r.Run(context.Background(), entries, resultCh)
			close(resultCh)
			require.NoError(t, runErr)

			var results []EntryResult
			for res := range resultCh {
				require.NotEmpty(t, res.Error)
				assert.Equal(t, tt.wantCode, res.grpcCode, res.Error)
				results = append(results, res)
			}
			require.Len(t, results, 1)
			assert.Equal(t, tt.wantExit, exitCodeFor(classifyOutcome(results, runErr)))
		})
	}
}
