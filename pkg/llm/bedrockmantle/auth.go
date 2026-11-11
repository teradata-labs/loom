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
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
)

const (
	// SigningService is the AWS SigV4 service name for the bedrock-mantle
	// endpoint. Verified live 2026-11-04: requests signed for "bedrock-mantle"
	// are accepted on both the Messages and the Chat Completions paths.
	SigningService = "bedrock-mantle"

	// shortTermKeyPrefix marks a short-term Bedrock API key: a base64-encoded
	// SigV4 presigned URL that expires within 12 hours and is bound to the
	// region it was signed for. Long-term keys ("ABSK…") are opaque.
	shortTermKeyPrefix = "bedrock-api-key-"

	// emptyPayloadSHA256 is the hex SHA-256 of an empty body, used to sign
	// bodiless requests (GET /v1/models).
	emptyPayloadSHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

// ShortTermKeyRegion returns the region a short-term Bedrock API key was minted
// for. The key is "bedrock-api-key-" + base64(presigned URL) and the presigned
// URL's X-Amz-Credential has the form "<key-id>/<date>/<region>/bedrock/aws4_request".
// ok is false for long-term keys and for anything that does not parse.
//
// Mantle rejects a key used outside its region with 401 "Credential should be
// scoped to a valid region", so callers compare this against the endpoint
// region at construction time instead of at the first request.
func ShortTermKeyRegion(token string) (region string, ok bool) {
	if !strings.HasPrefix(token, shortTermKeyPrefix) {
		return "", false
	}
	encoded := strings.TrimPrefix(token, shortTermKeyPrefix)
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		raw, err = base64.RawStdEncoding.DecodeString(encoded)
		if err != nil {
			return "", false
		}
	}
	u, err := url.Parse(string(raw))
	if err != nil {
		return "", false
	}
	parts := strings.Split(u.Query().Get("X-Amz-Credential"), "/")
	if len(parts) < 3 || parts[2] == "" {
		return "", false
	}
	return parts[2], true
}

// sigv4Transport signs every request with AWS SigV4 for SigningService before
// handing it to the base transport. It strips the x-api-key and Authorization
// headers the inner clients set so an empty API key never reaches the gateway,
// then lets the signer write the Authorization header.
type sigv4Transport struct {
	base   http.RoundTripper
	creds  aws.CredentialsProvider
	signer *v4.Signer
	region string
	now    func() time.Time
}

func newSigV4Transport(base http.RoundTripper, creds aws.CredentialsProvider, region string) *sigv4Transport {
	if base == nil {
		base = http.DefaultTransport
	}
	return &sigv4Transport{
		base:   base,
		creds:  creds,
		signer: v4.NewSigner(),
		region: region,
		now:    func() time.Time { return time.Now().UTC() },
	}
}

// RoundTrip implements http.RoundTripper. The incoming request is never
// mutated: a clone carries the signature and a re-readable body.
func (t *sigv4Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx := req.Context()
	signed := req.Clone(ctx)

	payloadHash := emptyPayloadSHA256
	if req.Body != nil && req.Body != http.NoBody {
		body, err := io.ReadAll(req.Body)
		_ = req.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("bedrock-mantle: read request body for signing: %w", err)
		}
		sum := sha256.Sum256(body)
		payloadHash = hex.EncodeToString(sum[:])
		signed.Body = io.NopCloser(bytes.NewReader(body))
		signed.ContentLength = int64(len(body))
		signed.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(body)), nil
		}
	}

	signed.Header.Del("x-api-key")
	signed.Header.Del("Authorization")

	creds, err := t.creds.Retrieve(ctx)
	if err != nil {
		return nil, fmt.Errorf("bedrock-mantle: resolve AWS credentials: %w", err)
	}
	if err := t.signer.SignHTTP(ctx, creds, signed, payloadHash, SigningService, t.region, t.now()); err != nil {
		return nil, fmt.Errorf("bedrock-mantle: sign request: %w", err)
	}
	return t.base.RoundTrip(signed)
}

// loadAWSConfig resolves AWS credentials the same way pkg/llm/bedrock does:
// explicit static keys, then a named profile, then the default chain (env,
// shared config, SSO, IAM role). An ambient AWS_BEARER_TOKEN_BEDROCK is NOT
// consulted: the spike found long-term keys rejected by Mantle in at least one
// organization, so bearer auth is opt-in through Config.APIKey.
func loadAWSConfig(ctx context.Context, cfg Config) (aws.Config, error) {
	opts := []func(*config.LoadOptions) error{config.WithRegion(cfg.Region)}
	switch {
	case cfg.AccessKeyID != "" && cfg.SecretAccessKey != "":
		opts = append(opts, config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, cfg.SessionToken)))
	case cfg.Profile != "":
		opts = append(opts, config.WithSharedConfigProfile(cfg.Profile))
	}
	awsCfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return aws.Config{}, fmt.Errorf("bedrock-mantle: load AWS config: %w", err)
	}
	if awsCfg.Credentials == nil {
		return aws.Config{}, fmt.Errorf("bedrock-mantle: no AWS credentials resolved (set an API key, static keys, a profile, or run in an environment with a credential chain)")
	}
	return awsCfg, nil
}
