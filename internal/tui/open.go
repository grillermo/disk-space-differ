package tui

import (
	"os/exec"
	"runtime"

	tea "github.com/charmbracelet/bubbletea"
)

// openInFileManager reveals a path in the desktop file manager. Failures are
// deliberately silent: the report is still usable without it.
func openInFileManager(path string) tea.Cmd {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", path)
	case "windows":
		cmd = exec.Command("explorer", path)
	default:
		cmd = exec.Command("xdg-open", path)
	}

	return func() tea.Msg {
		_ = cmd.Start()
		return nil
	}
}
