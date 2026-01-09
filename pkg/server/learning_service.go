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
package server

import (
	"context"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/metaagent/learning"
)

// LearningService implements the LearningAgentService gRPC interface.
// It wraps the LearningAgent and provides gRPC service endpoints.
type LearningService struct {
	loomv1.UnimplementedLearningAgentServiceServer
	learningAgent *learning.LearningAgent
}

// NewLearningService creates a new LearningService gRPC wrapper
func NewLearningService(agent *learning.LearningAgent) *LearningService {
	return &LearningService{
		learningAgent: agent,
	}
}

// AnalyzePatternEffectiveness analyzes pattern performance over a time window
func (s *LearningService) AnalyzePatternEffectiveness(
	ctx context.Context,
	req *loomv1.AnalyzePatternEffectivenessRequest,
) (*loomv1.PatternAnalysisResponse, error) {
	return s.learningAgent.AnalyzePatternEffectiveness(ctx, req)
}

// GenerateImprovements generates improvement proposals based on analysis
func (s *LearningService) GenerateImprovements(
	ctx context.Context,
	req *loomv1.GenerateImprovementsRequest,
) (*loomv1.ImprovementsResponse, error) {
	return s.learningAgent.GenerateImprovements(ctx, req)
}

// ApplyImprovement applies an improvement proposal
func (s *LearningService) ApplyImprovement(
	ctx context.Context,
	req *loomv1.ApplyImprovementRequest,
) (*loomv1.ApplyImprovementResponse, error) {
	return s.learningAgent.ApplyImprovement(ctx, req)
}

// RollbackImprovement rolls back a previously applied improvement
func (s *LearningService) RollbackImprovement(
	ctx context.Context,
	req *loomv1.RollbackImprovementRequest,
) (*loomv1.RollbackImprovementResponse, error) {
	return s.learningAgent.RollbackImprovement(ctx, req)
}

// GetImprovementHistory retrieves improvement history
func (s *LearningService) GetImprovementHistory(
	ctx context.Context,
	req *loomv1.GetImprovementHistoryRequest,
) (*loomv1.ImprovementHistoryResponse, error) {
	return s.learningAgent.GetImprovementHistory(ctx, req)
}

// StreamPatternMetrics streams real-time pattern metrics
// 🚨 CRITICAL: This implements the streaming RPC using MessageBus
func (s *LearningService) StreamPatternMetrics(
	req *loomv1.StreamPatternMetricsRequest,
	stream loomv1.LearningAgentService_StreamPatternMetricsServer,
) error {
	return s.learningAgent.StreamPatternMetrics(req, stream)
}
