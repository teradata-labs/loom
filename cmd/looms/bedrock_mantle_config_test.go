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
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/llm/bedrockmantle"
	"go.uber.org/zap"
)

// Static, obviously fake credentials: construction with static keys never
// touches the network, which keeps these tests hermetic.
const (
	testMantleAccessKeyID = "test-access-key-id"
	testMantleSecretKey   = "test-secret-access-key"
)

func TestMantleRegion_FallsBackToBedrockRegion(t *testing.T) {
	assert.Equal(t, "us-west-2", mantleRegion(LLMConfig{BedrockRegion: "us-west-2"}))
	assert.Equal(t, "eu-west-1", mantleRegion(LLMConfig{BedrockRegion: "us-west-2", BedrockMantleRegion: "eu-west-1"}))
	assert.Empty(t, mantleRegion(LLMConfig{}))
}

func TestMantleConfigFrom(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		cfg  LLMConfig
		want bedrockmantle.Config
	}{
		{
			name: "bedrock region and static SigV4 keys",
			env:  map[string]string{"BEDROCK_MANTLE_API_KEY": ""},
			cfg: LLMConfig{
				BedrockRegion:          "us-west-2",
				BedrockAccessKeyID:     testMantleAccessKeyID,
				BedrockSecretAccessKey: testMantleSecretKey,
				BedrockSessionToken:    "session",
				BedrockMantleModel:     "openai.gpt-oss-120b",
				MaxTokens:              2048,
				Temperature:            0.5,
				Timeout:                30,
			},
			want: bedrockmantle.Config{
				Region:          "us-west-2",
				AccessKeyID:     testMantleAccessKeyID,
				SecretAccessKey: testMantleSecretKey,
				SessionToken:    "session",
				Model:           "openai.gpt-oss-120b",
				MaxTokens:       2048,
				Temperature:     0.5,
			},
		},
		{
			name: "a profile drops explicit SigV4 keys",
			env:  map[string]string{"BEDROCK_MANTLE_API_KEY": ""},
			cfg: LLMConfig{
				BedrockRegion:          "us-west-2",
				BedrockAccessKeyID:     testMantleAccessKeyID,
				BedrockSecretAccessKey: testMantleSecretKey,
				BedrockProfile:         "bedrock",
			},
			want: bedrockmantle.Config{Region: "us-west-2", Profile: "bedrock"},
		},
		{
			name: "mantle region wins and placeholders expand",
			env: map[string]string{
				"BEDROCK_MANTLE_API_KEY":      "",
				"TEST_MANTLE_HELPER_KEY":      "key-from-placeholder",
				"TEST_MANTLE_HELPER_ENDPOINT": "https://mantle.example.test",
			},
			cfg: LLMConfig{
				BedrockRegion:         "us-west-2",
				BedrockMantleRegion:   "eu-west-1",
				BedrockMantleEndpoint: "${TEST_MANTLE_HELPER_ENDPOINT}",
				BedrockMantleAPIKey:   "${TEST_MANTLE_HELPER_KEY}",
			},
			want: bedrockmantle.Config{
				Region:   "eu-west-1",
				Endpoint: "https://mantle.example.test",
				APIKey:   "key-from-placeholder",
			},
		},
		{
			name: "empty config key falls back to BEDROCK_MANTLE_API_KEY",
			env:  map[string]string{"BEDROCK_MANTLE_API_KEY": "key-from-env"},
			cfg:  LLMConfig{BedrockMantleRegion: "us-east-1"},
			want: bedrockmantle.Config{Region: "us-east-1", APIKey: "key-from-env"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			got := mantleConfigFrom(tt.cfg)
			assert.Equal(t, tt.want, got)
			assert.Zero(t, got.Timeout, "callers set Timeout")
			assert.Zero(t, got.RateLimiterConfig, "callers set RateLimiterConfig")
		})
	}
}

func TestValidate_BedrockMantle(t *testing.T) {
	base := func(llm LLMConfig) *Config {
		llm.Provider = "bedrock-mantle"
		return &Config{
			Server:  ServerConfig{Port: 60051},
			LLM:     llm,
			Storage: StorageBackendConfig{Backend: "sqlite", SQLite: SQLiteConfig{Path: "/tmp/test.db"}},
		}
	}

	t.Run("no region anywhere -> error", func(t *testing.T) {
		err := base(LLMConfig{}).Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "bedrock-mantle region is required")
	})
	t.Run("llm.bedrock_region alone is enough", func(t *testing.T) {
		assert.NoError(t, base(LLMConfig{BedrockRegion: "us-west-2"}).Validate())
	})
	t.Run("llm.bedrock_mantle_region alone is enough", func(t *testing.T) {
		assert.NoError(t, base(LLMConfig{BedrockMantleRegion: "us-east-1"}).Validate())
	})
}

func TestGetSecretMappings_BedrockMantleAPIKey(t *testing.T) {
	mappings := GetSecretMappings()
	idx := -1
	for i, m := range mappings {
		if m.KeyringKey == "bedrock_mantle_api_key" {
			idx = i
		}
	}
	require.NotEqual(t, -1, idx, "bedrock_mantle_api_key must be a keyring secret")

	cfg := &Config{}
	assert.False(t, mappings[idx].IsSet(cfg))
	mappings[idx].Setter(cfg, "bedrock-api-key-test")
	assert.Equal(t, "bedrock-api-key-test", cfg.LLM.BedrockMantleAPIKey)
	assert.True(t, mappings[idx].IsSet(cfg))
}

func TestInheritCredentials_BedrockMantle(t *testing.T) {
	src := LLMConfig{
		BedrockMantleRegion:   "eu-west-1",
		BedrockMantleEndpoint: "https://mantle.example.test",
		BedrockMantleAPIKey:   "key-from-main",
	}

	got := inheritCredentials(LLMConfig{}, src)
	assert.Equal(t, "eu-west-1", got.BedrockMantleRegion)
	assert.Equal(t, "https://mantle.example.test", got.BedrockMantleEndpoint)
	assert.Equal(t, "key-from-main", got.BedrockMantleAPIKey)

	own := inheritCredentials(LLMConfig{BedrockMantleRegion: "us-east-2", BedrockMantleAPIKey: "own-key"}, src)
	assert.Equal(t, "us-east-2", own.BedrockMantleRegion, "pool entry's own value wins")
	assert.Equal(t, "own-key", own.BedrockMantleAPIKey)
	assert.Equal(t, "https://mantle.example.test", own.BedrockMantleEndpoint)
}

// TestInheritCredentials_PoolEntryMantleRegionPrecedence checks that a region
// set on a provider-pool entry wins over the main LLM's inherited
// bedrock_mantle_region (review item S5 on #558).
func TestInheritCredentials_PoolEntryMantleRegionPrecedence(t *testing.T) {
	tests := []struct {
		name       string
		entry      LLMConfig
		main       LLMConfig
		wantRegion string
	}{
		{
			name:       "entry sets bedrock_region only: entry region wins",
			entry:      LLMConfig{BedrockRegion: "ap-southeast-2"},
			main:       LLMConfig{BedrockRegion: "us-west-2", BedrockMantleRegion: "eu-west-1"},
			wantRegion: "ap-southeast-2",
		},
		{
			name:       "entry sets bedrock_mantle_region: that region",
			entry:      LLMConfig{BedrockRegion: "ap-southeast-2", BedrockMantleRegion: "us-east-2"},
			main:       LLMConfig{BedrockRegion: "us-west-2", BedrockMantleRegion: "eu-west-1"},
			wantRegion: "us-east-2",
		},
		{
			name:       "entry sets bedrock_mantle_region only: that region",
			entry:      LLMConfig{BedrockMantleRegion: "us-east-2"},
			main:       LLMConfig{BedrockRegion: "us-west-2", BedrockMantleRegion: "eu-west-1"},
			wantRegion: "us-east-2",
		},
		{
			name:       "entry sets neither: inherited mantle region",
			entry:      LLMConfig{},
			main:       LLMConfig{BedrockRegion: "us-west-2", BedrockMantleRegion: "eu-west-1"},
			wantRegion: "eu-west-1",
		},
		{
			name:       "main has no mantle region, entry sets neither: inherited bedrock_region",
			entry:      LLMConfig{},
			main:       LLMConfig{BedrockRegion: "us-west-2"},
			wantRegion: "us-west-2",
		},
		{
			name:       "main has no mantle region, entry sets bedrock_region: entry region",
			entry:      LLMConfig{BedrockRegion: "ap-southeast-2"},
			main:       LLMConfig{BedrockRegion: "us-west-2"},
			wantRegion: "ap-southeast-2",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := inheritCredentials(tt.entry, tt.main)
			assert.Equal(t, tt.wantRegion, mantleRegion(got))
			assert.Equal(t, tt.wantRegion, mantleConfigFrom(got).Region)
		})
	}
}

func TestGetDefaultModelForProvider_BedrockMantle(t *testing.T) {
	cfg := &Config{LLM: LLMConfig{Provider: "bedrock-mantle"}}
	assert.Equal(t, bedrockmantle.DefaultModel, getDefaultModelForProvider(cfg))

	cfg.LLM.BedrockMantleModel = "anthropic.claude-opus-4-7"
	assert.Equal(t, "anthropic.claude-opus-4-7", getDefaultModelForProvider(cfg))
}

func TestCreateProviderWithRateLimit_BedrockMantle(t *testing.T) {
	// No Mantle region set: the provider must inherit llm.bedrock_region.
	p, err := createProviderWithRateLimit(LLMConfig{
		Provider:               "bedrock-mantle",
		BedrockRegion:          "us-west-2",
		BedrockAccessKeyID:     testMantleAccessKeyID,
		BedrockSecretAccessKey: testMantleSecretKey,
		BedrockMantleModel:     "openai.gpt-oss-120b",
	}, zap.NewNop())
	require.NoError(t, err)

	client, ok := p.(*bedrockmantle.Client)
	require.True(t, ok, "expected *bedrockmantle.Client, got %T", p)
	assert.Equal(t, "bedrock-mantle", client.Name())
	assert.Equal(t, "openai.gpt-oss-120b", client.Model())
	assert.Equal(t, "https://bedrock-mantle.us-west-2.api.aws", client.BaseURL())
}

func TestCreateLLMProviderFromProtoConfig_BedrockMantle(t *testing.T) {
	server := &Config{LLM: LLMConfig{
		BedrockMantleRegion:    "us-east-1",
		BedrockAccessKeyID:     testMantleAccessKeyID,
		BedrockSecretAccessKey: testMantleSecretKey,
		BedrockMantleModel:     bedrockmantle.DefaultModel,
	}}

	t.Run("agent model overrides the server default", func(t *testing.T) {
		p, err := createLLMProviderFromProtoConfig(&loomv1.LLMConfig{Provider: "bedrock-mantle", Model: "anthropic.claude-opus-4-7"}, server, zap.NewNop())
		require.NoError(t, err)
		client, ok := p.(*bedrockmantle.Client)
		require.True(t, ok, "expected *bedrockmantle.Client, got %T", p)
		assert.Equal(t, "anthropic.claude-opus-4-7", client.Model())
		assert.Equal(t, "https://bedrock-mantle.us-east-1.api.aws", client.BaseURL())
	})
	t.Run("empty agent model uses llm.bedrock_mantle_model", func(t *testing.T) {
		p, err := createLLMProviderFromProtoConfig(&loomv1.LLMConfig{Provider: "bedrock-mantle"}, server, zap.NewNop())
		require.NoError(t, err)
		client, ok := p.(*bedrockmantle.Client)
		require.True(t, ok, "expected *bedrockmantle.Client, got %T", p)
		assert.Equal(t, bedrockmantle.DefaultModel, client.Model())
	})
}

// TestWorkflowCreateLLMProvider_BedrockMantleAPIKeyFromEnv is a regression test
// for the `looms workflow` provider path: with only BEDROCK_MANTLE_API_KEY set
// (no key in config) the provider must send that key as a bearer token, the
// same way the serve path does, instead of falling back to SigV4.
func TestWorkflowCreateLLMProvider_BedrockMantleAPIKeyFromEnv(t *testing.T) {
	tests := []struct {
		name      string
		cfgKey    string
		cfgEndpt  func(url string) string
		wantToken string
	}{
		{
			name:      "env var only",
			cfgKey:    "",
			cfgEndpt:  func(url string) string { return url },
			wantToken: "ABSK-from-env",
		},
		{
			name:      "placeholders in config",
			cfgKey:    "${TEST_MANTLE_KEY_PLACEHOLDER}",
			cfgEndpt:  func(string) string { return "${TEST_MANTLE_ENDPOINT_PLACEHOLDER}" },
			wantToken: "ABSK-from-placeholder",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Hermetic AWS environment: no shared config, no credentials file,
			// no IMDS. Only the bearer key can authenticate the request.
			empty := filepath.Join(t.TempDir(), "empty-aws")
			require.NoError(t, os.WriteFile(empty, nil, 0o600))
			t.Setenv("AWS_CONFIG_FILE", empty)
			t.Setenv("AWS_SHARED_CREDENTIALS_FILE", empty)
			t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
			t.Setenv("AWS_PROFILE", "")
			t.Setenv("AWS_ACCESS_KEY_ID", "")
			t.Setenv("AWS_SECRET_ACCESS_KEY", "")
			t.Setenv("AWS_SESSION_TOKEN", "")

			var gotAuth atomic.Value
			gotAuth.Store("")
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotAuth.Store(r.Header.Get("Authorization"))
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"object":"list","data":[]}`))
			}))
			t.Cleanup(srv.Close)

			if tt.cfgKey == "" {
				t.Setenv("BEDROCK_MANTLE_API_KEY", tt.wantToken)
			} else {
				t.Setenv("BEDROCK_MANTLE_API_KEY", "")
				t.Setenv("TEST_MANTLE_KEY_PLACEHOLDER", tt.wantToken)
				t.Setenv("TEST_MANTLE_ENDPOINT_PLACEHOLDER", srv.URL)
			}

			saved := config
			t.Cleanup(func() { config = saved })
			config = &Config{LLM: LLMConfig{
				Provider:              "bedrock-mantle",
				BedrockRegion:         "us-west-2",
				BedrockMantleEndpoint: tt.cfgEndpt(srv.URL),
				BedrockMantleAPIKey:   tt.cfgKey,
				BedrockMantleModel:    "openai.gpt-oss-120b",
			}}

			p, label := createLLMProvider()
			require.Equal(t, "AWS Bedrock Mantle (config)", label)
			client, ok := p.(*bedrockmantle.Client)
			require.True(t, ok, "expected *bedrockmantle.Client, got %T", p)
			assert.Equal(t, srv.URL, client.BaseURL())

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			require.NoError(t, client.HealthCheck(ctx))
			assert.Equal(t, "Bearer "+tt.wantToken, gotAuth.Load())
		})
	}
}

// On the agent-YAML path an explicit llm.bedrock_mantle_region wins, then
// BEDROCK_MANTLE_REGION, then AWS_REGION, then the region mantleConfigFrom
// resolved (llm.bedrock_region). The endpoint env fills only an empty endpoint.
func TestWithMantleEnvFallback(t *testing.T) {
	tests := []struct {
		name         string
		in           bedrockmantle.Config
		cfg          LLMConfig
		mantleRegion string
		awsRegion    string
		endpoint     string
		wantRegion   string
		wantEndpoint string
	}{
		{name: "explicit bedrock_mantle_region and endpoint win",
			in:  bedrockmantle.Config{Region: "eu-west-1", Endpoint: "https://server.example/v1"},
			cfg: LLMConfig{BedrockMantleRegion: "eu-west-1"}, mantleRegion: "us-west-2", awsRegion: "us-east-1",
			endpoint: "https://env.example/v1", wantRegion: "eu-west-1", wantEndpoint: "https://server.example/v1"},
		{name: "BEDROCK_MANTLE_REGION beats bedrock_region", in: bedrockmantle.Config{Region: "us-west-2"},
			mantleRegion: "us-east-2", awsRegion: "us-east-1", wantRegion: "us-east-2"},
		{name: "AWS_REGION beats bedrock_region", in: bedrockmantle.Config{Region: "us-west-2"},
			awsRegion: "us-east-1", wantRegion: "us-east-1"},
		{name: "no env keeps bedrock_region", in: bedrockmantle.Config{Region: "us-west-2"}, wantRegion: "us-west-2"},
		{name: "endpoint from env", endpoint: "https://env.example/v1", wantEndpoint: "https://env.example/v1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("BEDROCK_MANTLE_REGION", tt.mantleRegion)
			t.Setenv("AWS_REGION", tt.awsRegion)
			t.Setenv("BEDROCK_MANTLE_ENDPOINT", tt.endpoint)
			got := withMantleEnvFallback(tt.in, tt.cfg)
			assert.Equal(t, tt.wantRegion, got.Region)
			assert.Equal(t, tt.wantEndpoint, got.Endpoint)
		})
	}
}

// Through the real LoadConfig, llm.bedrock_region carries its built-in default
// (us-west-2), so an environment region must win over it on the agent-YAML path;
// an explicit llm.bedrock_mantle_region still wins over the environment.
func TestCreateLLMProviderFromProtoConfig_BedrockMantleRegionThroughLoadConfig(t *testing.T) {
	tests := []struct {
		name         string
		yaml         string
		mantleRegion string
		awsRegion    string
		wantBaseURL  string
	}{
		{name: "BEDROCK_MANTLE_REGION beats the bedrock_region default", yaml: "server:\n  port: 60051\n",
			mantleRegion: "us-east-1", wantBaseURL: "https://bedrock-mantle.us-east-1.api.aws"},
		{name: "AWS_REGION beats the bedrock_region default", yaml: "server:\n  port: 60051\n",
			awsRegion: "eu-west-1", wantBaseURL: "https://bedrock-mantle.eu-west-1.api.aws"},
		{name: "llm.bedrock_mantle_region beats the environment", yaml: "server:\n  port: 60051\nllm:\n  bedrock_mantle_region: ap-southeast-2\n",
			mantleRegion: "us-east-1", wantBaseURL: "https://bedrock-mantle.ap-southeast-2.api.aws"},
		{name: "no setting anywhere keeps the bedrock_region default", yaml: "server:\n  port: 60051\n",
			wantBaseURL: "https://bedrock-mantle.us-west-2.api.aws"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isolateAWSEnv(t)
			t.Setenv("BEDROCK_MANTLE_REGION", tt.mantleRegion)
			t.Setenv("AWS_REGION", tt.awsRegion)
			t.Setenv("BEDROCK_MANTLE_ENDPOINT", "")
			t.Setenv("BEDROCK_MANTLE_API_KEY", "ABSKtest")
			viper.Reset()
			t.Cleanup(viper.Reset)
			cfgPath := filepath.Join(t.TempDir(), "looms.yaml")
			require.NoError(t, os.WriteFile(cfgPath, []byte(tt.yaml), 0o600))
			cfg, err := LoadConfig(cfgPath)
			require.NoError(t, err)

			provider, err := createLLMProviderFromProtoConfig(&loomv1.LLMConfig{Provider: "bedrock-mantle", Model: "anthropic.claude-haiku-4-5"}, cfg, zap.NewNop())
			require.NoError(t, err)
			client, ok := provider.(*bedrockmantle.Client)
			require.True(t, ok, "provider must be a bedrock-mantle client")
			assert.Equal(t, tt.wantBaseURL, client.BaseURL())
		})
	}
}
