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
// Package claude provides Claude OAuth dialog stubs.
// OAuth-based authentication from Crush is not applicable to Loom.
package claude

import (
	tea "charm.land/bubbletea/v2"

	"github.com/teradata-labs/loom/internal/tui/styles"
	"github.com/teradata-labs/loom/internal/tui/util"
)

// Model is the Claude OAuth dialog model.
type Model struct {
	theme *styles.Theme
}

// New creates a new Claude OAuth dialog.
func New(theme *styles.Theme) *Model {
	return &Model{
		theme: theme,
	}
}

// Init initializes the dialog.
func (m *Model) Init() tea.Cmd {
	return nil
}

// Update handles messages.
func (m *Model) Update(msg tea.Msg) (*Model, tea.Cmd) {
	return m, nil
}

// View renders the dialog.
func (m *Model) View() string {
	return ""
}

// SetSize sets the dialog size.
func (m *Model) SetSize(width, height int) {
}

// SetTheme sets the theme.
func (m *Model) SetTheme(theme *styles.Theme) {
	m.theme = theme
}

// Visible returns whether the dialog is visible.
func (m *Model) Visible() bool {
	return false
}

// Show shows the dialog.
func (m *Model) Show() {
}

// Hide hides the dialog.
func (m *Model) Hide() {
}

// AuthRequired returns whether auth is required.
func AuthRequired() bool {
	return false
}

// IsAuthenticated returns whether user is authenticated.
func IsAuthenticated() bool {
	return true
}

// AuthMethodState represents the selected auth method.
type AuthMethodState int

const (
	AuthMethodAPIKey AuthMethodState = iota
	AuthMethodOAuth2
)

// AuthMethodChooser is a stub for auth method selection.
type AuthMethodChooser struct {
	width  int
	height int
	State  AuthMethodState
}

// NewAuthMethodChooser creates a new auth method chooser stub.
func NewAuthMethodChooser() *AuthMethodChooser {
	return &AuthMethodChooser{
		State: AuthMethodAPIKey,
	}
}

// SetDefaults resets to default state.
func (a *AuthMethodChooser) SetDefaults() {
	a.State = AuthMethodAPIKey
}

// ToggleChoice toggles between auth methods.
func (a *AuthMethodChooser) ToggleChoice() {
	if a.State == AuthMethodAPIKey {
		a.State = AuthMethodOAuth2
	} else {
		a.State = AuthMethodAPIKey
	}
}

// Init initializes the component.
func (a *AuthMethodChooser) Init() tea.Cmd {
	return nil
}

// Update handles messages.
func (a *AuthMethodChooser) Update(msg tea.Msg) (util.Model, tea.Cmd) {
	return a, nil
}

// View renders the component.
func (a *AuthMethodChooser) View() string {
	return ""
}

// SetSize sets the component size.
func (a *AuthMethodChooser) SetSize(width, height int) tea.Cmd {
	a.width = width
	a.height = height
	return nil
}

// GetSize returns the component size.
func (a *AuthMethodChooser) GetSize() (int, int) {
	return a.width, a.height
}

// OAuth2 is a stub for OAuth2 flow.
type OAuth2 struct {
	width           int
	height          int
	complete        bool
	urlState        bool
	State           OAuthState
	ValidationState OAuthValidationState
	URL             string
	CodeInput       *CodeInputStub
}

// CodeInputStub is a stub for code input.
type CodeInputStub struct{}

// Cursor returns nil cursor.
func (c *CodeInputStub) Cursor() *tea.Cursor {
	return nil
}

// NewOAuth2 creates a new OAuth2 component stub.
func NewOAuth2() *OAuth2 {
	return &OAuth2{
		State:     OAuthStateNone,
		CodeInput: &CodeInputStub{},
	}
}

// Init initializes the component.
func (o *OAuth2) Init() tea.Cmd {
	return nil
}

// Update handles messages.
func (o *OAuth2) Update(msg tea.Msg) (util.Model, tea.Cmd) {
	return o, nil
}

// View renders the component.
func (o *OAuth2) View() string {
	return ""
}

// SetSize sets the component size.
func (o *OAuth2) SetSize(width, height int) tea.Cmd {
	o.width = width
	o.height = height
	return nil
}

// GetSize returns the component size.
func (o *OAuth2) GetSize() (int, int) {
	return o.width, o.height
}

// IsComplete returns whether OAuth flow is complete.
func (o *OAuth2) IsComplete() bool {
	return o.complete
}

// IsURLState returns whether in URL state.
func (o *OAuth2) IsURLState() bool {
	return o.urlState
}

// Cursor returns the cursor position.
func (o *OAuth2) Cursor() *tea.Cursor {
	return nil
}

// SetDefaults resets to default state.
func (o *OAuth2) SetDefaults() {
	o.State = OAuthStateNone
	o.ValidationState = OAuthValidationStateNone
	o.URL = ""
}

// ValidationConfirm handles validation confirmation.
func (o *OAuth2) ValidationConfirm() (*OAuth2, tea.Cmd) {
	return o, nil
}

// OAuthState represents OAuth flow state.
type OAuthState int

const (
	OAuthStateNone OAuthState = iota
	OAuthStateURL
	OAuthStateCode
	OAuthStateComplete
)

// OAuthValidationState represents validation state.
type OAuthValidationState int

const (
	OAuthValidationStateNone OAuthValidationState = iota
	OAuthValidationStateValid
	OAuthValidationStateInvalid
)

// ValidationCompletedMsg is sent when validation completes.
type ValidationCompletedMsg struct {
	State OAuthValidationState
	Token any
}

// AuthenticationCompleteMsg is sent when authentication completes.
type AuthenticationCompleteMsg struct {
	Success bool
}

// SetWidth sets the component width.
func (a *AuthMethodChooser) SetWidth(width int) {
	a.width = width
}
