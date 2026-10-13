package app

import (
	"claude-squad/log"
	"claude-squad/session"
	"fmt"
	"strings"
	"time"
)

// startupTimingSpec controls how the TUI decides a freshly started program is
// ready for input: the pane is polled every poll, counts as ready once its
// content has been non-blank and unchanged for stableFor, and timeout bounds
// the whole wait.
type startupTimingSpec struct {
	poll      time.Duration
	stableFor time.Duration
	timeout   time.Duration
}

// startupTiming is a variable so tests can shrink the durations.
var startupTiming = startupTimingSpec{
	poll:      250 * time.Millisecond,
	stableFor: time.Second,
	timeout:   60 * time.Second,
}

// remoteControlModalMarker is text from the confirmation claude shows after
// remoteControlCommand ("This session is available in the Claude mobile app
// ... Disconnect this session / Show QR code / Continue"). Anything typed
// while it is up is dropped, and Enter selects Continue.
const remoteControlModalMarker = "Disconnect this session"

// waitForProgramReady blocks until the instance's program has drawn a screen
// that is no longer changing, dismissing claude's startup dialogs (workspace
// trust, the remote-control confirmation) if they are shown on the way. It
// returns the screen it settled on.
func waitForProgramReady(instance *session.Instance) (string, error) {
	content, err := waitUntilIdle(instance.Preview, func(content string) bool {
		return dismissStartupDialog(instance, content)
	}, startupTiming.poll, startupTiming.stableFor, startupTiming.timeout)
	if err != nil {
		return "", fmt.Errorf("waiting for %s to start: %w", instance.Program, err)
	}
	return content, nil
}

// dismissStartupDialog dismisses a known claude dialog shown in content and
// reports whether it did.
func dismissStartupDialog(instance *session.Instance, content string) bool {
	if instance.CheckAndHandleTrustPrompt() {
		return true
	}
	if instance.IsClaude() && strings.Contains(content, remoteControlModalMarker) {
		if err := instance.PressEnter(); err != nil {
			log.ErrorLog.Printf("could not dismiss remote-control dialog: %v", err)
			return false
		}
		return true
	}
	return false
}

// waitForScreenChange waits until the pane no longer shows before, so a
// caller about to exit knows its last keystrokes were delivered. A timeout is
// only logged: the keys were sent, we just could not confirm they were seen.
func waitForScreenChange(instance *session.Instance, before string, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		content, err := instance.Preview()
		if err != nil || content != before {
			return
		}
		time.Sleep(startupTiming.poll)
	}
	log.WarningLog.Printf("screen of %s did not change within %s of sending the prompt", instance.Title, timeout)
}

// waitUntilIdle polls capture until the content has been non-blank and
// unchanged for stableFor, returning that content. Each time the content
// changes it is offered to dismissDialog; when that reports it handled a
// dialog the stability clock restarts so the screen that follows is waited
// for too. Fails after timeout.
func waitUntilIdle(capture func() (string, error), dismissDialog func(content string) bool,
	poll, stableFor, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	var last, dismissed string
	var stableSince time.Time
	for {
		content, err := capture()
		if err != nil {
			return "", err
		}
		now := time.Now()
		switch {
		case strings.TrimSpace(content) == "":
			stableSince = time.Time{}
		case content != last:
			stableSince = now
			if dismissDialog(content) {
				// The dialog may stay on screen until the program processes
				// the keys; never treat that screen as the ready one.
				dismissed = content
				stableSince = time.Time{}
			}
		case content == dismissed:
			stableSince = time.Time{}
		case stableSince.IsZero():
			stableSince = now
		case now.Sub(stableSince) >= stableFor:
			return content, nil
		}
		last = content
		if now.After(deadline) {
			return "", fmt.Errorf("timed out after %s waiting for the program to become ready", timeout)
		}
		time.Sleep(poll)
	}
}
