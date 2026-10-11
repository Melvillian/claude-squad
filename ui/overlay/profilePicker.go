package overlay

import (
	"claude-squad/config"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ProfilePicker is an embeddable component for selecting a profile.
// It displays a horizontal selector with left/right arrow navigation.
type ProfilePicker struct {
	profiles []config.Profile
	cursor   int
	focused  bool
	width    int
	// label is the heading shown above the choices.
	label string
	// changeHint describes the keys that change the selection.
	changeHint string
}

// NewProfilePicker creates a new profile picker with the given profiles.
// The first profile is selected by default.
func NewProfilePicker(profiles []config.Profile) *ProfilePicker {
	return &ProfilePicker{
		profiles:   profiles,
		label:      "Profile",
		changeHint: "←/→ to change",
	}
}

// Focus gives the profile picker focus.
func (pp *ProfilePicker) Focus() {
	pp.focused = true
}

// Blur removes focus from the profile picker.
func (pp *ProfilePicker) Blur() {
	pp.focused = false
}

// SetWidth sets the rendering width.
func (pp *ProfilePicker) SetWidth(w int) {
	pp.width = w
}

// HandleKeyPress processes a key event. Returns true if consumed.
func (pp *ProfilePicker) HandleKeyPress(msg tea.KeyMsg) bool {
	switch msg.Type {
	case tea.KeyLeft:
		if pp.cursor > 0 {
			pp.cursor--
		}
		return true
	case tea.KeyRight:
		if pp.cursor < len(pp.profiles)-1 {
			pp.cursor++
		}
		return true
	}
	return false
}

// Next selects the following profile, wrapping around to the first.
func (pp *ProfilePicker) Next() {
	pp.cursor = (pp.cursor + 1) % len(pp.profiles)
}

// Prev selects the preceding profile, wrapping around to the last.
func (pp *ProfilePicker) Prev() {
	pp.cursor = (pp.cursor - 1 + len(pp.profiles)) % len(pp.profiles)
}

// GetSelectedProfile returns the currently selected profile.
func (pp *ProfilePicker) GetSelectedProfile() config.Profile {
	if pp.cursor < 0 || pp.cursor >= len(pp.profiles) {
		return pp.profiles[0]
	}
	return pp.profiles[pp.cursor]
}

// HasMultiple returns true if there is more than one profile to choose from.
func (pp *ProfilePicker) HasMultiple() bool {
	return len(pp.profiles) > 1
}

var (
	ppLabelStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("62")).
			Bold(true)

	ppSelectedStyle = lipgloss.NewStyle().
			Background(lipgloss.Color("62")).
			Foreground(lipgloss.Color("0"))

	ppDimStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("240"))
)

// Render renders the profile picker.
func (pp *ProfilePicker) Render() string {
	var s strings.Builder
	s.WriteString(ppLabelStyle.Render(pp.label))

	if pp.HasMultiple() && pp.focused {
		s.WriteString(ppDimStyle.Render("  " + pp.changeHint))
	}
	s.WriteString("\n\n")

	for i, p := range pp.profiles {
		if i == pp.cursor && pp.focused {
			s.WriteString(ppSelectedStyle.Render(" " + p.Name + " "))
		} else if i == pp.cursor {
			s.WriteString(" " + p.Name + " ")
		} else {
			s.WriteString(ppDimStyle.Render(" " + p.Name + " "))
		}
		if i < len(pp.profiles)-1 {
			s.WriteString(ppDimStyle.Render(" | "))
		}
	}

	return s.String()
}
