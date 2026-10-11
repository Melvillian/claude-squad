package app

import (
	"claude-squad/config"
	"claude-squad/ui/overlay"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// repoTemplateKey opens the "new repo from template" overlay from the directory overlay.
const repoTemplateKey = "ctrl+t"

// ghCheckTimeout bounds how long `gh auth status` may take before the
// feature is treated as unavailable.
const ghCheckTimeout = 10 * time.Second

// cloneRetries is how many times an empty clone is re-cloned. GitHub copies
// template contents asynchronously, so `gh repo create --clone` can clone the
// new repo before it has any commits.
const cloneRetries = 5

var (
	// runCmd runs a command in dir and returns its combined output. Replaced in tests.
	runCmd = func(ctx context.Context, dir string, name string, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Dir = dir
		return cmd.CombinedOutput()
	}
	// cloneRetryDelay is the wait between attempts to clone a repo with commits.
	cloneRetryDelay = 2 * time.Second

	repoNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
)

// ghCheckDoneMsg reports whether the gh CLI is installed and authenticated.
type ghCheckDoneMsg struct {
	ok bool
}

// repoCreatedMsg is sent when a repo created from a template has been cloned.
type repoCreatedMsg struct {
	path string
	err  error
}

// checkGitHubCLI checks in the background whether gh is usable.
func checkGitHubCLI(ctx context.Context) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, ghCheckTimeout)
		defer cancel()
		_, err := runCmd(ctx, "", "gh", "auth", "status")
		return ghCheckDoneMsg{ok: err == nil}
	}
}

// repoTemplates returns the configured repo templates.
func (m *home) repoTemplates() []config.RepoTemplate {
	if m.appConfig == nil {
		return nil
	}
	return m.appConfig.RepoTemplates
}

// repoTemplatesAvailable reports whether new repos can be created from templates:
// templates are configured and gh is installed and authenticated.
func (m *home) repoTemplatesAvailable() bool {
	return m.ghReady && len(m.repoTemplates()) > 0
}

// openRepoTemplateOverlay replaces the directory overlay with the overlay for
// choosing a template and naming the new repo.
func (m *home) openRepoTemplateOverlay() tea.Cmd {
	templates := m.repoTemplates()
	choices := make([]config.Profile, len(templates))
	for i, t := range templates {
		choices[i] = config.Profile{Name: t.Name, Program: t.Repo}
	}
	m.state = stateNewRepo
	m.textInputOverlay = overlay.NewTemplateInputOverlay(
		fmt.Sprintf("New private repo name (cloned into %s)", m.appConfig.GetRepoTemplatesDir()),
		"Template", choices)
	return tea.WindowSize()
}

// handleRepoTemplateKeyPress handles keys while the repo template overlay is open.
// Esc returns to the directory overlay; ctrl+c abandons the new session.
func (m *home) handleRepoTemplateKeyPress(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.repoCreating {
		// gh is running; ignore input until it finishes.
		return m, nil
	}
	if msg.String() == "ctrl+c" {
		return m, m.cancelDirOverlay()
	}
	shouldClose, _ := m.textInputOverlay.HandleKeyPress(msg)
	if !shouldClose {
		return m, nil
	}
	if m.textInputOverlay.IsCanceled() {
		m.newDirOverlay(m.promptAfterName)
		return m, tea.WindowSize()
	}

	name := strings.TrimSpace(m.textInputOverlay.GetValue())
	template := m.textInputOverlay.GetSelectedProgram()
	m.textInputOverlay.ResetSubmitted()
	if err := validateRepoName(name); err != nil {
		m.textInputOverlay.SetError(err.Error())
		return m, nil
	}

	m.repoCreating = true
	m.textInputOverlay.SetStatus(fmt.Sprintf("Creating %s from %s…", name, template))
	parentDir := m.appConfig.GetRepoTemplatesDir()
	ctx := m.ctx
	return m, func() tea.Msg {
		path, err := createRepoFromTemplate(ctx, parentDir, name, template)
		return repoCreatedMsg{path: path, err: err}
	}
}

// handleRepoCreated continues new-session creation in the freshly cloned repo,
// or shows the error in the repo template overlay so the name can be changed.
func (m *home) handleRepoCreated(msg repoCreatedMsg) (tea.Model, tea.Cmd) {
	m.repoCreating = false
	if m.state != stateNewRepo || m.textInputOverlay == nil {
		return m, nil
	}
	m.textInputOverlay.SetStatus("")
	if msg.err != nil {
		m.textInputOverlay.SetError(msg.err.Error())
		return m, nil
	}
	model, cmd := m.handleDirSubmit(msg.path)
	if m.state == stateNew {
		// Default the session title to the repo name.
		instance := m.list.GetInstances()[m.list.NumInstances()-1]
		if title := filepath.Base(msg.path); len(title) <= 32 {
			_ = instance.SetTitle(title)
		}
	}
	return model, cmd
}

// validateRepoName checks that name is usable both as a GitHub repo name and
// as a directory name.
func validateRepoName(name string) error {
	if name == "" {
		return fmt.Errorf("repo name cannot be empty")
	}
	if !repoNameRe.MatchString(name) || name == "." || name == ".." {
		return fmt.Errorf("invalid repo name %q: use letters, digits, '.', '-' and '_'", name)
	}
	return nil
}

// createRepoFromTemplate creates a private GitHub repo named name from
// template and clones it into parentDir, returning the clone's path.
func createRepoFromTemplate(ctx context.Context, parentDir, name, template string) (string, error) {
	if err := validateRepoName(name); err != nil {
		return "", err
	}
	parent, err := filepath.Abs(overlay.ExpandUserPath(parentDir))
	if err != nil {
		return "", fmt.Errorf("invalid repo directory %q: %w", parentDir, err)
	}
	if err := os.MkdirAll(parent, 0755); err != nil {
		return "", fmt.Errorf("failed to create %s: %w", parent, err)
	}
	path := filepath.Join(parent, name)
	if _, err := os.Stat(path); err == nil {
		return "", fmt.Errorf("%s already exists", path)
	}

	out, err := runCmd(ctx, parent, "gh", "repo", "create", name,
		"--template", template, "--private", "--clone")
	if err != nil {
		return "", fmt.Errorf("gh repo create failed: %s", strings.TrimSpace(string(out)))
	}

	for attempt := 0; ; attempt++ {
		if _, err := runCmd(ctx, path, "git", "rev-parse", "--verify", "HEAD"); err == nil {
			return path, nil
		}
		if attempt == cloneRetries {
			return "", fmt.Errorf("repo %s was created but its clone has no commits yet; "+
				"try again in a moment with `gh repo clone %s` in %s", name, name, parent)
		}
		// The clone is empty because GitHub hadn't finished copying the template.
		// Remove it and clone again.
		time.Sleep(cloneRetryDelay)
		if err := os.RemoveAll(path); err != nil {
			return "", fmt.Errorf("failed to remove empty clone %s: %w", path, err)
		}
		_, _ = runCmd(ctx, parent, "gh", "repo", "clone", name)
	}
}
