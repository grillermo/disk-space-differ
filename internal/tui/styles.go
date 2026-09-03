package tui

import "github.com/charmbracelet/lipgloss"

// Colours are adaptive so the report stays legible on light and dark terminals.
// Growth is warm because it costs space; reclaimed space is cool and calm.
var (
	colGrew    = lipgloss.AdaptiveColor{Light: "#B4331B", Dark: "#FF8A65"}
	colShrank  = lipgloss.AdaptiveColor{Light: "#1B6E3C", Dark: "#7FD99A"}
	colAccent  = lipgloss.AdaptiveColor{Light: "#2B5FBF", Dark: "#7FB3FF"}
	colMuted   = lipgloss.AdaptiveColor{Light: "#6B7280", Dark: "#8A8F98"}
	colWarn    = lipgloss.AdaptiveColor{Light: "#8A5A00", Dark: "#F2C14E"}
	colSelBG   = lipgloss.AdaptiveColor{Light: "#DCE6F7", Dark: "#2A3550"}
	colDanger  = lipgloss.AdaptiveColor{Light: "#B00020", Dark: "#FF6B6B"}
	colHeadFG  = lipgloss.AdaptiveColor{Light: "#111827", Dark: "#E6E8EB"}
	colSurface = lipgloss.AdaptiveColor{Light: "#F3F4F6", Dark: "#1C2333"}
)

var (
	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(colHeadFG)
	subtleTxt  = lipgloss.NewStyle().Foreground(colMuted)
	accentTxt  = lipgloss.NewStyle().Foreground(colAccent)
	grewTxt    = lipgloss.NewStyle().Foreground(colGrew)
	shrankTxt  = lipgloss.NewStyle().Foreground(colShrank)
	warnTxt    = lipgloss.NewStyle().Foreground(colWarn)
	dangerTxt  = lipgloss.NewStyle().Foreground(colDanger).Bold(true)

	headerRow = lipgloss.NewStyle().
			Bold(true).
			Foreground(colMuted).
			BorderStyle(lipgloss.NormalBorder()).
			BorderBottom(true).
			BorderForeground(colMuted)

	selectedRow = lipgloss.NewStyle().Background(colSelBG).Bold(true)

	summaryBox = lipgloss.NewStyle().
			Padding(0, 1).
			Background(colSurface)

	confirmBox = lipgloss.NewStyle().
			Padding(1, 2).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colDanger)

	helpBar = lipgloss.NewStyle().Foreground(colMuted).Padding(0, 1)
)

func deltaStyle(delta int64) lipgloss.Style {
	if delta < 0 {
		return shrankTxt
	}
	if delta > 0 {
		return grewTxt
	}
	return subtleTxt
}
