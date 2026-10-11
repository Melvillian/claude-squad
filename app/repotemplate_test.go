package app

import (
	"claude-squad/config"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeGH replaces runCmd for the duration of a test. gh invocations are
// recorded and handled by handle; everything else (git) runs for real.
func fakeGH(t *testing.T, handle func(dir string, args []string) ([]byte, error)) *[][]string {
	t.Helper()
	var calls [][]string
	orig, origDelay := runCmd, cloneRetryDelay
	realRun := runCmd
	runCmd = func(ctx context.Context, dir string, name string, args ...string) ([]byte, error) {
		if name != "gh" {
			return realRun(ctx, dir, name, args...)
		}
		calls = append(calls, args)
		return handle(dir, args)
	}
	cloneRetryDelay = 0
	t.Cleanup(func() { runCmd, cloneRetryDelay = orig, origDelay })
	return &calls
}

// makeClone creates dir/name as a git repo, with a commit unless empty is set.
func makeClone(t *testing.T, dir, name string, empty bool) {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, exec.Command("git", "init", path).Run())
	if !empty {
		cmd := exec.Command("git", "-C", path, "-c", "user.name=t", "-c", "user.email=t@t",
			"commit", "--allow-empty", "-m", "init")
		require.NoError(t, cmd.Run())
	}
}

func TestValidateRepoName(t *testing.T) {
	assert.NoError(t, validateRepoName("my-repo_1.x"))
	assert.Error(t, validateRepoName(""))
	assert.Error(t, validateRepoName("owner/repo"))
	assert.Error(t, validateRepoName("has space"))
	assert.Error(t, validateRepoName(".."))
}

func TestCreateRepoFromTemplate(t *testing.T) {
	t.Run("runs gh repo create in parent dir and returns clone path", func(t *testing.T) {
		parent := t.TempDir()
		calls := fakeGH(t, func(dir string, args []string) ([]byte, error) {
			assert.Equal(t, parent, dir)
			makeClone(t, dir, "new-repo", false)
			return nil, nil
		})

		path, err := createRepoFromTemplate(context.Background(), parent, "new-repo", "me/tpl")
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(parent, "new-repo"), path)
		assert.Equal(t, [][]string{{"repo", "create", "new-repo", "--template", "me/tpl", "--private", "--clone"}}, *calls)
	})

	t.Run("creates missing parent dir", func(t *testing.T) {
		parent := filepath.Join(t.TempDir(), "code")
		fakeGH(t, func(dir string, args []string) ([]byte, error) {
			makeClone(t, dir, "r", false)
			return nil, nil
		})
		_, err := createRepoFromTemplate(context.Background(), parent, "r", "me/tpl")
		require.NoError(t, err)
	})

	t.Run("gh failure surfaces its output", func(t *testing.T) {
		fakeGH(t, func(dir string, args []string) ([]byte, error) {
			return []byte("GraphQL: Name already exists on this account\n"), errors.New("exit 1")
		})
		_, err := createRepoFromTemplate(context.Background(), t.TempDir(), "r", "me/tpl")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "Name already exists")
	})

	t.Run("existing directory errors without calling gh", func(t *testing.T) {
		parent := t.TempDir()
		require.NoError(t, os.Mkdir(filepath.Join(parent, "r"), 0755))
		calls := fakeGH(t, func(dir string, args []string) ([]byte, error) { return nil, nil })
		_, err := createRepoFromTemplate(context.Background(), parent, "r", "me/tpl")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "already exists")
		assert.Empty(t, *calls)
	})

	t.Run("empty clone is re-cloned until it has commits", func(t *testing.T) {
		parent := t.TempDir()
		clones := 0
		calls := fakeGH(t, func(dir string, args []string) ([]byte, error) {
			clones++
			// The first two clones happen before GitHub has copied the template.
			makeClone(t, dir, "r", clones <= 2)
			return nil, nil
		})
		path, err := createRepoFromTemplate(context.Background(), parent, "r", "me/tpl")
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(parent, "r"), path)
		require.Len(t, *calls, 3)
		assert.Equal(t, []string{"repo", "clone", "r"}, (*calls)[2])
	})

	t.Run("gives up after retries", func(t *testing.T) {
		fakeGH(t, func(dir string, args []string) ([]byte, error) {
			makeClone(t, dir, "r", true)
			return nil, nil
		})
		_, err := createRepoFromTemplate(context.Background(), t.TempDir(), "r", "me/tpl")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no commits yet")
	})
}

func TestCheckGitHubCLI(t *testing.T) {
	fakeGH(t, func(dir string, args []string) ([]byte, error) { return nil, nil })
	assert.Equal(t, ghCheckDoneMsg{ok: true}, checkGitHubCLI(context.Background())())

	fakeGH(t, func(dir string, args []string) ([]byte, error) { return nil, errors.New("not logged in") })
	assert.Equal(t, ghCheckDoneMsg{ok: false}, checkGitHubCLI(context.Background())())
}

func newTemplateTestHome(t *testing.T) *home {
	h := newTestHome()
	h.appConfig.RepoTemplates = []config.RepoTemplate{
		{Name: "TypeScript", Repo: "me/ts"},
		{Name: "Rust", Repo: "me/rust"},
	}
	h.appConfig.RepoTemplatesDir = t.TempDir()
	return h
}

func ctrlT() tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyCtrlT} }

func TestRepoTemplateKeyIgnoredWhenGHUnavailable(t *testing.T) {
	h := newTemplateTestHome(t)
	h.Update(ghCheckDoneMsg{ok: false})
	h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	assert.NotContains(t, h.textInputOverlay.Render(), "new repo from template")

	h.handleKeyPress(ctrlT())
	assert.Equal(t, stateNewDir, h.state)
}

func TestRepoTemplateKeyIgnoredWithoutTemplates(t *testing.T) {
	h := newTestHome()
	h.Update(ghCheckDoneMsg{ok: true})
	h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	h.handleKeyPress(ctrlT())
	assert.Equal(t, stateNewDir, h.state)
}

func TestRepoTemplateFlowCreatesInstanceInNewRepo(t *testing.T) {
	h := newTemplateTestHome(t)
	parent := h.appConfig.RepoTemplatesDir
	calls := fakeGH(t, func(dir string, args []string) ([]byte, error) {
		makeClone(t, dir, "fresh", false)
		return nil, nil
	})
	h.Update(ghCheckDoneMsg{ok: true})

	h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	h.textInputOverlay.SetSize(80, 10)
	assert.Contains(t, h.textInputOverlay.Render(), "ctrl+t new repo from template")

	h.handleKeyPress(ctrlT())
	require.Equal(t, stateNewRepo, h.state)

	h.handleKeyPress(tea.KeyMsg{Type: tea.KeyTab}) // select Rust
	h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("fresh")})
	_, cmd := h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	require.NotNil(t, cmd)
	assert.True(t, h.repoCreating)

	// Keys are ignored while gh runs.
	h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEsc})
	assert.Equal(t, stateNewRepo, h.state)

	msg := cmd()
	assert.Equal(t, []string{"repo", "create", "fresh", "--template", "me/rust", "--private", "--clone"}, (*calls)[0])
	h.Update(msg)

	assert.False(t, h.repoCreating)
	assert.Equal(t, stateNew, h.state)
	require.Equal(t, 1, h.list.NumInstances())
	instance := h.list.GetInstances()[0]
	assert.Equal(t, filepath.Join(parent, "fresh"), instance.Path)
	assert.Equal(t, "fresh", instance.Title)
}

func TestRepoTemplateCreateErrorKeepsOverlayOpen(t *testing.T) {
	h := newTemplateTestHome(t)
	fakeGH(t, func(dir string, args []string) ([]byte, error) {
		return []byte("name already exists"), errors.New("exit 1")
	})
	h.Update(ghCheckDoneMsg{ok: true})
	h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	h.handleKeyPress(ctrlT())
	h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("taken")})
	_, cmd := h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	h.Update(cmd())

	assert.Equal(t, stateNewRepo, h.state)
	h.textInputOverlay.SetSize(80, 10)
	assert.Contains(t, h.textInputOverlay.Render(), "name already exists")
	assert.Equal(t, 0, h.list.NumInstances())
}

func TestRepoTemplateInvalidNameShowsError(t *testing.T) {
	h := newTemplateTestHome(t)
	calls := fakeGH(t, func(dir string, args []string) ([]byte, error) { return nil, nil })
	h.Update(ghCheckDoneMsg{ok: true})
	h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	h.handleKeyPress(ctrlT())
	h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("bad/name")})
	_, cmd := h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})

	assert.Nil(t, cmd)
	assert.False(t, h.repoCreating)
	h.textInputOverlay.SetSize(80, 10)
	assert.True(t, strings.Contains(h.textInputOverlay.Render(), "invalid repo name"))
	assert.Empty(t, *calls)
}

func TestRepoTemplateEscReturnsToDirectoryOverlay(t *testing.T) {
	h := newTemplateTestHome(t)
	h.Update(ghCheckDoneMsg{ok: true})
	h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("N")})
	h.handleKeyPress(ctrlT())
	h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEsc})

	assert.Equal(t, stateNewDir, h.state)
	assert.True(t, h.promptAfterName, "N vs n choice is preserved")
}
