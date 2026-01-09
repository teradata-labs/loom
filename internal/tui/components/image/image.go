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
// Based on the implementation by @trashhalo at:
// https://github.com/trashhalo/imgcat
package image

import (
	"fmt"
	_ "image/jpeg"
	_ "image/png"

	tea "charm.land/bubbletea/v2"
)

type Model struct {
	url    string
	image  string
	width  uint
	height uint
	err    error
}

func New(width, height uint, url string) Model {
	return Model{
		width:  width,
		height: height,
		url:    url,
	}
}

func (m Model) Init() tea.Cmd {
	return nil
}

func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case errMsg:
		m.err = msg
		return m, nil
	case redrawMsg:
		m.width = msg.width
		m.height = msg.height
		m.url = msg.url
		return m, loadURL(m.url)
	case loadMsg:
		return handleLoadMsg(m, msg)
	}
	return m, nil
}

func (m Model) View() string {
	if m.err != nil {
		return fmt.Sprintf("couldn't load image(s): %v", m.err)
	}
	return m.image
}

type errMsg struct{ error }

func (m Model) Redraw(width uint, height uint, url string) tea.Cmd {
	return func() tea.Msg {
		return redrawMsg{
			width:  width,
			height: height,
			url:    url,
		}
	}
}

func (m Model) UpdateURL(url string) tea.Cmd {
	return func() tea.Msg {
		return redrawMsg{
			width:  m.width,
			height: m.height,
			url:    url,
		}
	}
}

type redrawMsg struct {
	width  uint
	height uint
	url    string
}

func (m Model) IsLoading() bool {
	return m.image == ""
}
