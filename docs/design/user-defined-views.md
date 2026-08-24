# Design: User-Defined "Break Views" in claude-squad

**Status:** Proposed (not yet implemented)
**Date:** 2026-08-08
**Author:** Melvillian + Claude

## Problem / Goal

While coding agents run, the user is idle in the instance list view. We want a
view, reachable with one keystroke from the main process list, that displays a
**user-defined** interactive TUI program — e.g. a snake game, space invaders,
an SRS flashcard reviewer, or a Linear task-review TUI.

Key requirement: the view must be *user-defined* (bring-your-own binary or
script), not a feature compiled into claude-squad per game.

## Architecture context (as of commit 2dd388e)

claude-squad is a Go Bubble Tea app with a deliberately flat structure:

- **One top-level `tea.Model`**: `home` in `app/app.go`. All other UI
  components are plain structs that render to strings — none are `tea.Model`s.
- **Event loop**: `home.Update()` handles all messages; `home.handleKeyPress()`
  is the single keyboard dispatcher, gated by a state machine
  (`stateDefault`, `stateNew`, `statePrompt`, `stateHelp`, `stateConfirm`).
  Keys are declared centrally in `keys/keys.go` (`GlobalKeyStringsMap`,
  `GlobalkeyBindings`).
- **Layout**: `home.View()` composes lipgloss strings: `ui.List` (left 30%),
  `ui.TabbedWindow` (right 70%; Preview / Diff / Terminal tabs), `ui.Menu`
  (bottom shortcut bar), `ui.ErrBox`. Modals are string-composited via
  `overlay.PlaceOverlay` (`ui/overlay/overlay.go`).
- **Sessions**: each instance = tmux session + git worktree
  (`session/instance.go`, `session/tmux/`, `session/git/`). The TUI never
  embeds a terminal emulator — the Preview pane polls `tmux capture-pane`
  every 100ms (`previewTickMsg`); a 500ms metadata ticker
  (`tickUpdateMetadataCmd`) computes diffs/status in background goroutines.
- **Attach** (`session/tmux/tmux.go`, `Attach()` around line 258): full-screen
  takeover. One goroutine copies the tmux pty to stdout; another watches stdin
  for `Ctrl+q` (ASCII 17) to detach; it also nukes the first ~50ms of stdin to
  swallow terminal control sequences, and monitors window size. Bubble Tea
  keeps running underneath and repaints after detach via `tea.WindowSize()`.
- **Precedent that matters**: the Terminal tab (`ui/terminal.go`,
  `TerminalPane`) already runs an arbitrary program (`$SHELL`) in its own tmux
  session per instance, shows a read-only polled capture in the pane, and
  attaches full-screen on Enter. This is ~90% of the machinery needed.

## Options considered

### Option A (RECOMMENDED): user-defined program in a dedicated tmux session, entered via full-screen attach

Run whatever command the user configured in its own tmux session (not tied to
any instance/worktree), and reuse the existing full-screen attach flow to
interact with it. `Ctrl+q` returns to the list.

Why it wins:
- Any TUI program works with **zero Go code per view** — genuinely user-defined.
- Full keyboard input for free via attach.
- State persists between visits (tmux keeps the process alive) — important for
  an SRS session or a half-finished game.
- ~100–150 lines, all following existing patterns.

### Option B (rejected for v1): native Bubble Tea game model

New `stateGame` + a snake `tea.Model` compiled into the binary. Clean and
in-process, and would allow rich integration (status bar over the game), but
every new view requires writing Go and recompiling — the opposite of
user-defined. Also introduces nested-`tea.Model` composition into a codebase
that deliberately avoids it.

### Option C (rejected): fourth tab in `TabbedWindow`

Tabs are per-selected-instance, and pane content is a **read-only 100ms-poll
capture** — keyboard input never reaches it (arrows, `j`/`k`, `q` are globally
bound in `handleKeyPress`). A game there is unplayable; you'd need full-screen
attach anyway.

## Recommended implementation sketch

1. **Config** (`config/config.go`): add
   ```json
   "user_views": [{ "name": "snake", "program": "nsnake" }]
   ```
   Mirror the existing `Profile` type (`UserView {Name, Program string}`).
   A single `"break_program"` string is acceptable for v1.

2. **Keybinding** (`keys/keys.go`): add `KeyUserView` bound to `g` (unused;
   taken keys: up/k, down/j, shift+up/down, J, K, N, enter, o, n, D, q, tab,
   c, r, p, ?). Add to `GlobalKeyStringsMap`, `GlobalkeyBindings`, and the
   bottom menu in `ui/menu.go`.

3. **Session manager**: a small struct held by `home` (or a stripped-down
   sibling of `TerminalPane`) holding one `*tmux.TmuxSession` per configured
   view, keyed by view name — independent of instances/worktrees. First use:
   `tmux.NewTmuxSession("userview_"+name, program)` + `Start(cwd)`. Later
   uses: `DoesSessionExist()` → `Restore()`. Call `SetDetachedSize` on resize.

4. **Handler** (`app/app.go`, `stateDefault` branch of `handleKeyPress`): on
   `KeyUserView`, ensure the session exists, then reuse the exact attach flow
   `KeyEnter` uses (`showHelpScreen` callback → `Attach()` → wait on the
   returned channel → back to `stateDefault` + redraw). `Ctrl+q` detach,
   stdin-nuking, and window-size monitoring already work.

5. **Multiple views**: when >1 view is configured, `g` opens a picker overlay —
   reuse the `ui/overlay/profilePicker.go` precedent; Enter attaches.

6. **Cleanup**: decide whether user-view sessions die on quit (`Close()` in
   `handleQuit`) or persist across app restarts (nicer for SRS). Check naming
   interaction with `CleanupSessions` / `clean.sh`, which sweep
   `claudesquad_`-prefixed tmux sessions (`toClaudeSquadTmuxName`).

## Enhancements unlocked by this design

- **Auto-return when an agent needs you** (the killer feature): the attach
  loop reads stdin in Go and `Detach()` is callable from outside; the 500ms
  metadata ticker knows when an instance flips to `Ready` / hits a prompt.
  Wire them together: pop the user out of the game when any agent finishes.
  Only possible because attach is mediated by our process, not raw
  `tmux attach`.
- **Read-only preview pane** of the game/view before attaching: reuse
  `TerminalPane`'s capture-poll rendering as-is.

## Caveats

- While attached, Bubble Tea runs but doesn't paint: error toasts and status
  changes are invisible until detach (same as attaching to an agent today).
  Auto-return is the real fix.
- The game process consumes resources while backgrounded in tmux; fine for
  small TUIs, worth noting for anything heavy.
