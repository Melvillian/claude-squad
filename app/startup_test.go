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
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scriptedCapture returns the scripted pane contents in order, repeating the
// last one forever, and counts calls.
func scriptedCapture(script ...string) (capture func() (string, error), calls *int) {
	n := 0
	return func() (string, error) {
		i := n
		if i >= len(script) {
			i = len(script) - 1
		}
		n++
		return script[i], nil
	}, &n
}

func TestWaitUntilIdleReturnsOnceContentStopsChanging(t *testing.T) {
	capture, calls := scriptedCapture("", "", "loading", "loading.", "loading..", "ready", "ready")
	noDialog := func(string) bool { return false }

	content, err := waitUntilIdle(capture, noDialog, time.Millisecond, 5*time.Millisecond, time.Second)
	require.NoError(t, err)
	assert.Equal(t, "ready", content)
	assert.GreaterOrEqual(t, *calls, 6, "must not return before content settles")
}

func TestWaitUntilIdleIgnoresBlankScreens(t *testing.T) {
	// A blank pane is what a program shows before it has drawn anything; it
	// must not count as idle even though it is "stable".
	capture, _ := scriptedCapture("", " \n \n", "", "ready")
	noDialog := func(string) bool { return false }

	content, err := waitUntilIdle(capture, noDialog, time.Millisecond, 3*time.Millisecond, time.Second)
	require.NoError(t, err)
	assert.Equal(t, "ready", content)
}

func TestWaitUntilIdleTimesOut(t *testing.T) {
	n := 0
	capture := func() (string, error) { n++; return fmt.Sprintf("frame %d", n), nil }
	noDialog := func(string) bool { return false }

	_, err := waitUntilIdle(capture, noDialog, time.Millisecond, 5*time.Millisecond, 30*time.Millisecond)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timed out")
}

func TestWaitUntilIdleDismissesDialogThenWaitsAgain(t *testing.T) {
	const dialog = "Is this a project you created or one you trust?\n ❯ No, exit"
	var mu sync.Mutex
	dismissed := 0
	capture := func() (string, error) {
		mu.Lock()
		defer mu.Unlock()
		if dismissed == 0 {
			return dialog, nil
		}
		return "ready", nil
	}
	dismiss := func(content string) bool {
		mu.Lock()
		defer mu.Unlock()
		if strings.Contains(content, "one you trust") {
			dismissed++
			return true
		}
		return false
	}

	content, err := waitUntilIdle(capture, dismiss, time.Millisecond, 5*time.Millisecond, time.Second)
	require.NoError(t, err)
	assert.Equal(t, "ready", content)
	assert.Equal(t, 1, dismissed)
}

func TestWaitUntilIdleDoesNotTreatLingeringDialogAsReady(t *testing.T) {
	const dialog = "Is this a project you created or one you trust?\n ❯ No, exit"
	// The dialog stays on screen for several polls after it is dismissed.
	capture, _ := scriptedCapture(dialog, dialog, dialog, dialog, dialog, dialog, dialog, dialog, "ready")
	dismissed := 0
	dismiss := func(content string) bool {
		if strings.Contains(content, "one you trust") {
			dismissed++
			return true
		}
		return false
	}

	content, err := waitUntilIdle(capture, dismiss, time.Millisecond, 2*time.Millisecond, time.Second)
	require.NoError(t, err)
	assert.Equal(t, "ready", content)
	assert.Equal(t, 1, dismissed, "dialog is dismissed once, not on every poll")
}

func TestWaitUntilIdleTimesOutIfDialogNeverGoesAway(t *testing.T) {
	const dialog = "Is this a project you created or one you trust?\n ❯ No, exit"
	capture, _ := scriptedCapture(dialog)
	dismiss := func(string) bool { return true }

	_, err := waitUntilIdle(capture, dismiss, time.Millisecond, 2*time.Millisecond, 20*time.Millisecond)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timed out")
}

// startedTestInstance starts a claude instance whose pane content comes from
// nextPane and whose typed keys are recorded in the returned file.
func startedTestInstance(t *testing.T, nextPane func() string) (*session.Instance, string) {
	t.Helper()
	repo := initTestRepo(t)
	ptyPath := filepath.Join(t.TempDir(), "pty")
	instance, err := session.NewInstance(session.InstanceOptions{Title: "su", Path: repo, Program: "claude"})
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
			if strings.Contains(cmd.String(), "capture-pane") {
				return []byte(nextPane()), nil
			}
			return []byte("output"), nil
		},
	}))
	require.NoError(t, instance.Start(true))
	t.Cleanup(func() { _ = instance.Kill() })
	return instance, ptyPath
}

func TestTrustDialogIsAcceptedNotExited(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, tc := range []struct {
		name, pane, want string
	}{
		{"current dialog defaults to No, exit", " Is this a project you created or one you trust?\n ❯ No, exit\n   Yes, I trust this folder", "\x1b[B\r"},
		{"legacy dialog defaults to Yes", " Do you trust the files in this folder?\n ❯ Yes, proceed", "\r"},
		{"no dialog", "❯ Try \"refactor <filepath>\"", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			instance, ptyPath := startedTestInstance(t, func() string { return tc.pane })
			handled := instance.CheckAndHandleTrustPrompt()
			assert.Equal(t, tc.want != "", handled)
			written, err := os.ReadFile(ptyPath)
			require.NoError(t, err)
			assert.Equal(t, tc.want, string(written))
		})
	}
}

func TestSendStartupPromptWaitsForReadyAndDismissesDialogs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	orig := startupTiming
	startupTiming = startupTimingSpec{poll: time.Millisecond, stableFor: 5 * time.Millisecond, timeout: time.Second}
	t.Cleanup(func() { startupTiming = orig })

	const dialog = " Is this a project you created or one you trust?\n ❯ No, exit\n   Yes, I trust this folder"
	const rcModal = " This session is available in the Claude mobile app\n   Disconnect this session\n   Show QR code\n ❯ Continue"
	const ready = "❯ Try \"refactor <filepath>\""
	var mu sync.Mutex
	frames := []string{"", "", dialog, dialog, dialog}
	i := 0
	ptyPath := ""
	// The fake screen reacts to what has been typed so far, like claude would.
	nextPane := func() string {
		mu.Lock()
		defer mu.Unlock()
		typed, _ := os.ReadFile(ptyPath)
		switch {
		case strings.Contains(string(typed), "hello\r"):
			return "❯ hello\n⏺ hi there"
		case strings.Contains(string(typed), "\x1b[B\r\r"):
			return ready // remote-control confirmation dismissed
		case strings.Contains(string(typed), "\x1b[B\r"):
			return rcModal // trust accepted
		}
		if i < len(frames) {
			i++
			return frames[i-1]
		}
		return dialog
	}
	instance, p := startedTestInstance(t, nextPane)
	ptyPath = p

	require.NoError(t, sendStartupPrompt(instance, "hello"))

	written, err := os.ReadFile(ptyPath)
	require.NoError(t, err)
	assert.Equal(t, "\x1b[B\r"+"\r"+"hello\r", string(written),
		"accept trust, dismiss the remote-control confirmation, send prompt")
}

func TestDismissStartupDialog(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, tc := range []struct {
		name, pane, want string
	}{
		{"remote-control confirmation", " This session is available in the Claude mobile app\n   Disconnect this session\n ❯ Continue", "\r"},
		{"trust dialog", " Is this a project you created or one you trust?\n ❯ No, exit", "\x1b[B\r"},
		{"ready screen", "❯ ", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			instance, ptyPath := startedTestInstance(t, func() string { return tc.pane })
			handled := dismissStartupDialog(instance, tc.pane)
			assert.Equal(t, tc.want != "", handled)
			written, err := os.ReadFile(ptyPath)
			require.NoError(t, err)
			assert.Equal(t, tc.want, string(written))
		})
	}
}
