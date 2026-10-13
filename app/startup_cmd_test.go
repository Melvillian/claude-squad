package app

import (
	"claude-squad/cmd/cmd_test"
	"claude-squad/session"
	"claude-squad/session/tmux"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingPtyFactory hands out a single temp file as the PTY so tests can read
// back what was written to it.
type recordingPtyFactory struct {
	path string
}

func (f recordingPtyFactory) Start(cmd *exec.Cmd) (*os.File, error) {
	return os.OpenFile(f.path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0644)
}

func (f recordingPtyFactory) Close() {}

func TestSendStartupMessagesCmd(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	orig := startupTiming
	startupTiming = startupTimingSpec{poll: time.Millisecond, stableFor: 5 * time.Millisecond, timeout: time.Second}
	t.Cleanup(func() { startupTiming = orig })
	repo := initTestRepo(t)
	ptyPath := filepath.Join(t.TempDir(), "pty")

	instance, err := session.NewInstance(session.InstanceOptions{
		Title:   "su",
		Path:    repo,
		Program: "claude",
	})
	require.NoError(t, err)

	created := false
	instance.SetTmuxSession(tmux.NewTmuxSessionWithDeps("su", "claude", recordingPtyFactory{ptyPath}, cmd_test.MockCmdExec{
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
	require.NoError(t, instance.Start(true))
	t.Cleanup(func() { _ = instance.Kill() })

	assert.Nil(t, sendStartupMessagesCmd(instance), "nothing to send")

	instance.Prompt = "hello"
	cmd := sendStartupMessagesCmd(instance)
	require.NotNil(t, cmd)
	assert.Empty(t, instance.Prompt)

	assert.Nil(t, cmd())

	written, err := os.ReadFile(ptyPath)
	require.NoError(t, err)
	assert.Equal(t, "hello\r", string(written))
}
