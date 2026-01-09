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
package mcp

import (
	"charm.land/bubbles/v2/key"
)

// AddServerDialogKeyMap defines the key bindings for the add server dialog
type AddServerDialogKeyMap struct {
	Close    key.Binding
	Confirm  key.Binding
	Test     key.Binding
	Next     key.Binding
	Previous key.Binding
}

// ShortHelp returns key bindings to be shown in the help view.
func (k AddServerDialogKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Test, k.Confirm, k.Next, k.Previous, k.Close}
}

// FullHelp returns all key bindings.
func (k AddServerDialogKeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Test, k.Confirm},
		{k.Next, k.Previous},
		{k.Close},
	}
}

// DefaultAddServerDialogKeyMap returns a default set of key bindings.
func DefaultAddServerDialogKeyMap() AddServerDialogKeyMap {
	return AddServerDialogKeyMap{
		Close: key.NewBinding(
			key.WithKeys("esc"),
			key.WithHelp("esc", "close"),
		),
		Confirm: key.NewBinding(
			key.WithKeys("ctrl+s"),
			key.WithHelp("ctrl+s", "save (after test passes)"),
		),
		Test: key.NewBinding(
			key.WithKeys("ctrl+t"),
			key.WithHelp("ctrl+t", "test connection"),
		),
		Next: key.NewBinding(
			key.WithKeys("tab", "down"),
			key.WithHelp("tab/↓", "next field"),
		),
		Previous: key.NewBinding(
			key.WithKeys("shift+tab", "up"),
			key.WithHelp("shift+tab/↑", "previous field"),
		),
	}
}
