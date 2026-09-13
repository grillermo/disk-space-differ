package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/grillermo/disk-space-differ/internal/humanize"
	"github.com/grillermo/disk-space-differ/internal/level"
	"github.com/grillermo/disk-space-differ/internal/model"
	"github.com/grillermo/disk-space-differ/internal/report"
)

// Fixed column widths. The path column absorbs whatever space is left, since
// it is the column that benefits most from extra room.
const (
	// Sized to the widest real value each column renders (e.g. "+1023 PiB",
	// "gone", "100%"), plus a one-character margin — not to a round number.
	colDelta = 10
	colTag   = 4
	colSize  = 9
	gutter   = 2

	// The inspect listing trades the rank and trend columns for a proportion
	// bar, which is what answers "which of these is the problem" at a glance.
	colBar   = 6
	colShare = 4

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
	case stateNeedScan:
		return m.viewNeedScan()
	case stateInspect:
		return m.viewInspect()
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

// viewNeedScan explains why -no-scan has nothing to show and offers the only
// two ways forward: scan now, or leave the database untouched and quit.
func (m *Model) viewNeedScan() string {
	lines := []string{
		warnTxt.Render("Nothing to compare yet"),
		"",
		subtleTxt.Render("Comparing needs two recorded scans: one to be the baseline,"),
		subtleTxt.Render("one to measure against it. -no-scan only reads what is stored."),
		"",
	}
	for _, r := range m.needScan {
		lines = append(lines, accentTxt.Render(prettyPath(r.root))+
			subtleTxt.Render(fmt.Sprintf("  %d of 2 scans recorded", r.stored)))
	}
	lines = append(lines,
		"",
		"  "+accentTxt.Render("s")+subtleTxt.Render(" scan now")+
			"    "+accentTxt.Render("q")+subtleTxt.Render(" quit"),
	)

	box := confirmBox.Render(strings.Join(lines, "\n"))
	if m.width > 0 && m.height > 0 {
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
	}
	return "\n" + box + "\n"
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
		end := min(m.table.offset+visible, len(m.rows))
		for i := m.table.offset; i < end; i++ {
			b.WriteString(m.renderRow(i, pathWidth))
		}
	}

	b.WriteString(m.renderFooter(m.renderHelp()))
	return b.String()
}

// viewInspect reads one directory as a tree instead of as a ranking: what it
// holds, how big each part is, and how much of the directory each part accounts
// for. It is the second deliberate exception to ranking by change rather than by
// size, and the only screen where the numbers overlap between rows and the rows
// inside them.
func (m *Model) viewInspect() string {
	var b strings.Builder

	b.WriteString(m.renderHeader())
	b.WriteString(m.renderInspectSummary())
	b.WriteString("\n")

	nameWidth := m.nameWidth()
	b.WriteString(m.renderInspectHeadings(nameWidth))

	if len(m.entries) == 0 {
		b.WriteString("\n  " + subtleTxt.Render("nothing recorded in this folder") + "\n")
	} else {
		total := m.inspectUsage()
		visible := m.visibleEntries()
		end := min(m.browse.offset+visible, len(m.entries))
		for i := m.browse.offset; i < end; i++ {
			b.WriteString(m.renderEntry(i, nameWidth, total))
		}
	}

	b.WriteString(m.renderFooter(m.renderInspectHelp()))
	return b.String()
}

// renderInspectSummary names the directory being read and totals what it holds.
func (m *Model) renderInspectSummary() string {
	usage, prev := m.inspectUsage(), m.inspectPrevUsage()

	var change string
	if m.inspectBaseline() {
		change = subtleTxt.Render("     baseline")
	} else {
		change = deltaStyle(usage - prev).Render(
			lipgloss.PlaceHorizontal(colDelta, lipgloss.Right, humanize.SignedBytes(usage-prev)))
	}

	line := fmt.Sprintf("%s  %s  %s",
		change,
		lipgloss.NewStyle().Foreground(colHeadFG).Render(
			pad(prettyPath(m.inspectPath()), max(18, m.nameWidth()))),
		subtleTxt.Render(fmt.Sprintf("%s here · %d items", humanize.Bytes(usage), len(m.entries))),
	)
	return " " + summaryBox.Render(line) + "\n"
}

func (m *Model) renderInspectHeadings(nameWidth int) string {
	head := strings.Join([]string{
		padLeft("SIZE", colSize),
		padLeft("CHANGE", colDelta),
		pad("", colTag),
		pad("SHARE", colBar),
		pad("", colShare),
		pad("HOLDS", nameWidth),
	}, strings.Repeat(" ", gutter/2))

	return " " + headerRow.Width(max(0, m.width-2)).Render(head) + "\n"
}

func (m *Model) renderEntry(i, nameWidth int, total int64) string {
	e := m.entries[i]

	size := subtleTxt.Render(padLeft(humanize.Bytes(e.Usage), colSize))

	delta := e.Usage - e.PrevUsage
	changeText := "—"
	if delta != 0 && !m.inspectBaseline() {
		changeText = humanize.SignedBytes(delta)
	}
	change := deltaStyle(delta).Render(padLeft(changeText, colDelta))

	tag := pad("", colTag)
	switch {
	case m.deleted[e.Path] && !e.Files:
		tag = shrankTxt.Render(pad("del", colTag))
	case e.Kind == model.Added:
		tag = warnTxt.Render(pad("new", colTag))
	case e.Kind == model.Removed:
		tag = subtleTxt.Render(pad("gone", colTag))
	}

	bar := subtleTxt.Render(shareBar(e.Usage, total, colBar))
	share := subtleTxt.Render(padLeft(sharePercent(e.Usage, total), colShare))

	nameStyle := lipgloss.NewStyle().Foreground(colHeadFG)
	if e.Files {
		nameStyle = subtleTxt
	} else if m.deleted[e.Path] {
		nameStyle = subtleTxt.Strikethrough(true)
	}
	name := nameStyle.Render(pad(m.entryName(e, nameWidth), nameWidth))

	sep := strings.Repeat(" ", gutter/2)
	line := strings.Join([]string{size, change, tag, bar, share, name}, sep)

	if i == m.browse.cursor {
		return " " + selectedRow.Width(max(0, m.width-2)).Render(line) + "\n"
	}
	return " " + line + "\n"
}

// entryName labels an entry. Directories show their own name; the file group
// says what it stands for, spelling out the folded-in small folders when there
// is room, because otherwise those bytes look unaccounted for.
func (m *Model) entryName(e level.Entry, width int) string {
	if !e.Files {
		return filepath.Base(e.Path) + "/"
	}
	name := "files here"
	note := fmt.Sprintf("  (and folders under %s)", humanize.Bytes(m.cfg.MinDirSize))
	if width >= len(name)+len(note) {
		return name + note
	}
	return name
}

// shareBar draws part as a proportion of total. Which child dominates a folder
// is the question inspect mode exists to answer, and a column of byte counts
// does not answer it at a glance.
func shareBar(part, total, width int64) string {
	if total <= 0 || part <= 0 {
		return strings.Repeat("░", int(width))
	}
	filled := (part*width + total/2) / total
	// Anything with bytes in it gets at least one block, so that a long tail of
	// small folders is still visibly there.
	filled = max(1, min(filled, width))
	return strings.Repeat("█", int(filled)) + strings.Repeat("░", int(width-filled))
}

func sharePercent(part, total int64) string {
	if total <= 0 {
		return "0%"
	}
	return fmt.Sprintf("%d%%", (part*100+total/2)/total)
}

func (m *Model) renderHeader() string {
	left := titleStyle.Render("disk-space-differ")

	var right string
	if len(m.results) > 0 && m.results[0].Previous != nil {
		res := m.results[0]
		// The window is named only once it is wider than the default, so the
		// common case stays uncluttered.
		span := ""
		if res.Scans > report.MinScans {
			span = fmt.Sprintf("last %d scans · ", res.Scans)
		}
		right = subtleTxt.Render(span + "compared with " + humanize.Since(res.Previous.StartedAt))
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
				lipgloss.PlaceHorizontal(colDelta, lipgloss.Right, humanize.SignedBytes(res.TotalDelta())))
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
		padLeft(metric, colDelta),
		pad("", colTag),
		padLeft("TOTAL", colSize),
		pad(m.pathHeading(), pathWidth),
	}, strings.Repeat(" ", gutter/2))

	return " " + headerRow.Width(max(0, m.width-2)).Render(head) + "\n"
}

// pathHeading spells out the granularity on display, because the same numbers
// mean something different one level up.
func (m *Model) pathHeading() string {
	// In the path view the rank column no longer counts down by size, so the
	// heading has to say what the order actually is.
	name := "PATH"
	if m.view == viewByPath {
		name = "PATH a→z"
	}
	if m.maxLevel <= 1 {
		return name
	}
	what := "leaf folders"
	if m.level > 1 {
		what = fmt.Sprintf("folders holding level %d folders", m.level-1)
	}
	return fmt.Sprintf("%s · level %d/%d · %s", name, m.level, m.maxLevel, what)
}

func (m *Model) renderRow(i, pathWidth int) string {
	row := m.rows[i]
	selected := i == m.table.cursor

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

	size := subtleTxt.Render(padLeft(humanize.Bytes(row.Usage), colSize))

	pathText := humanize.Truncate(prettyPath(row.Path), pathWidth)
	pathStyle := lipgloss.NewStyle().Foreground(colHeadFG)
	if m.deleted[row.Path] {
		pathStyle = subtleTxt.Strikethrough(true)
	}
	path := pathStyle.Render(pad(pathText, pathWidth))

	sep := strings.Repeat(" ", gutter/2)
	line := strings.Join([]string{delta, tag, size, path}, sep)

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

func (m *Model) renderFooter(help string) string {
	if m.status != "" {
		return "\n " + accentTxt.Render(m.status) + "\n" + help
	}
	return "\n" + help
}

func (m *Model) renderHelp() string {
	position := ""
	if len(m.rows) > 0 {
		position = fmt.Sprintf("%d/%d · ", m.table.cursor+1, len(m.rows))
	}
	return " " + helpBar.Render(position+accentTxt.Render(m.view.label())+
		subtleTxt.Render(fmt.Sprintf(" · level %d/%d · scans %d · ↑↓ move · ←→ level · +/- scans · enter inspect · tab view · space path · d delete · o open · r rescan · q quit",
			m.level, m.maxLevel, m.displayedScans()))) + "\n"
}

// displayedScans is how wide the window on screen actually is, which is the
// requested width only until it runs out of history to reach into.
func (m *Model) displayedScans() int {
	if len(m.results) == 0 {
		return m.scans
	}
	return m.results[0].Scans
}

func (m *Model) renderInspectHelp() string {
	position := ""
	if len(m.entries) > 0 {
		position = fmt.Sprintf("%d/%d · ", m.browse.cursor+1, len(m.entries))
	}
	return " " + helpBar.Render(position+accentTxt.Render("inspect")+
		subtleTxt.Render(" · ↑↓ move · enter open folder · ← back · esc leave · space path · o reveal · q quit")) + "\n"
}

// visibleRows is how many table rows fit below the fixed chrome.
func (m *Model) visibleRows() int {
	if m.height == 0 {
		return 20
	}
	return max(1, m.height-chromeLines-len(m.results))
}

// visibleEntries is how many inspect rows fit. The inspect screen carries a
// single summary line however many roots were scanned.
func (m *Model) visibleEntries() int {
	if m.height == 0 {
		return 20
	}
	return max(1, m.height-chromeLines)
}

func (m *Model) pathWidth() int {
	fixed := colDelta + colTag + colSize + 3*(gutter/2) + 2
	if m.width == 0 {
		return 48
	}
	return max(16, m.width-fixed)
}

func (m *Model) nameWidth() int {
	fixed := colSize + colDelta + colTag + colBar + colShare + 5*(gutter/2) + 2
	if m.width == 0 {
		return 48
	}
	return max(16, m.width-fixed)
}

// inspectUsage totals what the inspected directory holds. It is summed from the
// entries rather than read off the directory, so the bar and the total can never
// disagree with the rows above them.
func (m *Model) inspectUsage() int64 {
	var total int64
	for _, e := range m.entries {
		total += e.Usage
	}
	return total
}

func (m *Model) inspectPrevUsage() int64 {
	var total int64
	for _, e := range m.entries {
		total += e.PrevUsage
	}
	return total
}

// inspectBaseline reports whether the inspected directory has a previous scan to
// be compared against. On a first run every entry would otherwise read as having
// grown by its whole size.
func (m *Model) inspectBaseline() bool {
	res, ok := m.resultFor(m.inspectPath())
	return !ok || res.Baseline
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
