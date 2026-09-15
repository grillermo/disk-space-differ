// Package tui renders the interactive growth report.
package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/grillermo/disk-space-differ/internal/config"
	"github.com/grillermo/disk-space-differ/internal/diff"
	"github.com/grillermo/disk-space-differ/internal/level"
	"github.com/grillermo/disk-space-differ/internal/model"
	"github.com/grillermo/disk-space-differ/internal/report"
	"github.com/grillermo/disk-space-differ/internal/scan"
	"github.com/grillermo/disk-space-differ/internal/store"
)

// scroll is a cursor and the viewport offset that follows it through a list.
type scroll struct {
	cursor int
	offset int
}

func (s *scroll) reset() { s.cursor, s.offset = 0, 0 }

func (s *scroll) move(delta, n, visible int) {
	if n == 0 {
		return
	}
	s.cursor += delta
	s.clamp(n, visible)
}

func (s *scroll) clamp(n, visible int) {
	s.cursor = clampInt(s.cursor, 0, max(0, n-1))
	if visible <= 0 {
		return
	}
	if s.cursor < s.offset {
		s.offset = s.cursor
	}
	if s.cursor >= s.offset+visible {
		s.offset = s.cursor - visible + 1
	}
	s.offset = clampInt(s.offset, 0, max(0, n-visible))
}

type state int

const (
	stateScanning state = iota
	stateTable
	stateInspect
	stateConfirmDelete
	stateNeedScan
	stateError
)

// shortRoot is a root that -no-scan cannot report on yet, and how many of the
// two scans a comparison needs it has.
type shortRoot struct {
	root   string
	stored int
}

type viewMode int

const (
	viewGrowth viewMode = iota
	viewAllChanges
	viewLargest

	// viewCount is not a view; it is how many there are to cycle through.
	viewCount
)

func (v viewMode) label() string {
	switch v {
	case viewAllChanges:
		return "all changes"
	case viewLargest:
		return "largest"
	default:
		return "growth"
	}
}

type (
	progressMsg struct {
		rootIndex int
		progress  scan.Progress
	}
	scanDoneMsg    struct{ results []*report.Result }
	subtreeDoneMsg struct{ result *report.Result }
	scanErrMsg     struct{ err error }
	needScanMsg    struct{ roots []shortRoot }
	storedMsg      struct {
		results []*report.Result
		scans   int
		stored  int
	}
	deletedMsg struct {
		path  string
		freed int64
		err   error
	}
)

// Model is the Bubble Tea model for the report screen.
type Model struct {
	cfg   config.Config
	store *store.Store
	roots []string
	prog  *tea.Program

	state   state
	view    viewMode
	spinner spinner.Model

	results []*report.Result
	rows    []model.GrowthRow

	// focus is the trail of folders the ranking has been narrowed to, the last
	// one being on display. Empty means every scanned root at once.
	focus []string

	// inspect is the trail of directories opened in inspect mode, the last one
	// being on display; entries is what it holds. Empty outside inspect mode.
	inspect []string
	entries []level.Entry

	// The table and the inspect listing keep their own cursors, so that leaving
	// a directory puts you back on the row you opened it from.
	table  scroll
	browse scroll

	width  int
	height int

	scanningRoot int
	progress     scan.Progress

	// subtree is the folder a targeted scan (`s`) is running on, empty while the
	// configured roots are being scanned. The scanning screen names it, since it
	// is not one of m.roots.
	subtree string

	deleteTarget model.GrowthRow
	status       string
	err          error

	// deleted tracks paths removed during this session so the table can mark
	// them without forcing an immediate rescan.
	deleted map[string]bool

	// noScan opens on the stored snapshots instead of scanning. It only governs
	// the first load; `r` still rescans, because that is an explicit ask.
	noScan bool

	// scans is how many recorded scans the comparison spans, widened with `+`
	// and narrowed with `-`. stored is how far back the history actually goes,
	// so the window cannot be widened past the end of it.
	scans  int
	stored int

	// needScan lists the roots that -no-scan found too few stored scans for.
	needScan []shortRoot
}

// New builds a model that will scan the configured roots on start, or, with
// noScan, open on the snapshots already recorded for them. scans is the width of
// the comparison window in recorded scans; `+` and `-` change it later.
func New(cfg config.Config, st *store.Store, noScan bool, scans int) *Model {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = accentTxt

	return &Model{
		cfg:     cfg,
		store:   st,
		roots:   cfg.ResolvedRoots(),
		state:   stateScanning,
		view:    viewGrowth,
		spinner: sp,
		deleted: map[string]bool{},
		noScan:  noScan,
		scans:   max(report.MinScans, scans),
		stored:  report.MinScans,
	}
}

// SetProgram wires the program handle used to push scan progress from the
// background scan goroutine.
func (m *Model) SetProgram(p *tea.Program) { m.prog = p }

// Init starts the spinner and the first load.
func (m *Model) Init() tea.Cmd {
	if m.noScan {
		return m.storedCmd(m.scans)
	}
	return tea.Batch(m.spinner.Tick, m.scanCmd())
}

// storedCmd reports across the last `scans` recorded snapshots of every root. It
// touches nothing but the database, so the table is on screen immediately — which
// is what makes widening the window an interactive move rather than a rescan.
//
// A root with fewer than two recorded scans has nothing to compare, and this
// mode will not scan behind the user's back, so it asks instead.
func (m *Model) storedCmd(scans int) tea.Cmd {
	return func() tea.Msg {
		var short []shortRoot
		deepest := 0
		for _, root := range m.roots {
			n, err := report.StoredCount(m.store, root)
			if err != nil {
				return scanErrMsg{err}
			}
			deepest = max(deepest, n)
			if n < report.MinScans {
				short = append(short, shortRoot{root: root, stored: n})
			}
		}
		if len(short) > 0 {
			return needScanMsg{roots: short}
		}

		scans = clampInt(scans, report.MinScans, max(report.MinScans, deepest))
		var results []*report.Result
		for _, root := range m.roots {
			res, err := report.FromStore(m.store, root, scans)
			if err != nil {
				return scanErrMsg{err}
			}
			results = append(results, res)
		}
		return storedMsg{results: results, scans: scans, stored: deepest}
	}
}

// scanCmd scans every configured root in the background, streaming progress
// back into the program as it goes.
func (m *Model) scanCmd() tea.Cmd {
	return func() tea.Msg {
		var results []*report.Result
		for i, root := range m.roots {
			idx := i
			res, err := report.Run(context.Background(), m.store, m.cfg, root,
				func(p scan.Progress) {
					if m.prog != nil {
						m.prog.Send(progressMsg{rootIndex: idx, progress: p})
					}
				})
			if err != nil {
				return scanErrMsg{err}
			}
			results = append(results, res)
		}
		return scanDoneMsg{results}
	}
}

// Update handles messages.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.clampScroll()
		return m, nil

	case spinner.TickMsg:
		if m.state != stateScanning {
			return m, nil
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case progressMsg:
		m.scanningRoot = msg.rootIndex
		m.progress = msg.progress
		return m, nil

	case subtreeDoneMsg:
		m.subtree = ""
		m.state = stateTable
		// The fresh scan read a different tree from the one the inspect trail was
		// opened on, exactly as a full rescan does.
		m.inspect, m.entries = nil, nil
		m.adoptResult(msg.result)
		m.focusScanned(msg.result)
		return m, nil

	case scanDoneMsg:
		m.subtree = ""
		m.results = msg.results
		// A rescan replaces the tree the inspect trail was reading, so it starts
		// again from the fresh table rather than from stale contents.
		m.inspect, m.entries = nil, nil
		m.state = stateTable
		m.table.reset()
		m.browse.reset()
		m.rebuildRows()
		// A scan only ever compares against the run before it, so a window wider
		// than that is re-read from the store, the fresh scan now being its newest
		// end.
		if m.scans > report.MinScans {
			return m, m.storedCmd(m.scans)
		}
		return m, nil

	case storedMsg:
		// Widening the window leaves the newest scan where it is, so the table is
		// still describing the same tree: the cursor and any open inspect trail
		// stay put rather than being thrown away.
		if msg.scans < m.scans {
			m.status = fmt.Sprintf("only %d scans recorded", msg.stored)
		}
		m.scans, m.stored = msg.scans, msg.stored
		m.results = msg.results
		m.state = stateTable
		m.rebuildRows()
		return m, nil

	case needScanMsg:
		m.needScan = msg.roots
		m.state = stateNeedScan
		return m, nil

	case scanErrMsg:
		m.err = msg.err
		m.state = stateError
		return m, nil

	case deletedMsg:
		return m, m.handleDeleted(msg)

	case tea.KeyMsg:
		return m, m.handleKey(msg)
	}

	return m, nil
}

func (m *Model) handleDeleted(msg deletedMsg) tea.Cmd {
	m.state = stateTable
	if msg.err != nil {
		m.status = fmt.Sprintf("could not delete %s: %v", filepath.Base(msg.path), msg.err)
		return nil
	}
	m.deleted[msg.path] = true
	m.status = fmt.Sprintf("deleted %s", msg.path)
	return nil
}

func (m *Model) handleKey(msg tea.KeyMsg) tea.Cmd {
	// Inspect mode owns esc, which everywhere else quits the program.
	switch m.state {
	case stateConfirmDelete:
		return m.handleConfirmKey(msg)
	case stateInspect:
		return m.handleInspectKey(msg)
	case stateNeedScan:
		// Anything but "scan now" falls through to the global quit keys, so the
		// prompt is never a trap.
		if s := msg.String(); s == "s" || s == "S" || s == "enter" || s == "y" {
			m.needScan = nil
			m.state = stateScanning
			m.progress = scan.Progress{}
			return tea.Batch(m.spinner.Tick, m.scanCmd())
		}
	}

	switch msg.String() {
	case "q", "ctrl+c", "esc":
		return tea.Quit
	}

	if m.state != stateTable {
		return nil
	}

	m.status = ""
	switch msg.String() {
	case "up", "k":
		m.moveCursor(-1)
	case "down", "j":
		m.moveCursor(1)
	case "pgup", "ctrl+u":
		m.moveCursor(-m.visibleRows())
	case "pgdown", "ctrl+d":
		m.moveCursor(m.visibleRows())
	case "home", "g":
		m.table.reset()
	case "end", "G":
		m.moveCursor(len(m.rows))
	case "enter":
		m.beginInspect()
	case "tab":
		m.view = (m.view + 1) % viewCount
		m.table.reset()
		m.rebuildRows()
	case "right", "l":
		m.descendScope()
	case "left", "h":
		m.ascendScope()
	case "+", "=":
		// The window is widened optimistically; the load clamps it to what the
		// history actually holds and says so.
		m.scans++
		return m.storedCmd(m.scans)
	case "-", "_":
		if m.scans <= report.MinScans {
			m.status = "already comparing the last two scans"
			return nil
		}
		m.scans--
		return m.storedCmd(m.scans)
	case "r":
		m.state = stateScanning
		m.progress = scan.Progress{}
		return tea.Batch(m.spinner.Tick, m.scanCmd())
	case "s":
		return m.scanSelected()
	case "o":
		return m.openCmd()
	case "d", "delete":
		m.beginDelete()
	case " ":
		if row, ok := m.selectedRow(); ok {
			m.status = prettyPath(row.Path)
		}
	}
	return nil
}

func (m *Model) handleConfirmKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "y", "Y":
		target := m.deleteTarget
		return func() tea.Msg {
			err := os.RemoveAll(target.Path)
			return deletedMsg{path: target.Path, freed: target.Usage, err: err}
		}
	default:
		m.state = stateTable
		m.status = "delete cancelled"
		return nil
	}
}

func (m *Model) handleInspectKey(msg tea.KeyMsg) tea.Cmd {
	m.status = ""
	visible := m.visibleEntries()

	switch msg.String() {
	case "q", "ctrl+c":
		return tea.Quit
	case "esc":
		m.leaveInspect()
	case "up", "k":
		m.browse.move(-1, len(m.entries), visible)
	case "down", "j":
		m.browse.move(1, len(m.entries), visible)
	case "pgup", "ctrl+u":
		m.browse.move(-visible, len(m.entries), visible)
	case "pgdown", "ctrl+d":
		m.browse.move(visible, len(m.entries), visible)
	case "home", "g":
		m.browse.reset()
	case "end", "G":
		m.browse.move(len(m.entries), len(m.entries), visible)
	case "enter", "right", "l":
		m.descend()
	case "left", "h", "backspace":
		m.ascend()
	case "o":
		if entry, ok := m.selectedEntry(); ok {
			m.status = "opened " + prettyPath(entry.Path)
			return openInFileManager(entry.Path)
		}
	case " ":
		if entry, ok := m.selectedEntry(); ok {
			m.status = prettyPath(entry.Path)
		}
	}
	return nil
}

// scanSelected rescans just the highlighted folder and narrows the ranking into
// the result, which is the cheap way to find out whether something has moved
// down there: a folder is seconds where its whole root is minutes.
func (m *Model) scanSelected() tea.Cmd {
	row, ok := m.selectedRow()
	if !ok {
		return nil
	}
	// The own-files row names the folder being broken down, so scanning it would
	// rescan the scope rather than something inside it.
	if m.isFilesRow(row) {
		m.status = "that row is this folder's own files, not a folder"
		return nil
	}
	if m.deleted[row.Path] {
		m.status = "deleted this session"
		return nil
	}

	enclosing := ""
	if res, ok := m.resultFor(row.Path); ok {
		enclosing = res.Root
	}

	m.subtree = row.Path
	m.state = stateScanning
	m.progress = scan.Progress{}
	return tea.Batch(m.spinner.Tick, m.subtreeCmd(row.Path, enclosing))
}

// subtreeCmd scans one folder in the background, reporting it against whatever
// last recorded it — see report.RunSubtree.
func (m *Model) subtreeCmd(dir, enclosing string) tea.Cmd {
	return func() tea.Msg {
		res, err := report.RunSubtree(context.Background(), m.store, m.cfg, dir, enclosing,
			func(p scan.Progress) {
				if m.prog != nil {
					m.prog.Send(progressMsg{progress: p})
				}
			})
		if err != nil {
			return scanErrMsg{err}
		}
		return subtreeDoneMsg{result: res}
	}
}

// adoptResult adds a targeted scan's result to the ones on display, replacing an
// earlier scan of the same folder. resultFor prefers the deepest root, so from
// here on everything inside that folder is read from the fresh scan while the
// rest of the tree keeps being read from its root's.
func (m *Model) adoptResult(res *report.Result) {
	for i, existing := range m.results {
		if existing.Root == res.Root {
			m.results[i] = res
			return
		}
	}
	m.results = append(m.results, res)
}

// focusScanned narrows the table into the folder that was just scanned, so the
// fresh numbers are what is on screen. `←` backs out of it the same as any other
// step in, landing on the row it was scanned from.
func (m *Model) focusScanned(res *report.Result) {
	if m.focusPath() != res.Root {
		m.focus = append(m.focus, res.Root)
	}
	m.table.reset()
	m.rebuildRows()
	if res.Baseline {
		m.status = "nothing recorded to compare " + prettyPath(res.Root) + " against yet"
	} else {
		m.status = "scanned " + prettyPath(res.Root)
	}
}

// beginInspect opens the selected row as a directory, listing what it holds
// rather than what changed in it.
func (m *Model) beginInspect() {
	row, ok := m.selectedRow()
	if !ok {
		return
	}
	if !m.enterDir(row.Path) {
		m.status = "nothing recorded inside " + prettyPath(row.Path)
		return
	}
	m.state = stateInspect
}

func (m *Model) leaveInspect() {
	m.state = stateTable
	m.inspect, m.entries = nil, nil
	m.status = ""
}

// descend opens the selected subdirectory. The file group stands for bytes, not
// for a directory, so there is nothing below it to open.
func (m *Model) descend() {
	entry, ok := m.selectedEntry()
	if !ok {
		return
	}
	if entry.Files {
		m.status = "that row is this folder's own files, not a folder"
		return
	}
	if !m.enterDir(entry.Path) {
		m.status = "nothing recorded inside " + prettyPath(entry.Path)
	}
}

// ascend goes back the way we came in, landing on the row that was opened.
// Leaving the directory inspect mode started at leaves inspect mode, since there
// is nothing further back to return to.
func (m *Model) ascend() {
	if len(m.inspect) <= 1 {
		m.leaveInspect()
		return
	}

	came := m.inspect[len(m.inspect)-1]
	m.inspect = m.inspect[:len(m.inspect)-1]
	entries, ok := m.contentsOf(m.inspectPath())
	if !ok {
		m.leaveInspect()
		return
	}

	m.entries = entries
	m.browse.reset()
	for i, e := range entries {
		if !e.Files && e.Path == came {
			m.browse.move(i, len(entries), m.visibleEntries())
			break
		}
	}
}

// enterDir pushes path onto the inspect trail, reporting whether it had anything
// recorded to show.
func (m *Model) enterDir(path string) bool {
	entries, ok := m.contentsOf(path)
	if !ok || len(entries) == 0 {
		return false
	}
	m.inspect = append(m.inspect, path)
	m.entries = entries
	m.browse.reset()
	return true
}

// inspectPath is the directory currently on display in inspect mode.
func (m *Model) inspectPath() string {
	if len(m.inspect) == 0 {
		return ""
	}
	return m.inspect[len(m.inspect)-1]
}

func (m *Model) selectedEntry() (level.Entry, bool) {
	if m.browse.cursor < 0 || m.browse.cursor >= len(m.entries) {
		return level.Entry{}, false
	}
	return m.entries[m.browse.cursor], true
}

// contentsOf reads a directory out of whichever scanned root holds it.
func (m *Model) contentsOf(path string) ([]level.Entry, bool) {
	res, ok := m.resultFor(path)
	if !ok {
		return nil, false
	}
	return res.Contents(path)
}

// resultFor finds the scan covering path, preferring the deepest root when one
// scanned root sits inside another.
func (m *Model) resultFor(path string) (*report.Result, bool) {
	var best *report.Result
	for _, res := range m.results {
		if !underRoot(path, res.Root) {
			continue
		}
		if best == nil || len(res.Root) > len(best.Root) {
			best = res
		}
	}
	return best, best != nil
}

func underRoot(path, root string) bool {
	return path == root || strings.HasPrefix(path, strings.TrimSuffix(root, "/")+"/")
}

// beginDelete opens the confirmation screen, refusing targets that would be
// catastrophic to remove.
func (m *Model) beginDelete() {
	row, ok := m.selectedRow()
	if !ok {
		return
	}
	// The own-files row names the folder it is a breakdown of, so deleting it
	// would take the whole folder rather than the bytes the row describes.
	if m.isFilesRow(row) {
		m.status = "that row is this folder's own files, not a folder"
		return
	}
	if m.deleted[row.Path] {
		m.status = "already deleted"
		return
	}
	if reason, unsafe := m.unsafeToDelete(row.Path); unsafe {
		m.status = reason
		return
	}
	m.deleteTarget = row
	m.state = stateConfirmDelete
}

// unsafeToDelete blocks removal of a scan root, a home directory, or a
// filesystem root. Those are never the folder someone means to reclaim, and
// deleting one would be unrecoverable.
func (m *Model) unsafeToDelete(path string) (string, bool) {
	clean := filepath.Clean(path)
	if clean == "/" || clean == "." {
		return "refusing to delete the filesystem root", true
	}
	if home, err := os.UserHomeDir(); err == nil && clean == filepath.Clean(home) {
		return "refusing to delete your home directory", true
	}
	for _, root := range m.roots {
		if clean == filepath.Clean(root) {
			return "refusing to delete a scan root", true
		}
	}
	if _, err := os.Stat(clean); err != nil {
		return "path no longer exists", true
	}
	return "", false
}

func (m *Model) openCmd() tea.Cmd {
	row, ok := m.selectedRow()
	if !ok {
		return nil
	}
	m.status = "opened " + row.Path
	return openInFileManager(row.Path)
}

func (m *Model) moveCursor(delta int) {
	m.table.move(delta, len(m.rows), m.visibleRows())
}

// clampScroll keeps both listings inside their contents after a resize.
func (m *Model) clampScroll() {
	m.table.clamp(len(m.rows), m.visibleRows())
	m.browse.clamp(len(m.entries), m.visibleEntries())
}

func (m *Model) selectedRow() (model.GrowthRow, bool) {
	if m.table.cursor < 0 || m.table.cursor >= len(m.rows) {
		return model.GrowthRow{}, false
	}
	return m.rows[m.table.cursor], true
}

// descendScope narrows the ranking to the selected folder, charging everything
// that changed anywhere inside it to the one child holding it. It is what `→`
// does, and what lets the ranking be walked as a tree without leaving it.
func (m *Model) descendScope() {
	row, ok := m.selectedRow()
	if !ok {
		return
	}
	if m.isFilesRow(row) {
		m.status = "that row is this folder's own files, not a folder"
		return
	}
	if !m.hasSubdirs(row.Path) {
		m.status = "nothing recorded inside " + prettyPath(row.Path)
		return
	}

	m.focus = append(m.focus, row.Path)
	m.table.reset()
	m.rebuildRows()
}

// ascendScope widens the ranking back out, landing on the row it was narrowed
// from so that stepping in and out again is a round trip.
func (m *Model) ascendScope() {
	if len(m.focus) == 0 {
		m.status = "already showing every scanned root"
		return
	}

	came := m.focus[len(m.focus)-1]
	m.focus = m.focus[:len(m.focus)-1]
	m.table.reset()
	m.rebuildRows()
	m.selectPath(came)
}

// focusPath is the folder the ranking is narrowed to, empty when it spans every
// scanned root.
func (m *Model) focusPath() string {
	if len(m.focus) == 0 {
		return ""
	}
	return m.focus[len(m.focus)-1]
}

// scopeDirs are the folders being broken down: the focused one, or every
// scanned root at once while the table has not been narrowed yet. Starting at
// the roots rather than at the leaves is what gives `→` something to walk down
// into.
//
// A root sitting inside another one is left out: its contents are already
// ranked under the wider root, and listing both would charge the same bytes
// twice. A targeted scan (`s`) adds exactly such a nested root.
func (m *Model) scopeDirs() []string {
	if dir := m.focusPath(); dir != "" {
		return []string{dir}
	}
	roots := make([]string, 0, len(m.results))
	for _, res := range m.results {
		if m.nestedRoot(res.Root) {
			continue
		}
		roots = append(roots, res.Root)
	}
	return roots
}

// nestedRoot reports whether another scanned root contains this one.
func (m *Model) nestedRoot(root string) bool {
	for _, other := range m.results {
		if other.Root != root && underRoot(root, other.Root) {
			return true
		}
	}
	return false
}

// isFilesRow reports whether row stands for the bytes of the folder being broken
// down rather than for something inside it. It names that folder, so there is
// nothing below it to open and nothing separate from it to delete.
func (m *Model) isFilesRow(row model.GrowthRow) bool {
	for _, dir := range m.scopeDirs() {
		if row.Path == dir {
			return true
		}
	}
	return false
}

// hasSubdirs reports whether path holds anything recorded to break it down
// into. A folder holding only its own files would narrow to a single row
// describing itself, which is not a drill-down.
func (m *Model) hasSubdirs(path string) bool {
	entries, ok := m.contentsOf(path)
	if !ok {
		return false
	}
	for _, e := range entries {
		if !e.Files {
			return true
		}
	}
	return false
}

// pruneFocus drops trail entries the current results do not record. A rescan or
// a wider window is a different pair of snapshots, and a folder missing from
// them cannot be broken down, so the ranking backs out to one that can.
func (m *Model) pruneFocus() {
	for len(m.focus) > 0 {
		if _, ok := m.contentsOf(m.focusPath()); ok {
			return
		}
		m.focus = m.focus[:len(m.focus)-1]
	}
}

// selectPath puts the cursor on path, leaving it where it is if the row is gone
// — which it is whenever the folder we came from no longer ranks in this view.
func (m *Model) selectPath(path string) {
	for i, row := range m.rows {
		if row.Path == path {
			m.table.move(i, len(m.rows), m.visibleRows())
			return
		}
	}
}

// rowsInside ranks what one folder holds: one row per child, carrying every
// change recorded anywhere beneath it, plus one row for the folder's own files.
func (m *Model) rowsInside(dir string) []model.GrowthRow {
	res, ok := m.resultFor(dir)
	if !ok {
		return nil
	}

	if m.view == viewLargest {
		rows, _ := res.SizesWithin(dir)
		return rows[:min(len(rows), m.cfg.Top)]
	}

	rows, _ := res.Within(dir)
	if m.view == viewAllChanges {
		return diff.TopChanges(rows, m.cfg.Top)
	}
	return diff.TopGrowth(rows, m.cfg.Top)
}

// rebuildRows ranks what the current scope holds — the focused folder, or every
// scanned root merged into one ranking. A row is always one folder inside the
// scope carrying everything that changed beneath it, so `→` and `←` walk the
// tree without the numbers ever changing meaning.
func (m *Model) rebuildRows() {
	m.pruneFocus()

	var all []model.GrowthRow
	for _, dir := range m.scopeDirs() {
		all = append(all, m.rowsInside(dir)...)
	}

	sort.Slice(all, func(i, j int) bool {
		a, b := absInt64(all[i].Delta), absInt64(all[j].Delta)
		if a != b {
			return a > b
		}
		return all[i].Path < all[j].Path
	})

	if len(all) > m.cfg.Top {
		all = all[:m.cfg.Top]
	}

	m.rows = all
	m.clampScroll()
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func absInt64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
