package tui

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/grillermo/disk-space-differ/internal/humanize"
	"github.com/grillermo/disk-space-differ/internal/model"
)

// Fixed column widths. The path column absorbs whatever space is left, since
// it is the column that benefits most from extra room.
const (
	colRank  = 4
	colDelta = 13
	colTag   = 4
	colTrend = 12
	colSize  = 11
	gutter   = 2

	// chromeLines counts the header, summary, column headings and help bar
	// that sit around the scrollable rows.
	chromeLines = 9
)

// View renders the current screen.
func (m *Model) View() string {
	switch m.state {
	case stateScanning:
		return m.viewScanning()
	case stateError:
		return m.viewError()
	case stateConfirmDelete:
		return m.viewConfirm()
	default:
		return m.viewTable()
	}
}

func (m *Model) viewScanning() string {
	root := ""
	if m.scanningRoot < len(m.roots) {
		root = m.roots[m.scanningRoot]
	}

	var b strings.Builder
	b.WriteString("\n  ")
	b.WriteString(m.spinner.View())
	b.WriteString(titleStyle.Render(" scanning "))
	b.WriteString(accentTxt.Render(prettyPath(root)))
	if len(m.roots) > 1 {
		b.WriteString(subtleTxt.Render(fmt.Sprintf("  (%d/%d)", m.scanningRoot+1, len(m.roots))))
	}
	b.WriteString("\n\n     ")
	b.WriteString(subtleTxt.Render(fmt.Sprintf(
		"%s items · %s",
		withThousands(m.progress.ItemCount), humanize.Bytes(m.progress.TotalUsage),
	)))
	if m.progress.CurrentItem != "" {
		b.WriteString("\n     ")
		b.WriteString(subtleTxt.Render(humanize.Truncate(m.progress.CurrentItem, max(20, m.width-8))))
	}
	b.WriteString("\n\n  ")
	b.WriteString(helpBar.Render("q cancel"))
	b.WriteString("\n")
	return b.String()
}

func (m *Model) viewError() string {
	return "\n  " + dangerTxt.Render("scan failed") + "\n\n  " +
		subtleTxt.Render(m.err.Error()) + "\n\n  " + helpBar.Render("q quit") + "\n"
}

func (m *Model) viewConfirm() string {
	target := m.deleteTarget

	body := strings.Join([]string{
		dangerTxt.Render("Permanently delete this directory?"),
		"",
		accentTxt.Render(prettyPath(target.Path)),
		subtleTxt.Render(fmt.Sprintf("%s will be freed · %s",
			humanize.Bytes(target.Usage), "this cannot be undone")),
		"",
		"  " + dangerTxt.Render("y") + subtleTxt.Render(" delete") +
			"    " + accentTxt.Render("any other key") + subtleTxt.Render(" cancel"),
	}, "\n")

	box := confirmBox.Render(body)
	if m.width > 0 && m.height > 0 {
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
	}
	return "\n" + box + "\n"
}

func (m *Model) viewTable() string {
	var b strings.Builder

	b.WriteString(m.renderHeader())
	b.WriteString(m.renderSummaries())
	b.WriteString("\n")

	pathWidth := m.pathWidth()
	b.WriteString(m.renderColumnHeadings(pathWidth))

	if len(m.rows) == 0 {
		b.WriteString("\n  " + subtleTxt.Render("nothing changed since the last run") + "\n")
	} else {
		visible := m.visibleRows()
		end := min(m.offset+visible, len(m.rows))
		for i := m.offset; i < end; i++ {
			b.WriteString(m.renderRow(i, pathWidth))
		}
	}

	b.WriteString(m.renderFooter())
	return b.String()
}

func (m *Model) renderHeader() string {
	left := titleStyle.Render("disk-space-differ")

	var right string
	if len(m.results) > 0 && m.results[0].Previous != nil {
		right = subtleTxt.Render("compared with " + humanize.Since(m.results[0].Previous.StartedAt))
	} else {
		right = warnTxt.Render("first run · baseline recorded")
	}

	return "\n " + spread(left, right, max(0, m.width-2)) + "\n"
}

// renderSummaries shows one line per scanned root: how much it moved overall,
// how big it is now, and how long the scan took.
func (m *Model) renderSummaries() string {
	var b strings.Builder
	for _, res := range m.results {
		var change string
		if res.Baseline {
			change = subtleTxt.Render("     baseline")
		} else {
			change = deltaStyle(res.TotalDelta()).Render(
				lipgloss.PlaceHorizontal(13, lipgloss.Right, humanize.SignedBytes(res.TotalDelta())))
		}

		line := fmt.Sprintf("%s  %s  %s",
			change,
			lipgloss.NewStyle().Foreground(colHeadFG).Render(
				pad(prettyPath(res.Root), max(18, m.pathWidth()/2))),
			subtleTxt.Render(fmt.Sprintf("%s total · %s items · scanned in %s",
				humanize.Bytes(res.Current.TotalUsage),
				withThousands(res.Current.ItemCount),
				humanize.Duration(res.Current.Duration))),
		)
		if res.Current.Unreadable > 0 {
			line += warnTxt.Render(fmt.Sprintf("  · %d unreadable", res.Current.Unreadable))
		}
		b.WriteString(" " + summaryBox.Render(line) + "\n")
	}
	return b.String()
}

func (m *Model) renderColumnHeadings(pathWidth int) string {
	metric := "GROWTH"
	if m.view == viewLargest {
		metric = "SIZE HERE"
	}

	head := strings.Join([]string{
		pad("#", colRank),
		padLeft(metric, colDelta),
		pad("", colTag),
		pad("TREND", colTrend),
		padLeft("TOTAL", colSize),
		pad("PATH", pathWidth),
	}, strings.Repeat(" ", gutter/2))

	return " " + headerRow.Width(max(0, m.width-2)).Render(head) + "\n"
}

func (m *Model) renderRow(i, pathWidth int) string {
	row := m.rows[i]
	selected := i == m.cursor

	rank := subtleTxt.Render(pad(fmt.Sprintf("%d", i+1), colRank))

	delta := deltaStyle(row.Delta).Render(padLeft(deltaText(row, m.view), colDelta))

	tag := pad("", colTag)
	switch {
	case m.deleted[row.Path]:
		tag = shrankTxt.Render(pad("del", colTag))
	case row.Kind == model.Added:
		tag = warnTxt.Render(pad("new", colTag))
	case row.Kind == model.Removed:
		tag = subtleTxt.Render(pad("gone", colTag))
	}

	trend := subtleTxt.Render(pad(humanize.Sparkline(row.History), colTrend))
	size := subtleTxt.Render(padLeft(humanize.Bytes(row.Usage), colSize))

	pathText := humanize.Truncate(prettyPath(row.Path), pathWidth)
	pathStyle := lipgloss.NewStyle().Foreground(colHeadFG)
	if m.deleted[row.Path] {
		pathStyle = subtleTxt.Strikethrough(true)
	}
	path := pathStyle.Render(pad(pathText, pathWidth))

	sep := strings.Repeat(" ", gutter/2)
	line := strings.Join([]string{rank, delta, tag, trend, size, path}, sep)

	if selected {
		return " " + selectedRow.Width(max(0, m.width-2)).Render(line) + "\n"
	}
	return " " + line + "\n"
}

// deltaText picks the number that matches the active view: growth views show
// the change, the size view shows the bytes held directly.
func deltaText(row model.GrowthRow, view viewMode) string {
	if view == viewLargest {
		return humanize.Bytes(row.Delta)
	}
	return humanize.SignedBytes(row.Delta)
}

func (m *Model) renderFooter() string {
	if m.status != "" {
		return "\n " + accentTxt.Render(m.status) + "\n" + m.renderHelp()
	}
	return "\n" + m.renderHelp()
}

func (m *Model) renderHelp() string {
	position := ""
	if len(m.rows) > 0 {
		position = fmt.Sprintf("%d/%d · ", m.cursor+1, len(m.rows))
	}
	return " " + helpBar.Render(position+accentTxt.Render(m.view.label())+
		subtleTxt.Render(" · ↑↓ move · tab view · d delete · o open · r rescan · q quit")) + "\n"
}

// visibleRows is how many table rows fit below the fixed chrome.
func (m *Model) visibleRows() int {
	if m.height == 0 {
		return 20
	}
	return max(1, m.height-chromeLines-len(m.results))
}

func (m *Model) pathWidth() int {
	fixed := colRank + colDelta + colTag + colTrend + colSize + 5*(gutter/2) + 2
	if m.width == 0 {
		return 48
	}
	return max(16, m.width-fixed)
}

// prettyPath shortens the home directory to ~, which buys back width on the
// column that needs it most.
func prettyPath(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if p == home {
		return "~"
	}
	if strings.HasPrefix(p, home+"/") {
		return "~" + p[len(home):]
	}
	return p
}

func withThousands(n int64) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var parts []string
	for len(s) > 3 {
		parts = append([]string{s[len(s)-3:]}, parts...)
		s = s[:len(s)-3]
	}
	return strings.Join(append([]string{s}, parts...), ",")
}

func pad(s string, w int) string {
	return lipgloss.PlaceHorizontal(w, lipgloss.Left, humanize.Truncate(s, w))
}

func padLeft(s string, w int) string {
	return lipgloss.PlaceHorizontal(w, lipgloss.Right, humanize.Truncate(s, w))
}

func spread(left, right string, width int) string {
	gap := width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}
