// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package openairesponses

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"syscall"
	"time"

	"github.com/teradata-labs/loom/pkg/llm"
)

// isRetryableTransportError reports whether a transport error is a stale
// keep-alive connection that one retry may recover. Go reports these both
// before and after the server processed the request, so a retry can run the
// call twice; callers must not rely on exactly-once delivery.
func isRetryableTransportError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	return errors.Is(err, syscall.ECONNRESET) || errors.Is(err, net.ErrClosed)
}

// sendRequest posts a blocking request.
func (c *Client) sendRequest(ctx context.Context, body []byte) (*http.Response, error) {
	return c.send(ctx, body, c.httpClient, false, 0, 0)
}

// sendStreamingRequest posts a streaming request with the first-byte and
// idle read timeouts applied to the body.
func (c *Client) sendStreamingRequest(ctx context.Context, body []byte) (*http.Response, error) {
	return c.send(ctx, body, c.streamHTTPClient, true, c.streamFirstByteTimeout, c.streamIdleTimeout)
}

// send posts body to the endpoint. With a rate limiter, 429 and transient
// 5xx statuses become llm.ThrottleError / llm.TransientError inside the
// limiter so its retry budget applies, and every attempt rebuilds the
// request from body. Without one, only a stale-connection transport error is
// retried, once; a non-2xx status is returned for checkStatus to report.
func (c *Client) send(ctx context.Context, body []byte, httpClient *http.Client, stream bool,
	firstByte, idle time.Duration) (*http.Response, error) {
	newReq := func(ctx context.Context) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		if stream {
			req.Header.Set("Accept", "text/event-stream")
		}
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
		for k, v := range c.extraHeaders {
			req.Header.Set(k, v)
		}
		return req, nil
	}

	if c.rateLimiter != nil {
		for attempt := 1; attempt <= 2; attempt++ {
			result, err := c.rateLimiter.Do(ctx, func(ctx context.Context) (interface{}, error) {
				req, err := newReq(ctx)
				if err != nil {
					return nil, err
				}
				resp, err := doHTTP(httpClient, req, firstByte, idle)
				if err != nil {
					return nil, err
				}
				if resp.StatusCode == http.StatusTooManyRequests {
					retryAfter := llm.RetryAfterFromHeaders(resp.Header)
					respBody, _ := io.ReadAll(io.LimitReader(resp.Body, llm.MaxErrorBodyBytes))
					_ = resp.Body.Close()
					return nil, llm.NewThrottleError(statusError(resp.StatusCode, respBody), retryAfter)
				}
				if llm.IsTransientStatus(resp.StatusCode) {
					retryAfter := llm.RetryAfterFromHeaders(resp.Header)
					respBody, _ := io.ReadAll(io.LimitReader(resp.Body, llm.MaxErrorBodyBytes))
					_ = resp.Body.Close()
					return nil, llm.NewTransientError(statusError(resp.StatusCode, respBody), resp.StatusCode, retryAfter)
				}
				return resp, nil
			})
			if err == nil {
				return result.(*http.Response), nil
			}
			if attempt == 2 || !isRetryableTransportError(err) {
				return nil, err
			}
		}
		return nil, errors.New("HTTP request failed")
	}

	for attempt := 1; attempt <= 2; attempt++ {
		req, err := newReq(ctx)
		if err != nil {
			return nil, err
		}
		resp, err := doHTTP(httpClient, req, firstByte, idle)
		if err == nil {
			return resp, nil
		}
		if attempt == 2 || !isRetryableTransportError(err) {
			return nil, fmt.Errorf("HTTP request failed: %w", err)
		}
	}
	return nil, errors.New("HTTP request failed")
}

// doHTTP runs one request through llm.DoStreamHTTP, which wraps the body so a
// stalled read (or no headers within the first-byte limit) returns an error
// wrapping llm.ErrStreamTimeout. With both limits zero it is a plain Do.
func doHTTP(httpClient *http.Client, req *http.Request, firstByte, idle time.Duration) (*http.Response, error) {
	return llm.DoStreamHTTP(httpClient, req, firstByte, idle)
}
