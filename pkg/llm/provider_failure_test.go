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

package llm

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"testing"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sdkAPIError builds the *anthropic.Error the Anthropic SDK returns: status
// from the HTTP response, error type parsed from the body. Error() reads the
// request and response, so both are populated as the SDK populates them.
func sdkAPIError(t *testing.T, statusCode int, body string) *anthropic.Error {
	t.Helper()
	u, err := url.Parse("https://bedrock-runtime.us-west-2.amazonaws.com/model/m/invoke")
	require.NoError(t, err)
	req := &http.Request{Method: http.MethodPost, URL: u}
	e := &anthropic.Error{
		StatusCode: statusCode,
		Request:    req,
		Response:   &http.Response{StatusCode: statusCode, Request: req},
	}
	require.NoError(t, e.UnmarshalJSON([]byte(body)))
	return e
}

// awsOperationError builds the chain the AWS SDK returns for a failed
// bedrockruntime call: OperationError → awshttp.ResponseError (carries the
// HTTP status) → the modeled service exception.
func awsOperationError(statusCode int, exception error) error {
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

// statusOnlyError is a provider error that exposes only HTTPStatusCode —
// the shape #411's TransientError takes once it carries the method.
type statusOnlyError struct {
	code int
	msg  string
}

func (e *statusOnlyError) Error() string       { return e.msg }
func (e *statusOnlyError) HTTPStatusCode() int { return e.code }

func TestClassifyProviderFailure(t *testing.T) {
	agentWrap := func(err error) error {
		// The agent's wrap chain: retry loop → turn → conversation loop.
		return fmt.Errorf("conversation loop failed: %w",
			fmt.Errorf("LLM call failed: %w",
				fmt.Errorf("llm call failed after 4 attempts: %w", err)))
	}

	tests := []struct {
		name string
		err  error
		want ProviderFailure
	}{
		{"nil", nil, ProviderFailureNone},
		{"plain error", errors.New("tool backend returned no rows"), ProviderFailureNone},

		// Typed throttle.
		{"typed ThrottleError", NewThrottleError(errors.New("API error (status 429)"), 0), ProviderFailureThrottled},
		{"ThrottleError through the agent chain", agentWrap(NewThrottleError(errors.New("slow down"), 0)), ProviderFailureThrottled},
		{"ThrottleError through limiter exhaustion",
			agentWrap(fmt.Errorf("LLM request failed after 6 retries due to throttling: %w", NewThrottleError(errors.New("x"), 0))),
			ProviderFailureThrottled},

		// Anthropic SDK (Bedrock Claude path).
		{"SDK 429", agentWrap(sdkAPIError(t, 429, `{"message":"Too many requests"}`)), ProviderFailureThrottled},
		{"SDK 503 Bedrock ServiceUnavailable body", agentWrap(sdkAPIError(t, 503, `{"message":"Bedrock is unable to process your request."}`)), ProviderFailureUnavailable},
		{"SDK 500", sdkAPIError(t, 500, `{"message":"internal"}`), ProviderFailureUnavailable},
		{"SDK 502", sdkAPIError(t, 502, `<html>bad gateway</html>`), ProviderFailureUnavailable},
		{"SDK 504", sdkAPIError(t, 504, `{}`), ProviderFailureUnavailable},
		{"SDK 529 overloaded", sdkAPIError(t, 529, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`), ProviderFailureUnavailable},
		{"SDK 400 validation", sdkAPIError(t, 400, `{"message":"messages: field required"}`), ProviderFailureNone},
		{"SDK 400 whose body mentions 429 stays deterministic",
			sdkAPIError(t, 400, `{"message":"input length and max_tokens exceed context limit: 180429 + 21000 > 200000, rate limit"}`),
			ProviderFailureNone},
		{"SDK 403", sdkAPIError(t, 403, `{"message":"not authorized"}`), ProviderFailureNone},
		{"SDK mid-stream overloaded_error on a 200", sdkAPIError(t, 200, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`), ProviderFailureUnavailable},
		{"SDK mid-stream rate_limit_error on a 200", sdkAPIError(t, 200, `{"type":"error","error":{"type":"rate_limit_error","message":"slow"}}`), ProviderFailureThrottled},
		{"SDK mid-stream Bedrock throttlingException", sdkAPIError(t, 200, `{"type":"error","error":{"type":"throttlingException","message":"Too many tokens"}}`), ProviderFailureThrottled},
		{"SDK mid-stream Bedrock internalServerException", sdkAPIError(t, 200, `{"type":"error","error":{"type":"internalServerException","message":"boom"}}`), ProviderFailureUnavailable},
		{"SDK mid-stream Bedrock serviceUnavailableException", sdkAPIError(t, 200, `{"type":"error","error":{"type":"serviceUnavailableException","message":"down"}}`), ProviderFailureUnavailable},
		{"SDK mid-stream Bedrock modelStreamErrorException", sdkAPIError(t, 200, `{"type":"error","error":{"type":"modelStreamErrorException","message":"retry"}}`), ProviderFailureUnavailable},
		{"SDK mid-stream validation on a 200 is not capacity", sdkAPIError(t, 200, `{"type":"error","error":{"type":"invalid_request_error","message":"bad"}}`), ProviderFailureNone},

		// AWS SDK (bedrockruntime Converse path).
		{"AWS ServiceUnavailableException 503",
			agentWrap(awsOperationError(503, &brtypes.ServiceUnavailableException{Message: aws.String("unavailable")})),
			ProviderFailureUnavailable},
		{"AWS InternalServerException 500",
			awsOperationError(500, &brtypes.InternalServerException{Message: aws.String("internal")}),
			ProviderFailureUnavailable},
		{"AWS ModelNotReadyException 429 is a provider fault, not a throttle",
			awsOperationError(429, &brtypes.ModelNotReadyException{Message: aws.String("not ready")}),
			ProviderFailureUnavailable},
		{"AWS ThrottlingException 429",
			agentWrap(awsOperationError(429, &brtypes.ThrottlingException{Message: aws.String("Too many requests")})),
			ProviderFailureThrottled},
		{"AWS ValidationException 400 mentioning throttle stays deterministic",
			awsOperationError(400, &brtypes.ValidationException{Message: aws.String("throttle field 429 invalid")}),
			ProviderFailureNone},
		{"AWS ResponseError 503 with an unmodeled error",
			awsOperationError(503, errors.New("upstream connect error")),
			ProviderFailureUnavailable},
		{"AWS generic server-fault APIError",
			&smithy.GenericAPIError{Code: "SomethingNew", Message: "m", Fault: smithy.FaultServer},
			ProviderFailureUnavailable},

		// HTTPStatusCoder: the shared seam for loom-typed provider errors.
		{"HTTPStatusCoder 503", agentWrap(&statusOnlyError{code: 503, msg: "API error (status 503)"}), ProviderFailureUnavailable},
		{"HTTPStatusCoder 529", &statusOnlyError{code: 529, msg: "overloaded"}, ProviderFailureUnavailable},
		{"HTTPStatusCoder 429", &statusOnlyError{code: 429, msg: "busy"}, ProviderFailureThrottled},
		{"HTTPStatusCoder 501 is not transient", &statusOnlyError{code: 501, msg: "not implemented"}, ProviderFailureNone},
		{"HTTPStatusCoder 408 is not transient", &statusOnlyError{code: 408, msg: "throttle"}, ProviderFailureNone},

		// Untyped messages: only throttling falls back to message matching.
		{"untyped AWS ThrottlingException message", agentWrap(errors.New("bedrock SDK invocation failed: ThrottlingException: Too many requests")), ProviderFailureThrottled},
		{"untyped limiter queue timeout", errors.New("rate limiter queue timeout after 5m0s"), ProviderFailureThrottled},
		{"untyped 5xx message is not trusted", agentWrap(errors.New("API error (status 503): upstream down")), ProviderFailureNone},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ClassifyProviderFailure(tt.err))
		})
	}
}

func TestProviderFailureString(t *testing.T) {
	assert.Equal(t, "none", ProviderFailureNone.String())
	assert.Equal(t, "throttled", ProviderFailureThrottled.String())
	assert.Equal(t, "unavailable", ProviderFailureUnavailable.String())
}

// A typed non-capacity status outranks IsThrottle's message match: these
// errors DO match the message heuristic, and must still classify as None.
func TestClassifyProviderFailureTypedStatusOutranksMessage(t *testing.T) {
	for _, err := range []error{
		sdkAPIError(t, 400, `{"message":"exceed context limit: 180429 + 21000 > 200000"}`),
		awsOperationError(400, &brtypes.ValidationException{Message: aws.String("throttle field 429 invalid")}),
	} {
		require.True(t, IsThrottle(err), "precondition: the message heuristic alone would call this a throttle: %v", err)
		assert.Equal(t, ProviderFailureNone, ClassifyProviderFailure(err), "%v", err)
	}
}
