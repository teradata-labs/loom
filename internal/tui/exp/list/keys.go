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
package list

import (
	"charm.land/bubbles/v2/key"
)

type KeyMap struct {
	Down,
	Up,
	DownOneItem,
	UpOneItem,
	PageDown,
	PageUp,
	HalfPageDown,
	HalfPageUp,
	Home,
	End key.Binding
}

func DefaultKeyMap() KeyMap {
	return KeyMap{
		Down: key.NewBinding(
			key.WithKeys("down", "ctrl+j", "ctrl+n", "j"),
			key.WithHelp("↓", "down"),
		),
		Up: key.NewBinding(
			key.WithKeys("up", "ctrl+k", "ctrl+p", "k"),
			key.WithHelp("↑", "up"),
		),
		UpOneItem: key.NewBinding(
			key.WithKeys("shift+up", "K"),
			key.WithHelp("shift+↑", "up one item"),
		),
		DownOneItem: key.NewBinding(
			key.WithKeys("shift+down", "J"),
			key.WithHelp("shift+↓", "down one item"),
		),
		HalfPageDown: key.NewBinding(
			key.WithKeys("d"),
			key.WithHelp("d", "half page down"),
		),
		PageDown: key.NewBinding(
			key.WithKeys("pgdown", " ", "f"),
			key.WithHelp("f/pgdn", "page down"),
		),
		PageUp: key.NewBinding(
			key.WithKeys("pgup", "b"),
			key.WithHelp("b/pgup", "page up"),
		),
		HalfPageUp: key.NewBinding(
			key.WithKeys("u"),
			key.WithHelp("u", "half page up"),
		),
		Home: key.NewBinding(
			key.WithKeys("g", "home"),
			key.WithHelp("g", "home"),
		),
		End: key.NewBinding(
			key.WithKeys("G", "end"),
			key.WithHelp("G", "end"),
		),
	}
}

func (k KeyMap) KeyBindings() []key.Binding {
	return []key.Binding{
		k.Down,
		k.Up,
		k.DownOneItem,
		k.UpOneItem,
		k.HalfPageDown,
		k.HalfPageUp,
		k.Home,
		k.End,
	}
}
