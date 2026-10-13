package app

import (
	"claude-squad/config"
	"claude-squad/log"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// refreshInterval is how often the TUI checks whether another process
// (another TUI, `cs new`) has written the state file.
const refreshInterval = time.Second

// refreshTickMsg asks the TUI to pick up sessions created by other processes.
type refreshTickMsg struct{}

func refreshTickCmd() tea.Cmd {
	return tea.Tick(refreshInterval, func(time.Time) tea.Msg { return refreshTickMsg{} })
}

// fileStamp identifies a version of the state file. Size is included because
// two writes within the filesystem's timestamp resolution share a mtime.
type fileStamp struct {
	modTime time.Time
	size    int64
}

// stateFileStamp returns the state file's current stamp. A missing file has
// the zero stamp.
func stateFileStamp() (fileStamp, error) {
	path, err := config.StateFilePath()
	if err != nil {
		return fileStamp{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fileStamp{}, nil
		}
		return fileStamp{}, err
	}
	return fileStamp{modTime: info.ModTime(), size: info.Size()}, nil
}

// refreshInstances adds to the list any session another process has saved
// since the last check. It is additive: sessions removed by other processes
// stay in the list. It runs on the main loop so it cannot interleave with the
// new-session wizard, and it is skipped while the wizard or an overlay is
// open, since the wizard treats the last list entry as the one being created.
func (m *home) refreshInstances() tea.Cmd {
	if m.state != stateDefault {
		return nil
	}
	stamp, err := stateFileStamp()
	if err != nil {
		log.WarningLog.Printf("could not stat state file: %v", err)
		return nil
	}
	if stamp == m.stateFileSeen {
		return nil
	}
	m.stateFileSeen = stamp

	known := make(map[string]bool, m.list.NumInstances())
	for _, inst := range m.list.GetInstances() {
		known[inst.Title] = true
	}
	instances, err := m.storage.LoadNewInstances(func(title string) bool { return known[title] })
	if err != nil {
		return m.handleError(err)
	}
	if len(instances) == 0 {
		return nil
	}
	for _, instance := range instances {
		m.list.AddInstance(instance)()
		if m.autoYes {
			instance.AutoYes = true
		}
	}
	return m.instanceChanged()
}
