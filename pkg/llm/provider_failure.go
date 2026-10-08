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
	"net/http"
	"strings"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/aws/smithy-go"
)

// ProviderFailure classifies a failed LLM call by whether the provider said
// "not now" rather than "not this request". It is what a caller outside the
// provider stack (the gRPC server mapping a failed turn to a status code)
// uses to tell a capacity failure, which a later retry can clear, from a
// deterministic one, which it cannot.
type ProviderFailure int

const (
	// ProviderFailureNone: not a recognised provider capacity failure. The
	// error may still be a provider error (a 400, an auth failure), or not a
	// provider error at all.
	ProviderFailureNone ProviderFailure = iota
	// ProviderFailureThrottled: the provider throttled the call (HTTP 429,
	// AWS ThrottlingException, Anthropic rate_limit_error).
	ProviderFailureThrottled
	// ProviderFailureUnavailable: the provider reported a temporary server
	// fault (HTTP 500/502/503/504/529, an AWS server-fault exception,
	// Bedrock ModelNotReadyException, Anthropic overloaded_error).
	ProviderFailureUnavailable
)

// String names the classification for logs and test output.
func (f ProviderFailure) String() string {
	switch f {
	case ProviderFailureThrottled:
		return "throttled"
	case ProviderFailureUnavailable:
		return "unavailable"
	default:
		return "none"
	}
}

// HTTPStatusCoder is implemented by provider errors that carry the HTTP
// status of the response that failed. The AWS SDK's ResponseError
// (smithy-go transport/http) implements it; a loom provider error type that
// records a status should implement it too so ClassifyProviderFailure
// recognises it without a per-type case.
type HTTPStatusCoder interface {
	HTTPStatusCode() int
}

// ClassifyProviderFailure reports whether err, anywhere in its wrap chain,
// is an LLM provider capacity failure.
//
// Typed evidence is consulted first and is authoritative:
//
//  1. a *ThrottleError (every loom HTTP client wraps a 429 in one);
//  2. an *anthropic.Error (the Anthropic SDK, which the Bedrock client uses
//     for Claude models): its error type when the body named one — this is
//     the only signal for an error event that arrives mid-stream on a 200
//     response — otherwise its HTTP status;
//  3. a smithy.APIError (AWS SDK service exceptions): ThrottlingException,
//     ModelNotReadyException, or any exception the SDK marks as a server
//     fault (InternalServerException, ServiceUnavailableException, ...);
//  4. an HTTPStatusCoder: 429 is throttling, 500/502/503/504/529 is a
//     temporary server fault.
//
// A typed status outside those sets (a 400 validation error, a 403) is
// authoritative too: it returns ProviderFailureNone even when the message
// happens to contain "429" or "throttle", so a deterministic refusal is not
// misread as capacity.
//
// Only an error with no typed status falls back to IsThrottle's message
// match, which is how the AWS SDK's untyped throttling messages and the
// rate limiter's own exhaustion surface. There is deliberately no message
// fallback for 5xx: a status is only trusted when a provider typed it.
func ClassifyProviderFailure(err error) ProviderFailure {
	if err == nil {
		return ProviderFailureNone
	}
	if f, typed := classifyTypedProviderFailure(err); typed {
		return f
	}
	if IsThrottle(err) {
		return ProviderFailureThrottled
	}
	return ProviderFailureNone
}

// classifyTypedProviderFailure applies the typed checks of
// ClassifyProviderFailure. typed is false when the chain carries no typed
// provider status, in which case the caller may fall back to message
// matching.
func classifyTypedProviderFailure(err error) (f ProviderFailure, typed bool) {
	var te *ThrottleError
	if errors.As(err, &te) {
		return ProviderFailureThrottled, true
	}

	var ae *anthropic.Error
	if errors.As(err, &ae) {
		if f, ok := providerFailureForErrorName(string(ae.Type())); ok {
			return f, true
		}
		// A mid-stream error event rides on the stream's 200 response, so
		// its StatusCode says nothing about the failure. Only a real error
		// status is authoritative.
		if ae.StatusCode >= http.StatusBadRequest {
			return providerFailureForHTTPStatus(ae.StatusCode), true
		}
	}

	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		if f, ok := providerFailureForErrorName(apiErr.ErrorCode()); ok {
			return f, true
		}
		if apiErr.ErrorFault() == smithy.FaultServer {
			return ProviderFailureUnavailable, true
		}
		// A client-fault exception falls through to the HTTP status that
		// the SDK's ResponseError wraps it in.
	}

	var sc HTTPStatusCoder
	if errors.As(err, &sc) {
		if code := sc.HTTPStatusCode(); code >= http.StatusBadRequest {
			return providerFailureForHTTPStatus(code), true
		}
	}
	return ProviderFailureNone, false
}

// providerFailureForHTTPStatus maps a provider's error status. The 5xx set
// is deliberately narrow — 500 and the gateway trio, plus 529, which
// Anthropic uses for "overloaded"; any other status is not a capacity
// failure.
func providerFailureForHTTPStatus(code int) ProviderFailure {
	switch code {
	case http.StatusTooManyRequests:
		return ProviderFailureThrottled
	case http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout, 529:
		return ProviderFailureUnavailable
	}
	return ProviderFailureNone
}

// providerFailureForErrorName maps a provider-named error to a failure
// class. Names are matched case-insensitively: Anthropic error types are
// snake_case ("overloaded_error"), AWS SDK error codes are PascalCase
// ("ThrottlingException"), and Bedrock's event-stream exception headers,
// which the Anthropic SDK passes through as the error type of a mid-stream
// failure, are camelCase ("throttlingException"). ok is false for a name
// that carries no capacity meaning, so the caller falls through to the
// status.
func providerFailureForErrorName(name string) (f ProviderFailure, ok bool) {
	switch strings.ToLower(name) {
	case "rate_limit_error", "throttlingexception":
		return ProviderFailureThrottled, true
	case "overloaded_error", "api_error", "timeout_error",
		"internalserverexception", "serviceunavailableexception",
		"modelnotreadyexception", "modelstreamerrorexception":
		return ProviderFailureUnavailable, true
	}
	return ProviderFailureNone, false
}
