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
// This file implements the subscriptions/listen wire types of the 2026-07-28
// revision, which replace resources/subscribe and the standalone GET stream.
package protocol

// Method and notification names for the subscriptions pattern.
const (
	MethodSubscriptionsListen = "subscriptions/listen"

	NotificationSubscriptionAcknowledged = "notifications/subscriptions/acknowledged"
	NotificationToolsListChanged         = "notifications/tools/list_changed"
	NotificationPromptsListChanged       = "notifications/prompts/list_changed"
	NotificationResourcesListChanged     = "notifications/resources/list_changed"
	NotificationResourceUpdated          = "notifications/resources/updated"
)

// NotificationFilter selects which notification types a subscriptions/listen
// stream delivers. Omitted fields mean "not subscribed". The server's
// acknowledgment echoes the subset it agreed to honor.
type NotificationFilter struct {
	ToolsListChanged      bool     `json:"toolsListChanged,omitempty"`
	PromptsListChanged    bool     `json:"promptsListChanged,omitempty"`
	ResourcesListChanged  bool     `json:"resourcesListChanged,omitempty"`
	ResourceSubscriptions []string `json:"resourceSubscriptions,omitempty"`
}

// ListenParams is the params object of a subscriptions/listen request.
type ListenParams struct {
	Notifications NotificationFilter `json:"notifications"`
}
