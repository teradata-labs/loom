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
package splash

import (
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/teradata-labs/loom/internal/charmtone"
	"github.com/teradata-labs/loom/internal/config"
	"github.com/teradata-labs/loom/internal/home"
	"github.com/teradata-labs/loom/internal/tui/components/core/layout"
	"github.com/teradata-labs/loom/internal/tui/components/logo"
	"github.com/teradata-labs/loom/internal/tui/styles"
	"github.com/teradata-labs/loom/internal/tui/util"
	"github.com/teradata-labs/loom/internal/version"
)

type Splash interface {
	util.Model
	layout.Sizeable
	layout.Help
	Cursor() *tea.Cursor
	// SetOnboarding controls whether the splash shows model selection UI
	SetOnboarding(bool)
	// SetProjectInit controls whether the splash shows project initialization prompt
	SetProjectInit(bool)

	// SetAgentInfo sets the current agent ID and name for display
	SetAgentInfo(agentID, agentName string)

	// Showing API key input
	IsShowingAPIKey() bool

	// IsAPIKeyValid returns whether the API key is valid
	IsAPIKeyValid() bool

	// IsShowingClaudeAuthMethodChooser returns whether showing Claude auth method chooser
	IsShowingClaudeAuthMethodChooser() bool

	// IsShowingClaudeOAuth2 returns whether showing Claude OAuth2 flow
	IsShowingClaudeOAuth2() bool

	// IsClaudeOAuthURLState returns whether in OAuth URL state
	IsClaudeOAuthURLState() bool

	// IsClaudeOAuthComplete returns whether Claude OAuth flow is complete
	IsClaudeOAuthComplete() bool
}

const (
	SplashScreenPaddingY = 1 // Padding Y for the splash screen
	LogoGap              = 6
)

// OnboardingCompleteMsg is sent when onboarding is complete
type (
	OnboardingCompleteMsg struct{}
	SubmitAPIKeyMsg       struct{}
)

type splashCmp struct {
	width, height int
	keyMap        KeyMap
	logoRendered  string
	agentID       string
	agentName     string
}

func New() Splash {
	return &splashCmp{
		keyMap: DefaultKeyMap(),
	}
}

func (s *splashCmp) SetOnboarding(onboarding bool) {
	// No-op for Loom - onboarding handled by server
}

func (s *splashCmp) SetProjectInit(needsInit bool) {
	// No-op for Loom - project init handled by server
}

func (s *splashCmp) SetAgentInfo(agentID, agentName string) {
	s.agentID = agentID
	s.agentName = agentName
}

// GetSize implements SplashPage.
func (s *splashCmp) GetSize() (int, int) {
	return s.width, s.height
}

// Init implements SplashPage.
func (s *splashCmp) Init() tea.Cmd {
	return nil
}

// SetSize implements SplashPage.
func (s *splashCmp) SetSize(width int, height int) tea.Cmd {
	wasSmallScreen := s.isSmallScreen()
	rerenderLogo := width != s.width
	s.height = height
	s.width = width
	if rerenderLogo || wasSmallScreen != s.isSmallScreen() {
		s.logoRendered = s.logoBlock()
	}
	return nil
}

// Update implements SplashPage.
func (s *splashCmp) Update(msg tea.Msg) (util.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		return s, s.SetSize(msg.Width, msg.Height)
	case tea.KeyPressMsg:
		// Any key press dismisses splash and starts chat
		if key.Matches(msg, s.keyMap.Select) {
			return s, util.CmdHandler(OnboardingCompleteMsg{})
		}
	}
	return s, nil
}

func (s *splashCmp) View() string {
	t := styles.CurrentTheme()
	parts := []string{
		s.logoRendered,
		s.infoSection(),
	}
	content := lipgloss.JoinVertical(lipgloss.Left, parts...)

	return t.S().Base.
		Width(s.width).
		Height(s.height).
		PaddingTop(SplashScreenPaddingY).
		PaddingBottom(SplashScreenPaddingY).
		Render(content)
}

func (s *splashCmp) Cursor() *tea.Cursor {
	return nil
}

func (s *splashCmp) isSmallScreen() bool {
	return s.width < 55 || s.height < 20
}

func (s *splashCmp) infoSection() string {
	t := styles.CurrentTheme()
	infoStyle := t.S().Base.PaddingLeft(2)
	if s.isSmallScreen() {
		infoStyle = infoStyle.MarginTop(1)
	}

	parts := []string{
		s.cwdPart(),
		"",
		s.currentModelBlock(),
	}

	// Add agent description if available
	if desc := s.agentDescription(); desc != "" {
		parts = append(parts, "", desc)
	}

	parts = append(parts, "")

	return infoStyle.Render(
		lipgloss.JoinVertical(lipgloss.Left, parts...),
	)
}

func (s *splashCmp) logoBlock() string {
	t := styles.CurrentTheme()
	logoStyle := t.S().Base.Padding(0, 2).Width(s.width)
	if s.isSmallScreen() {
		return logoStyle.Render(
			logo.SmallRender(s.width - logoStyle.GetHorizontalFrameSize()),
		)
	}
	return logoStyle.Render(
		logo.Render(version.Version, false, logo.Opts{
			FieldColor:    charmtone.TeradataOrange,
			TitleColorA:   charmtone.TeradataCyan,
			TitleColorB:   charmtone.TeradataOrange,
			TeradataColor: charmtone.TeradataOrange,
			VersionColor:  charmtone.TeradataCyan,
			Width:         s.width - logoStyle.GetHorizontalFrameSize(),
		}),
	)
}

// Bindings implements SplashPage.
func (s *splashCmp) Bindings() []key.Binding {
	return []key.Binding{
		s.keyMap.Select,
	}
}

func (s *splashCmp) getMaxInfoWidth() int {
	return min(s.width-2, 90)
}

func (s *splashCmp) cwdPart() string {
	t := styles.CurrentTheme()
	maxWidth := s.getMaxInfoWidth()
	return t.S().Muted.Width(maxWidth).Render(s.cwd())
}

func (s *splashCmp) cwd() string {
	return home.Short(config.Get().WorkingDir())
}

func (s *splashCmp) currentModelBlock() string {
	model := config.Get().GetModel()
	if model == nil {
		return ""
	}
	t := styles.CurrentTheme()
	modelIcon := t.S().Base.Foreground(t.FgSubtle).Render(styles.ModelIcon)
	modelName := t.S().Text.Render(model.Name)
	return lipgloss.JoinHorizontal(lipgloss.Left, modelIcon, " ", modelName)
}

// agentDescription returns a description for special agents like weaver and mender
func (s *splashCmp) agentDescription() string {
	t := styles.CurrentTheme()
	maxWidth := s.getMaxInfoWidth()

	var title, description string

	switch s.agentID {
	case "weaver":
		title = "✨ Weaver"
		description = `The weaver analyzes your natural language requirements and creates
specialized threads with appropriate patterns, tools, and capabilities.
Tell it what you need, and it will design and deploy custom agents for you.

Examples:
  • "Create a SQL query analyzer for PostgreSQL"
  • "Monitor REST APIs and track rate limits"
  • "Build a log file parser with error detection"`

	case "mender":
		title = "🪡 Mender"
		description = `The mender modifies existing agents and workflows. It can update
configurations, add new tools, adjust prompts, and refine capabilities.
Give it feedback or enhancement requests for your agents.

Examples:
  • "Add retry logic to my-api-agent"
  • "Improve error messages in sql-analyzer"
  • "Make the data-processor handle CSV files"`

	default:
		return ""
	}

	if description == "" {
		return ""
	}

	// Render title and description with appropriate styling
	titleStyle := t.S().Base.
		Foreground(t.Primary).
		Bold(true).
		Width(maxWidth)

	descStyle := t.S().Base.
		Foreground(t.FgSubtle).
		Width(maxWidth)

	return lipgloss.JoinVertical(
		lipgloss.Left,
		titleStyle.Render(title),
		"",
		descStyle.Render(description),
	)
}

// Stub implementations for interface compliance
func (s *splashCmp) IsShowingAPIKey() bool                  { return false }
func (s *splashCmp) IsAPIKeyValid() bool                    { return true }
func (s *splashCmp) IsShowingClaudeAuthMethodChooser() bool { return false }
func (s *splashCmp) IsShowingClaudeOAuth2() bool            { return false }
func (s *splashCmp) IsClaudeOAuthURLState() bool            { return false }
func (s *splashCmp) IsClaudeOAuthComplete() bool            { return false }
