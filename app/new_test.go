package app

import (
	"claude-squad/cmd/cmd_test"
	"claude-squad/config"
	"claude-squad/session"
	"claude-squad/session/tmux"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestConfig() *config.Config {
	return &config.Config{
		DefaultProgram: "claude",
		RepoTemplates: []config.RepoTemplate{
			{Name: "TypeScript", Repo: "me/ts"},
			{Name: "Rust", Repo: "me/rust"},
		},
	}
}

func TestResolveTemplate(t *testing.T) {
	cfg := newTestConfig()

	repo, err := resolveTemplate(cfg, "Rust")
	require.NoError(t, err)
	assert.Equal(t, "me/rust", repo)

	repo, err = resolveTemplate(cfg, "someone/other")
	require.NoError(t, err)
	assert.Equal(t, "someone/other", repo, "owner/repo is used as-is")

	_, err = resolveTemplate(cfg, "Go")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "TypeScript")
	assert.Contains(t, err.Error(), "Rust")

	_, err = resolveTemplate(&config.Config{}, "Go")
	require.Error(t, err)
}

func newTestNewStorage(t *testing.T) *session.Storage {
	t.Helper()
	storage, err := session.NewStorage(config.LoadState())
	require.NoError(t, err)
	return storage
}

func TestNewInstanceFromOptionsValidation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := initTestRepo(t)
	cfg := newTestConfig()
	storage := newTestNewStorage(t)
	ctx := context.Background()

	_, err := newInstanceFromOptions(ctx, cfg, storage, NewOptions{Path: repo})
	require.Error(t, err, "empty title")
	assert.Contains(t, err.Error(), "title")

	_, err = newInstanceFromOptions(ctx, cfg, storage, NewOptions{Path: repo, Title: strings.Repeat("x", 33)})
	require.Error(t, err, "long title")
	assert.Contains(t, err.Error(), "32")

	_, err = newInstanceFromOptions(ctx, cfg, storage, NewOptions{Path: t.TempDir(), Title: "ok"})
	require.Error(t, err, "not a git repo")
	assert.Contains(t, err.Error(), "not a git repository")

	_, err = newInstanceFromOptions(ctx, cfg, storage, NewOptions{Path: repo, Title: "ok", Template: "Rust"})
	require.Error(t, err, "template without repo name")
	assert.Contains(t, err.Error(), "repo-name")

	_, err = newInstanceFromOptions(ctx, cfg, storage, NewOptions{Path: repo, Title: "ok", RepoName: "x"})
	require.Error(t, err, "repo name without template")
	assert.Contains(t, err.Error(), "template")

	_, err = newInstanceFromOptions(ctx, cfg, storage, NewOptions{Path: repo, Title: "ok", Template: "Rust", RepoName: "x"})
	require.Error(t, err, "template and path are exclusive")
	assert.Contains(t, err.Error(), "path")

	// A title already saved by another process is rejected before anything
	// is created, since storage keys records by title.
	taken, err := session.FromInstanceData(session.InstanceData{
		Title: "taken", Path: repo, Status: session.Paused, Program: "claude",
	})
	require.NoError(t, err)
	require.NoError(t, storage.SaveInstances([]*session.Instance{taken}))
	_, err = newInstanceFromOptions(ctx, cfg, storage, NewOptions{Path: repo, Title: "taken"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestNewInstanceFromOptionsDefaultsAndOverrides(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := initTestRepo(t)
	cfg := newTestConfig()
	storage := newTestNewStorage(t)
	ctx := context.Background()

	// Defaults: cwd as repo, config program.
	wd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(repo))
	t.Cleanup(func() { _ = os.Chdir(wd) })

	instance, err := newInstanceFromOptions(ctx, cfg, storage, NewOptions{Title: "defaults"})
	require.NoError(t, err)
	assert.Equal(t, "defaults", instance.Title)
	assert.Equal(t, mustEvalSymlinks(t, repo), mustEvalSymlinks(t, instance.Path))
	assert.Equal(t, "claude", instance.Program)
	assert.Empty(t, instance.Prompt)
	assert.False(t, instance.Started())

	// Overrides.
	instance, err = newInstanceFromOptions(ctx, cfg, storage, NewOptions{
		Path:    repo,
		Title:   "custom",
		Prompt:  "do the thing",
		Program: "aider",
	})
	require.NoError(t, err)
	assert.Equal(t, "aider", instance.Program)
	assert.Equal(t, "do the thing", instance.Prompt)
}

func mustEvalSymlinks(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	require.NoError(t, err)
	return resolved
}

func TestNewInstanceFromOptionsCreatesRepoFromTemplate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := newTestConfig()
	cfg.RepoTemplatesDir = t.TempDir()
	storage := newTestNewStorage(t)
	calls := fakeGH(t, func(dir string, args []string) ([]byte, error) {
		makeClone(t, dir, "fresh", false)
		return nil, nil
	})

	instance, err := newInstanceFromOptions(context.Background(), cfg, storage, NewOptions{
		Template: "Rust",
		RepoName: "fresh",
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"repo", "create", "fresh", "--template", "me/rust", "--private", "--clone"}, (*calls)[0])
	assert.Equal(t, filepath.Join(cfg.RepoTemplatesDir, "fresh"), instance.Path)
	assert.Equal(t, "fresh", instance.Title, "title defaults to the repo name")

	// An explicit title still wins.
	calls = fakeGH(t, func(dir string, args []string) ([]byte, error) {
		makeClone(t, dir, "other", false)
		return nil, nil
	})
	instance, err = newInstanceFromOptions(context.Background(), cfg, storage, NewOptions{
		Template: "me/rust",
		RepoName: "other",
		Title:    "named",
	})
	require.NoError(t, err)
	assert.Equal(t, "named", instance.Title)
}

func TestStartInstanceHeadlessStartsSavesAndSendsPrompt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := initTestRepo(t)
	ptyPath := filepath.Join(t.TempDir(), "pty")
	storage := newTestNewStorage(t)

	instance, err := newInstanceFromOptions(context.Background(), newTestConfig(), storage, NewOptions{
		Path:   repo,
		Title:  "headless",
		Prompt: "hello",
	})
	require.NoError(t, err)

	created := false
	instance.SetTmuxSession(tmux.NewTmuxSessionWithDeps("headless", "claude", recordingPtyFactory{ptyPath}, cmd_test.MockCmdExec{
		RunFunc: func(cmd *exec.Cmd) error {
			if strings.Contains(cmd.String(), "has-session") && !created {
				created = true
				return fmt.Errorf("session does not exist")
			}
			return nil
		},
		OutputFunc: func(cmd *exec.Cmd) ([]byte, error) {
			return []byte("output"), nil
		},
	}))
	t.Cleanup(func() { _ = instance.Kill() })

	require.NoError(t, startInstanceHeadless(instance, storage))
	assert.True(t, instance.Started())

	stored := make([]session.InstanceData, 0)
	require.NoError(t, json.Unmarshal(config.LoadState().GetInstances(), &stored))
	require.Len(t, stored, 1)
	assert.Equal(t, "headless", stored[0].Title)

	written, err := os.ReadFile(ptyPath)
	require.NoError(t, err)
	assert.Equal(t, "hello\r", string(written))
	assert.Empty(t, instance.Prompt, "prompt is consumed once sent")
}
