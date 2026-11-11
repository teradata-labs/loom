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

package bedrockmantle

import (
	"bytes"
	"encoding/base64"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeShortTermKey builds a token with the same shape as the
// aws-bedrock-token-generator output: "bedrock-api-key-" + base64 of a
// presigned CallWithBearerToken URL whose credential scope names region.
func fakeShortTermKey(region string, raw bool) string {
	q := url.Values{}
	q.Set("Action", "CallWithBearerToken")
	q.Set("X-Amz-Algorithm", "AWS4-HMAC-SHA256")
	q.Set("X-Amz-Credential", "AKIDEXAMPLE/20261104/"+region+"/bedrock/aws4_request")
	q.Set("X-Amz-Date", "20261104T120000Z")
	q.Set("X-Amz-Expires", "43200")
	q.Set("X-Amz-SignedHeaders", "host")
	q.Set("X-Amz-Signature", "deadbeef")
	presigned := "https://bedrock.amazonaws.com/?" + q.Encode() + "&Version=1"
	if raw {
		return shortTermKeyPrefix + base64.RawStdEncoding.EncodeToString([]byte(presigned))
	}
	return shortTermKeyPrefix + base64.StdEncoding.EncodeToString([]byte(presigned))
}

func TestShortTermKeyRegion(t *testing.T) {
	tests := []struct {
		name   string
		token  string
		want   string
		wantOK bool
	}{
		{"short-term std base64", fakeShortTermKey("us-west-2", false), "us-west-2", true},
		{"short-term raw base64", fakeShortTermKey("eu-west-1", true), "eu-west-1", true},
		{"long-term key is opaque", "ABSKQmVkcm9ja0FQSUtleS1leGFtcGxl", "", false},
		{"prefix but not base64", shortTermKeyPrefix + "!!!not-base64!!!", "", false},
		{"prefix but no credential", shortTermKeyPrefix + base64.StdEncoding.EncodeToString([]byte("https://bedrock.amazonaws.com/?Action=X")), "", false},
		{"empty", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ShortTermKeyRegion(tt.token)
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

// captureRoundTripper records the request the transport forwards and returns
// a canned response.
type captureRoundTripper struct {
	req  *http.Request
	body []byte
}

func (c *captureRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	c.req = r
	if r.Body != nil {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		c.body = b
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("{}")), Header: http.Header{}}, nil
}

func TestSigV4Transport_SignsAndStripsInnerAuth(t *testing.T) {
	base := &captureRoundTripper{}
	creds := credentials.NewStaticCredentialsProvider("AKIDEXAMPLE", "secret", "session-token")
	tr := newSigV4Transport(base, creds, "us-west-2")
	tr.now = func() time.Time { return time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC) }

	payload := []byte(`{"model":"anthropic.claude-haiku-4-5"}`)
	req, err := http.NewRequest(http.MethodPost, "https://bedrock-mantle.us-west-2.api.aws/anthropic/v1/messages", bytes.NewReader(payload))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", "")            // what the anthropic client would send with no key
	req.Header.Set("Authorization", "Bearer ") // what the openai client would send with no key
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := tr.RoundTrip(req)
	require.NoError(t, err)
	require.NotNil(t, resp)
	_ = resp.Body.Close()

	signed := base.req
	require.NotNil(t, signed)
	assert.NotSame(t, req, signed, "the incoming request must not be mutated")
	_, hasAPIKey := signed.Header["X-Api-Key"]
	assert.False(t, hasAPIKey, "x-api-key must be stripped before signing")

	auth := signed.Header.Get("Authorization")
	assert.True(t, strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20261104/us-west-2/bedrock-mantle/aws4_request"), "got %q", auth)
	assert.Contains(t, auth, "SignedHeaders=")
	assert.Contains(t, auth, "Signature=")
	assert.Equal(t, "20261104T120000Z", signed.Header.Get("X-Amz-Date"))
	assert.Equal(t, "session-token", signed.Header.Get("X-Amz-Security-Token"))
	assert.Equal(t, "2023-06-01", signed.Header.Get("anthropic-version"), "non-auth headers pass through")

	assert.Equal(t, payload, base.body, "body must reach the base transport intact")
	require.NotNil(t, signed.GetBody, "body must be re-readable for transport-level retries")
	again, err := signed.GetBody()
	require.NoError(t, err)
	b, _ := io.ReadAll(again)
	assert.Equal(t, payload, b)
}

func TestSigV4Transport_BodilessRequest(t *testing.T) {
	base := &captureRoundTripper{}
	creds := credentials.NewStaticCredentialsProvider("AKIDEXAMPLE", "secret", "")
	tr := newSigV4Transport(base, creds, "us-east-1")

	req, err := http.NewRequest(http.MethodGet, "https://bedrock-mantle.us-east-1.api.aws/v1/models", nil)
	require.NoError(t, err)
	resp, err := tr.RoundTrip(req)
	require.NoError(t, err)
	_ = resp.Body.Close()

	auth := base.req.Header.Get("Authorization")
	assert.Contains(t, auth, "/us-east-1/bedrock-mantle/aws4_request")
	assert.Empty(t, base.req.Header.Get("X-Amz-Security-Token"), "no session token for long-lived keys")
	assert.Nil(t, base.body)
}

func TestNewSigV4Transport_DefaultsBase(t *testing.T) {
	tr := newSigV4Transport(nil, credentials.NewStaticCredentialsProvider("a", "b", ""), "us-west-2")
	assert.Same(t, http.DefaultTransport, tr.base)
}
