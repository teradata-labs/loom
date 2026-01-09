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
package util

import (
	tea "charm.land/bubbletea/v2"
	"github.com/teradata-labs/loom/internal/uiutil"
)

// Cursor is an interface for components that can return a cursor position.
type Cursor interface {
	Cursor() *tea.Cursor
}

type Model interface {
	Init() tea.Cmd
	Update(tea.Msg) (Model, tea.Cmd)
	View() string
}

// CmdHandler wraps a message in a command that returns that message.
func CmdHandler(msg tea.Msg) tea.Cmd {
	return uiutil.CmdHandler(msg)
}

// ReportError reports an error message.
func ReportError(err error) tea.Cmd {
	if err == nil {
		return nil
	}
	return uiutil.ReportError(err.Error())
}

type InfoType = uiutil.InfoType

const (
	InfoTypeInfo    = uiutil.InfoTypeInfo
	InfoTypeSuccess = uiutil.InfoTypeSuccess
	InfoTypeWarn    = uiutil.InfoTypeWarn
	InfoTypeError   = uiutil.InfoTypeError
	InfoTypeUpdate  = uiutil.InfoTypeUpdate
)

func ReportInfo(info string) tea.Cmd {
	return uiutil.ReportInfo(info)
}

func ReportWarn(warn string) tea.Cmd {
	return uiutil.ReportWarn(warn)
}

type (
	InfoMsg        = uiutil.InfoMsg
	ClearStatusMsg = uiutil.ClearStatusMsg
)
