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
package editor

import (
	"charm.land/bubbles/v2/key"
)

type EditorKeyMap struct {
	AddFile     key.Binding
	SendMessage key.Binding
	OpenEditor  key.Binding
	Newline     key.Binding
}

func DefaultEditorKeyMap() EditorKeyMap {
	return EditorKeyMap{
		AddFile: key.NewBinding(
			key.WithKeys("/"),
			key.WithHelp("/", "add file"),
		),
		SendMessage: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("enter", "send"),
		),
		OpenEditor: key.NewBinding(
			key.WithKeys("ctrl+o"),
			key.WithHelp("ctrl+o", "open editor"),
		),
		Newline: key.NewBinding(
			key.WithKeys("shift+enter", "ctrl+j"),
			// "ctrl+j" is a common keybinding for newline in many editors. If
			// the terminal supports "shift+enter", we substitute the help text
			// to reflect that.
			key.WithHelp("ctrl+j", "newline"),
		),
	}
}

// KeyBindings implements layout.KeyMapProvider
func (k EditorKeyMap) KeyBindings() []key.Binding {
	return []key.Binding{
		k.AddFile,
		k.SendMessage,
		k.OpenEditor,
		k.Newline,
		AttachmentsKeyMaps.AttachmentDeleteMode,
		AttachmentsKeyMaps.DeleteAllAttachments,
		AttachmentsKeyMaps.Escape,
	}
}

type DeleteAttachmentKeyMaps struct {
	AttachmentDeleteMode key.Binding
	Escape               key.Binding
	DeleteAllAttachments key.Binding
}

// TODO: update this to use the new keymap concepts
var AttachmentsKeyMaps = DeleteAttachmentKeyMaps{
	AttachmentDeleteMode: key.NewBinding(
		key.WithKeys("ctrl+r"),
		key.WithHelp("ctrl+r+{i}", "delete attachment at index i"),
	),
	Escape: key.NewBinding(
		key.WithKeys("esc", "alt+esc"),
		key.WithHelp("esc", "cancel delete mode"),
	),
	DeleteAllAttachments: key.NewBinding(
		key.WithKeys("r"),
		key.WithHelp("ctrl+r+r", "delete all attachments"),
	),
}
