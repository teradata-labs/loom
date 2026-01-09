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
// Package permission provides permission types compatible with Crush's interface.
package permission

import (
	"context"

	"github.com/teradata-labs/loom/internal/pubsub"
)

// PermissionRequest represents a tool permission request.
type PermissionRequest struct {
	ID          string
	ToolName    string
	ToolCallID  string
	SessionID   string
	Description string
	Arguments   string
	Priority    string
	Timeout     int32
	Path        string // File path for file-related tools
	Params      any    // Tool-specific parameters (type-assert to specific param type)
}

// PermissionNotification represents a permission grant/deny notification.
type PermissionNotification struct {
	ToolCallID string
	Granted    bool
}

// ErrorPermissionDenied is returned when a permission is denied.
var ErrorPermissionDenied = &PermissionDeniedError{}

// PermissionDeniedError represents a permission denied error.
type PermissionDeniedError struct{}

func (e *PermissionDeniedError) Error() string {
	return "permission denied"
}

// Service defines the permission service interface.
type Service interface {
	SetSkipRequests(skip bool)
	SkipRequests() bool
	Grant(perm PermissionRequest)
	GrantPersistent(perm PermissionRequest)
	Deny(perm PermissionRequest)
	IsGranted(toolCallID string) bool
	Subscribe(ctx context.Context) <-chan pubsub.Event[PermissionRequest]
	SubscribeNotifications(ctx context.Context) <-chan pubsub.Event[PermissionNotification]
	AutoApproveSession(sessionID string)
}
