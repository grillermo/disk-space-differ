// Package tui renders the interactive growth report.
package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/grillermo/disk-space-differ/internal/config"
	"github.com/grillermo/disk-space-differ/internal/model"
	"github.com/grillermo/disk-space-differ/internal/report"
	"github.com/grillermo/disk-space-differ/internal/scan"
	"github.com/grillermo/disk-space-differ/internal/store"
)

type state int

const (
	stateScanning state = iota
	stateTable
	stateConfirmDelete
	stateError
)

type viewMode int

const (
	viewGrowth viewMode = iota
	viewAllChanges
	viewLargest
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
	scanDoneMsg struct{ results []*report.Result }
	scanErrMsg  struct{ err error }
	deletedMsg  struct {
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

	cursor int
	offset int
	width  int
	height int

	scanningRoot int
	progress     scan.Progress

	deleteTarget model.GrowthRow
	status       string
	err          error

	// deleted tracks paths removed during this session so the table can mark
	// them without forcing an immediate rescan.
	deleted map[string]bool
}

// New builds a model that will scan the configured roots on start.
func New(cfg config.Config, st *store.Store) *Model {
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
	}
}

// SetProgram wires the program handle used to push scan progress from the
// background scan goroutine.
func (m *Model) SetProgram(p *tea.Program) { m.prog = p }

// Init starts the spinner and the first scan.
func (m *Model) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, m.scanCmd())
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

	case scanDoneMsg:
		m.results = msg.results
		m.state = stateTable
		m.cursor, m.offset = 0, 0
		m.rebuildRows()
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
	if m.state == stateConfirmDelete {
		return m.handleConfirmKey(msg)
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
		m.cursor, m.offset = 0, 0
	case "end", "G":
		m.cursor = len(m.rows) - 1
		m.clampScroll()
	case "tab":
		m.view = (m.view + 1) % 3
		m.cursor, m.offset = 0, 0
		m.rebuildRows()
	case "r":
		m.state = stateScanning
		m.progress = scan.Progress{}
		return tea.Batch(m.spinner.Tick, m.scanCmd())
	case "o":
		return m.openCmd()
	case "d", "delete":
		m.beginDelete()
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

// beginDelete opens the confirmation screen, refusing targets that would be
// catastrophic to remove.
func (m *Model) beginDelete() {
	row, ok := m.selectedRow()
	if !ok {
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
	if len(m.rows) == 0 {
		return
	}
	m.cursor = clampInt(m.cursor+delta, 0, len(m.rows)-1)
	m.clampScroll()
}

func (m *Model) clampScroll() {
	visible := m.visibleRows()
	if visible <= 0 {
		return
	}
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+visible {
		m.offset = m.cursor - visible + 1
	}
	maxOffset := max(0, len(m.rows)-visible)
	m.offset = clampInt(m.offset, 0, maxOffset)
}

func (m *Model) selectedRow() (model.GrowthRow, bool) {
	if m.cursor < 0 || m.cursor >= len(m.rows) {
		return model.GrowthRow{}, false
	}
	return m.rows[m.cursor], true
}

// rebuildRows merges every root's rows into a single ranking for the current view.
func (m *Model) rebuildRows() {
	var all []model.GrowthRow
	for _, res := range m.results {
		switch m.view {
		case viewLargest:
			all = append(all, largestRows(res)...)
		case viewAllChanges:
			all = append(all, res.Changes(m.cfg.Top*len(m.results)+m.cfg.Top)...)
		default:
			all = append(all, res.Growth(m.cfg.Top*len(m.results)+m.cfg.Top)...)
		}
	}

	if m.view == viewLargest {
		sort.Slice(all, func(i, j int) bool { return all[i].Delta > all[j].Delta })
	} else {
		sort.Slice(all, func(i, j int) bool {
			a, b := absInt64(all[i].Delta), absInt64(all[j].Delta)
			if a != b {
				return a > b
			}
			return all[i].Path < all[j].Path
		})
	}

	if len(all) > m.cfg.Top {
		all = all[:m.cfg.Top]
	}
	m.rows = all
	m.cursor = clampInt(m.cursor, 0, max(0, len(m.rows)-1))
	m.clampScroll()
}

// largestRows ranks by bytes held directly, so a big directory does not drag
// its whole chain of ancestors into the list alongside it.
func largestRows(res *report.Result) []model.GrowthRow {
	rows := make([]model.GrowthRow, 0, len(res.Current.Dirs))
	for _, d := range res.Current.Dirs {
		rows = append(rows, model.GrowthRow{Path: d.Path, Delta: d.SelfUsage, Usage: d.Usage})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Delta > rows[j].Delta })
	if len(rows) > 200 {
		rows = rows[:200]
	}
	return rows
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
