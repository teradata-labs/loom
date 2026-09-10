package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Issue #348 follow-up: spec.llm.rate_limit in agent YAML was silently
// dropped — LLMConfigYAML had no field for it, so the proto RateLimit was
// never populated and every agent ran on provider defaults regardless of
// what the YAML said.
func TestLLMConfigYAMLRateLimitRoundTrip(t *testing.T) {
	y := &LLMConfigYAML{
		Provider: "azure-openai",
		Model:    "gpt-4o",
		RateLimit: &LLMRateLimitYAML{
			RequestsPerSecond:   15,
			TokensPerMinute:     1_200_000,
			MinDelayMs:          10,
			BurstCapacity:       32,
			QueueTimeoutSeconds: 600,
		},
	}
	pb, err := convertLLMConfigYAMLToProto(y)
	require.NoError(t, err)
	require.NotNil(t, pb.RateLimit, "rate_limit must survive YAML→proto conversion")
	assert.Equal(t, 15.0, pb.RateLimit.RequestsPerSecond)
	assert.Equal(t, int64(1_200_000), pb.RateLimit.TokensPerMinute)
	assert.Equal(t, int32(10), pb.RateLimit.MinDelayMs)
	assert.Equal(t, int32(32), pb.RateLimit.BurstCapacity)
	assert.Equal(t, int32(600), pb.RateLimit.QueueTimeoutSeconds)

	back := convertProtoToLLMConfigYAML(pb)
	require.NotNil(t, back.RateLimit)
	assert.Equal(t, y.RateLimit, back.RateLimit, "proto→YAML must round-trip")

	// Absent block stays absent (nil, not zero-values).
	pbNo, err := convertLLMConfigYAMLToProto(&LLMConfigYAML{Provider: "azure-openai"})
	require.NoError(t, err)
	assert.Nil(t, pbNo.RateLimit)
}
