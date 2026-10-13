package app

import (
	"claude-squad/config"
	"claude-squad/session"
	"claude-squad/ui"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// saveFromOtherProcess simulates another claude-squad process (another TUI,
// `cs new`) saving a paused instance titled title in repo.
func saveFromOtherProcess(t *testing.T, repo string, title string) {
	t.Helper()
	other, err := session.NewStorage(config.LoadState())
	require.NoError(t, err)
	instance, err := session.FromInstanceData(session.InstanceData{
		Title: title, Path: repo, Status: session.Paused, Program: "claude",
	})
	require.NoError(t, err)
	require.NoError(t, other.SaveInstances([]*session.Instance{instance}))
}

// newRefreshTestHome builds a home with real storage and the panes that
// instanceChanged touches when the list changes.
func newRefreshTestHome(t *testing.T) *home {
	t.Helper()
	h := newTestHome()
	storage, err := session.NewStorage(config.LoadState())
	require.NoError(t, err)
	h.storage = storage
	h.tabbedWindow = ui.NewTabbedWindow(ui.NewPreviewPane(), ui.NewDiffPane(), ui.NewTerminalPane())
	return h
}

func titles(h *home) []string {
	var out []string
	for _, inst := range h.list.GetInstances() {
		out = append(out, inst.Title)
	}
	return out
}

func TestRefreshAddsInstancesSavedByOtherProcesses(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := initTestRepo(t)
	h := newRefreshTestHome(t)

	h.Update(refreshTickMsg{})
	assert.Empty(t, titles(h), "nothing on disk yet")

	saveFromOtherProcess(t, repo, "a")
	h.Update(refreshTickMsg{})
	assert.Equal(t, []string{"a"}, titles(h))

	h.Update(refreshTickMsg{})
	assert.Equal(t, []string{"a"}, titles(h), "unchanged state file is not re-read")

	// A record this TUI already has is never added twice, even when the file changes.
	saveFromOtherProcess(t, repo, "a")
	h.Update(refreshTickMsg{})
	assert.Equal(t, []string{"a"}, titles(h))

	saveFromOtherProcess(t, repo, "b")
	h.Update(refreshTickMsg{})
	assert.Equal(t, []string{"a", "b"}, titles(h))
}

func TestRefreshWaitsWhileWizardIsOpen(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := initTestRepo(t)
	h := newRefreshTestHome(t)

	saveFromOtherProcess(t, repo, "a")
	for _, s := range []state{stateNewDir, stateNewRepo, stateNew, statePrompt, stateConfirm, stateHelp} {
		h.state = s
		h.Update(refreshTickMsg{})
		assert.Empty(t, titles(h), "state %d", s)
	}

	h.state = stateDefault
	h.Update(refreshTickMsg{})
	assert.Equal(t, []string{"a"}, titles(h), "picked up once the wizard closes")
}

func TestRefreshTickReschedulesItself(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := newRefreshTestHome(t)

	_, cmd := h.Update(refreshTickMsg{})
	require.NotNil(t, cmd)
}
