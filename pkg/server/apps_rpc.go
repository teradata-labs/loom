// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package server

import (
	"context"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/mcp/apps"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// AppProvider provides UI app information to the gRPC server.
// Implemented by UIResourceRegistry to avoid tight coupling between
// the server package and the apps package internals.
type AppProvider interface {
	ListAppInfo() []apps.AppInfo
	GetAppHTML(name string) ([]byte, *apps.AppInfo, error)
}

// SetAppProvider sets the app provider for ListUIApps/GetUIApp RPCs.
func (s *MultiAgentServer) SetAppProvider(p AppProvider) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.appProvider = p
}

// ListUIApps lists all available UI apps registered with the app provider.
func (s *MultiAgentServer) ListUIApps(ctx context.Context, req *loomv1.ListUIAppsRequest) (*loomv1.ListUIAppsResponse, error) {
	s.mu.RLock()
	provider := s.appProvider
	s.mu.RUnlock()

	if provider == nil {
		return &loomv1.ListUIAppsResponse{}, nil
	}

	infos := provider.ListAppInfo()
	protoApps := make([]*loomv1.UIApp, 0, len(infos))
	for _, info := range infos {
		protoApps = append(protoApps, appInfoToProto(&info))
	}

	if s.logger != nil {
		s.logger.Info("ListUIApps completed", zap.Int("total_count", len(protoApps)))
	}

	return &loomv1.ListUIAppsResponse{
		Apps:       protoApps,
		TotalCount: safeIntToInt32(len(protoApps)),
	}, nil
}

// GetUIApp retrieves a specific UI app by short name, returning its metadata and HTML content.
func (s *MultiAgentServer) GetUIApp(ctx context.Context, req *loomv1.GetUIAppRequest) (*loomv1.GetUIAppResponse, error) {
	if req.Name == "" {
		return nil, status.Error(codes.InvalidArgument, "app name is required")
	}

	s.mu.RLock()
	provider := s.appProvider
	s.mu.RUnlock()

	if provider == nil {
		return nil, status.Error(codes.NotFound, "no app provider configured")
	}

	content, info, err := provider.GetAppHTML(req.Name)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "app not found: %s", req.Name)
	}

	if s.logger != nil {
		s.logger.Info("GetUIApp completed", zap.String("name", req.Name))
	}

	return &loomv1.GetUIAppResponse{
		App:     appInfoToProto(info),
		Content: content,
	}, nil
}

// appInfoToProto converts an apps.AppInfo to a proto UIApp message.
func appInfoToProto(info *apps.AppInfo) *loomv1.UIApp {
	return &loomv1.UIApp{
		Name:          info.Name,
		Uri:           info.URI,
		DisplayName:   info.DisplayName,
		Description:   info.Description,
		MimeType:      info.MimeType,
		PrefersBorder: info.PrefersBorder,
	}
}
