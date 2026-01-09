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
package adapter

import (
	"context"
	"sync"

	"github.com/teradata-labs/loom/internal/permission"
	"github.com/teradata-labs/loom/internal/pubsub"
)

// PermissionAdapter manages permission requests for tool execution.
// Implements permission.Service interface.
// In Loom, HITL requests come via WeaveProgress, not a separate permission system.
type PermissionAdapter struct {
	mu               sync.RWMutex
	skipRequests     bool
	pendingRequests  map[string]*permission.PermissionRequest
	grantedTools     map[string]bool
	persistentGrants map[string]bool
}

// NewPermissionAdapter creates a new permission adapter.
func NewPermissionAdapter() *PermissionAdapter {
	return &PermissionAdapter{
		pendingRequests:  make(map[string]*permission.PermissionRequest),
		grantedTools:     make(map[string]bool),
		persistentGrants: make(map[string]bool),
	}
}

// SetSkipRequests enables/disables auto-approval mode.
func (p *PermissionAdapter) SetSkipRequests(skip bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.skipRequests = skip
}

// SkipRequests returns whether auto-approval is enabled.
func (p *PermissionAdapter) SkipRequests() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.skipRequests
}

// Grant grants a one-time permission.
func (p *PermissionAdapter) Grant(perm permission.PermissionRequest) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.grantedTools[perm.ToolCallID] = true
	delete(p.pendingRequests, perm.ToolCallID)
}

// GrantPersistent grants a persistent permission for a tool.
func (p *PermissionAdapter) GrantPersistent(perm permission.PermissionRequest) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.persistentGrants[perm.ToolName] = true
	p.grantedTools[perm.ToolCallID] = true
	delete(p.pendingRequests, perm.ToolCallID)
}

// Deny denies a permission request.
func (p *PermissionAdapter) Deny(perm permission.PermissionRequest) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.grantedTools[perm.ToolCallID] = false
	delete(p.pendingRequests, perm.ToolCallID)
}

// IsGranted checks if a tool call is granted.
func (p *PermissionAdapter) IsGranted(toolCallID string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.grantedTools[toolCallID]
}

// IsPersistentlyGranted checks if a tool has persistent permission.
func (p *PermissionAdapter) IsPersistentlyGranted(toolName string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.persistentGrants[toolName]
}

// AddPendingRequest adds a pending permission request.
func (p *PermissionAdapter) AddPendingRequest(perm permission.PermissionRequest) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pendingRequests[perm.ToolCallID] = &perm
}

// GetPendingRequest gets a pending request by tool call ID.
func (p *PermissionAdapter) GetPendingRequest(toolCallID string) *permission.PermissionRequest {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.pendingRequests[toolCallID]
}

// Subscribe subscribes to permission request events.
// Note: In Loom, HITL requests come via WeaveProgress streaming.
func (p *PermissionAdapter) Subscribe(ctx context.Context) <-chan pubsub.Event[permission.PermissionRequest] {
	ch := make(chan pubsub.Event[permission.PermissionRequest])
	go func() {
		<-ctx.Done()
		close(ch)
	}()
	return ch
}

// SubscribeNotifications subscribes to permission notification events.
func (p *PermissionAdapter) SubscribeNotifications(ctx context.Context) <-chan pubsub.Event[permission.PermissionNotification] {
	ch := make(chan pubsub.Event[permission.PermissionNotification])
	go func() {
		<-ctx.Done()
		close(ch)
	}()
	return ch
}

// AutoApproveSession enables auto-approval for a session.
func (p *PermissionAdapter) AutoApproveSession(sessionID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.skipRequests = true
}
