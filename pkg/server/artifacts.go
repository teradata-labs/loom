// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package server

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/artifacts"
	"github.com/teradata-labs/loom/pkg/session"
	"github.com/teradata-labs/loom/pkg/types"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ListArtifacts lists artifacts with optional filtering.
func (s *MultiAgentServer) ListArtifacts(ctx context.Context, req *loomv1.ListArtifactsRequest) (*loomv1.ListArtifactsResponse, error) {
	if s.artifactStore == nil {
		return nil, status.Error(codes.Unimplemented, "artifact store not configured")
	}

	// Build filter
	filter := &artifacts.Filter{
		IncludeDeleted: req.GetIncludeDeleted(),
		Limit:          int(req.GetLimit()),
		Offset:         int(req.GetOffset()),
	}

	if req.Source != "" {
		source := artifacts.SourceType(req.Source)
		filter.Source = &source
	}

	if req.ContentType != "" {
		filter.ContentType = &req.ContentType
	}

	if len(req.Tags) > 0 {
		filter.Tags = req.Tags
	}

	// Apply default limit
	if filter.Limit == 0 {
		filter.Limit = 50
	}

	// List artifacts
	artifactList, err := s.artifactStore.List(ctx, filter)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list artifacts: %v", err)
	}

	// Convert to proto
	protoArtifacts := make([]*loomv1.Artifact, len(artifactList))
	for i, art := range artifactList {
		protoArtifacts[i] = artifactToProto(art)
	}

	return &loomv1.ListArtifactsResponse{
		Artifacts:  protoArtifacts,
		TotalCount: types.SafeInt32(len(artifactList)),
	}, nil
}

// GetArtifact retrieves artifact metadata.
func (s *MultiAgentServer) GetArtifact(ctx context.Context, req *loomv1.GetArtifactRequest) (*loomv1.GetArtifactResponse, error) {
	if s.artifactStore == nil {
		return nil, status.Error(codes.Unimplemented, "artifact store not configured")
	}

	var art *artifacts.Artifact
	var err error

	if req.Id != "" {
		art, err = s.artifactStore.Get(ctx, req.Id)
	} else if req.Name != "" {
		// Extract session ID from context for scoped lookup
		sessionID := session.SessionIDFromContext(ctx)
		art, err = s.artifactStore.GetByName(ctx, req.Name, sessionID)
	} else {
		return nil, status.Error(codes.InvalidArgument, "either id or name must be provided")
	}

	if err != nil {
		return nil, status.Errorf(codes.NotFound, "artifact not found: %v", err)
	}

	return &loomv1.GetArtifactResponse{
		Artifact: artifactToProto(art),
	}, nil
}

// UploadArtifact uploads a file to artifacts storage.
func (s *MultiAgentServer) UploadArtifact(ctx context.Context, req *loomv1.UploadArtifactRequest) (*loomv1.UploadArtifactResponse, error) {
	if s.artifactStore == nil {
		return nil, status.Error(codes.Unimplemented, "artifact store not configured")
	}

	// Validate request
	if req.Name == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	if len(req.Content) == 0 {
		return nil, status.Error(codes.InvalidArgument, "content is required")
	}

	// Validate source
	source := artifacts.SourceType(req.Source)
	if source == "" {
		source = artifacts.SourceUser // Default to user
	}
	if source != artifacts.SourceUser && source != artifacts.SourceGenerated && source != artifacts.SourceAgent {
		return nil, status.Errorf(codes.InvalidArgument, "invalid source: %s (must be user, generated, or agent)", req.Source)
	}

	// Check size limits (100MB default)
	const maxFileSize = 100 * 1024 * 1024
	if int64(len(req.Content)) > maxFileSize {
		return nil, status.Errorf(codes.InvalidArgument, "file too large: %d bytes (max: %d)", len(req.Content), maxFileSize)
	}

	// Get artifacts directory
	artifactsDir, err := artifacts.GetArtifactsDir()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get artifacts directory: %v", err)
	}

	// Ensure directory exists
	if err := os.MkdirAll(artifactsDir, 0750); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create artifacts directory: %v", err)
	}

	// Generate unique filename to avoid conflicts
	filename := req.Name
	filePath := filepath.Join(artifactsDir, filename)

	// Check if file exists
	if _, err := os.Stat(filePath); err == nil {
		// File exists - generate unique name with timestamp
		ext := filepath.Ext(filename)
		base := filename[:len(filename)-len(ext)]
		timestamp := time.Now().Format("20060102-150405")
		filename = fmt.Sprintf("%s-%s%s", base, timestamp, ext)
		filePath = filepath.Join(artifactsDir, filename)
	}

	// Write file
	if err := os.WriteFile(filePath, req.Content, 0640); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to write file: %v", err)
	}

	// Analyze file
	analyzer := artifacts.NewAnalyzer()
	result, err := analyzer.Analyze(filePath)
	if err != nil {
		// Cleanup file on error
		os.Remove(filePath)
		return nil, status.Errorf(codes.Internal, "failed to analyze file: %v", err)
	}

	// Timestamp for all artifacts created in this upload
	now := time.Now()

	// Check if file is an archive and auto-extract it
	if artifacts.IsArchive(result.ContentType) {
		s.logger.Info("archive detected, auto-extracting",
			zap.String("filename", filename),
			zap.String("content_type", result.ContentType))

		// Add "archive" tag if not already present
		hasArchiveTag := false
		for _, tag := range result.Tags {
			if tag == "archive" {
				hasArchiveTag = true
				break
			}
		}
		if !hasArchiveTag {
			result.Tags = append(result.Tags, "archive")
		}

		// Create extraction directory (remove archive extension from name)
		extractDirName := strings.TrimSuffix(filename, filepath.Ext(filename))
		extractDir := filepath.Join(artifactsDir, extractDirName)

		// Extract archive
		extractedFiles, err := artifacts.ExtractArchive(filePath, extractDir)
		if err != nil {
			s.logger.Warn("failed to extract archive, will index as archive only",
				zap.String("filename", filename),
				zap.Error(err))
		} else {
			s.logger.Info("archive extracted successfully",
				zap.String("filename", filename),
				zap.Int("file_count", len(extractedFiles)))

			// Index each extracted file as a separate artifact
			for _, extractedPath := range extractedFiles {
				extractedAnalyzer := artifacts.NewAnalyzer()
				extractedResult, err := extractedAnalyzer.Analyze(extractedPath)
				if err != nil {
					s.logger.Warn("failed to analyze extracted file, skipping",
						zap.String("path", extractedPath),
						zap.Error(err))
					continue
				}

				// Create artifact for extracted file
				extractedFilename := filepath.Base(extractedPath)
				extractedArtifact := &artifacts.Artifact{
					ID:            artifacts.GenerateArtifactID(),
					Name:          extractedFilename,
					Path:          extractedPath,
					Source:        source,
					SourceAgentID: "", // No agent ID for extracted files
					Purpose:       fmt.Sprintf("Extracted from %s", filename),
					ContentType:   extractedResult.ContentType,
					SizeBytes:     extractedResult.SizeBytes,
					Checksum:      extractedResult.Checksum,
					CreatedAt:     now,
					UpdatedAt:     now,
					Tags:          append(extractedResult.Tags, "extracted", "from-archive"),
					Metadata: map[string]string{
						"parent_archive":      filename,
						"extraction_dir":      extractDirName,
						"relative_path":       strings.TrimPrefix(extractedPath, extractDir+"/"),
						"parent_content_type": result.ContentType,
					},
				}

				// Merge metadata from extracted file
				for k, v := range extractedResult.Metadata {
					extractedArtifact.Metadata[k] = v
				}

				// Index extracted file
				if err := s.artifactStore.Index(ctx, extractedArtifact); err != nil {
					s.logger.Warn("failed to index extracted file",
						zap.String("path", extractedPath),
						zap.Error(err))
					continue
				}
			}

			// Add extraction metadata to parent archive
			if result.Metadata == nil {
				result.Metadata = make(map[string]string)
			}
			result.Metadata["extraction_dir"] = extractDirName
			result.Metadata["extracted_file_count"] = fmt.Sprintf("%d", len(extractedFiles))
		}
	}

	// Merge user-provided tags with inferred tags
	tags := result.Tags
	if len(req.Tags) > 0 {
		tags = append(tags, req.Tags...)
		// Deduplicate
		tagMap := make(map[string]bool)
		uniqueTags := []string{}
		for _, tag := range tags {
			if !tagMap[tag] {
				tagMap[tag] = true
				uniqueTags = append(uniqueTags, tag)
			}
		}
		tags = uniqueTags
	}

	// Validate source_agent_id to prevent foreign key constraint violations
	// Only use source_agent_id if explicitly provided and source is "agent"
	sourceAgentID := ""
	if req.SourceAgentId != "" && source == artifacts.SourceAgent {
		// Verify the agent exists in sessions table
		if s.sessionStore != nil {
			if _, err := s.sessionStore.LoadSession(ctx, req.SourceAgentId); err == nil {
				sourceAgentID = req.SourceAgentId
			} else {
				// Agent doesn't exist - log warning but don't fail
				if s.logger != nil {
					s.logger.Warn("source_agent_id not found in sessions, ignoring",
						zap.String("agent_id", req.SourceAgentId),
						zap.Error(err))
				}
			}
		}
	}

	// Create artifact metadata
	artifact := &artifacts.Artifact{
		ID:            artifacts.GenerateArtifactID(),
		Name:          filename,
		Path:          filePath,
		Source:        source,
		SourceAgentID: sourceAgentID,
		Purpose:       req.Purpose,
		ContentType:   result.ContentType,
		SizeBytes:     result.SizeBytes,
		Checksum:      result.Checksum,
		CreatedAt:     now,
		UpdatedAt:     now,
		Tags:          tags,
		Metadata:      result.Metadata,
	}

	// Index in database
	if err := s.artifactStore.Index(ctx, artifact); err != nil {
		// Cleanup file on error
		os.Remove(filePath)
		return nil, status.Errorf(codes.Internal, "failed to index artifact: %v", err)
	}

	return &loomv1.UploadArtifactResponse{
		Artifact: artifactToProto(artifact),
	}, nil
}

// DeleteArtifact deletes an artifact (soft or hard delete).
func (s *MultiAgentServer) DeleteArtifact(ctx context.Context, req *loomv1.DeleteArtifactRequest) (*loomv1.DeleteArtifactResponse, error) {
	if s.artifactStore == nil {
		return nil, status.Error(codes.Unimplemented, "artifact store not configured")
	}

	if req.Id == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	if err := s.artifactStore.Delete(ctx, req.Id, req.HardDelete); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to delete artifact: %v", err)
	}

	return &loomv1.DeleteArtifactResponse{
		Success: true,
	}, nil
}

// SearchArtifacts performs full-text search on artifacts.
func (s *MultiAgentServer) SearchArtifacts(ctx context.Context, req *loomv1.SearchArtifactsRequest) (*loomv1.SearchArtifactsResponse, error) {
	if s.artifactStore == nil {
		return nil, status.Error(codes.Unimplemented, "artifact store not configured")
	}

	if req.Query == "" {
		return nil, status.Error(codes.InvalidArgument, "query is required")
	}

	limit := int(req.Limit)
	if limit == 0 {
		limit = 20
	}

	// Extract session ID from context for scoped search
	sessionID := session.SessionIDFromContext(ctx)
	artifactList, err := s.artifactStore.Search(ctx, req.Query, sessionID, limit)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to search artifacts: %v", err)
	}

	protoArtifacts := make([]*loomv1.Artifact, len(artifactList))
	for i, art := range artifactList {
		protoArtifacts[i] = artifactToProto(art)
	}

	return &loomv1.SearchArtifactsResponse{
		Artifacts: protoArtifacts,
	}, nil
}

// GetArtifactContent reads artifact file content.
func (s *MultiAgentServer) GetArtifactContent(ctx context.Context, req *loomv1.GetArtifactContentRequest) (*loomv1.GetArtifactContentResponse, error) {
	if s.artifactStore == nil {
		return nil, status.Error(codes.Unimplemented, "artifact store not configured")
	}

	if req.Id == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	// Get artifact metadata
	art, err := s.artifactStore.Get(ctx, req.Id)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "artifact not found: %v", err)
	}

	// Check size limit
	maxSizeMB := req.MaxSizeMb
	if maxSizeMB == 0 {
		maxSizeMB = 10 // Default 10MB
	}
	maxSizeBytes := maxSizeMB * 1024 * 1024

	if art.SizeBytes > maxSizeBytes {
		return nil, status.Errorf(codes.FailedPrecondition, "artifact too large: %d bytes (max: %d MB)", art.SizeBytes, maxSizeMB)
	}

	// Read file content
	content, err := os.ReadFile(art.Path)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to read file: %v", err)
	}

	// Determine encoding
	encoding := req.Encoding
	if encoding == "" {
		// Auto-detect: use base64 for binary files
		if isTextContent(art.ContentType) {
			encoding = "text"
		} else {
			encoding = "base64"
		}
	}

	// Encode if needed
	var responseContent []byte
	if encoding == "base64" {
		encoded := base64.StdEncoding.EncodeToString(content)
		responseContent = []byte(encoded)
	} else {
		responseContent = content
	}

	// Record access
	if err := s.artifactStore.RecordAccess(ctx, req.Id); err != nil {
		// Log but don't fail
		if s.logger != nil {
			s.logger.Warn("failed to record artifact access",
				zap.String("id", req.Id),
				zap.Error(err))
		}
	}

	return &loomv1.GetArtifactContentResponse{
		Content:  responseContent,
		Encoding: encoding,
	}, nil
}

// GetArtifactStats retrieves storage statistics.
func (s *MultiAgentServer) GetArtifactStats(ctx context.Context, req *loomv1.GetArtifactStatsRequest) (*loomv1.GetArtifactStatsResponse, error) {
	if s.artifactStore == nil {
		return nil, status.Error(codes.Unimplemented, "artifact store not configured")
	}

	stats, err := s.artifactStore.GetStats(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get stats: %v", err)
	}

	return &loomv1.GetArtifactStatsResponse{
		TotalFiles:     int32(stats.TotalFiles),
		TotalSizeBytes: stats.TotalSizeBytes,
		UserFiles:      int32(stats.UserFiles),
		GeneratedFiles: int32(stats.GeneratedFiles),
		DeletedFiles:   int32(stats.DeletedFiles),
	}, nil
}

// artifactToProto converts an artifacts.Artifact to loomv1.Artifact.
func artifactToProto(art *artifacts.Artifact) *loomv1.Artifact {
	proto := &loomv1.Artifact{
		Id:            art.ID,
		Name:          art.Name,
		Path:          art.Path,
		Source:        string(art.Source),
		SourceAgentId: art.SourceAgentID,
		Purpose:       art.Purpose,
		ContentType:   art.ContentType,
		SizeBytes:     art.SizeBytes,
		Checksum:      art.Checksum,
		CreatedAt:     art.CreatedAt.Unix(),
		UpdatedAt:     art.UpdatedAt.Unix(),
		AccessCount:   int32(art.AccessCount),
		Tags:          art.Tags,
		Metadata:      art.Metadata,
	}

	if art.LastAccessedAt != nil {
		proto.LastAccessedAt = art.LastAccessedAt.Unix()
	}

	if art.DeletedAt != nil {
		proto.DeletedAt = art.DeletedAt.Unix()
	}

	return proto
}

// isTextContent checks if a content type represents text data.
func isTextContent(contentType string) bool {
	textTypes := []string{
		"text/",
		"application/json",
		"application/xml",
		"application/yaml",
		"application/x-yaml",
		"application/sql",
		"application/javascript",
		"application/typescript",
	}

	for _, prefix := range textTypes {
		if len(contentType) >= len(prefix) && contentType[:len(prefix)] == prefix {
			return true
		}
	}

	return false
}
