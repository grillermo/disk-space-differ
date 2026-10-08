package tui

import (
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/grillermo/disk-space-differ/internal/level"
	"github.com/grillermo/disk-space-differ/internal/model"
)

// filter narrows whichever list is on screen to the rows whose name holds every
// term typed after `/`. It only hides rows: the ranking, the totals and the
// share bars are still computed from the whole list, so a filtered view never
// changes what a number means.
//
// It is temporary by design. Stepping into or out of a folder replaces the list
// it was typed against, so the filter goes with it.
type filter struct {
	editing bool   // keys go to the query rather than to the list
	query   []rune // matched case-insensitively against each row's name
	pos     int    // the query cursor
}

// shown reports whether the query line is on screen: while it is being typed,
// and afterwards for as long as it is hiding rows.
func (f *filter) shown() bool { return f.editing || len(f.query) > 0 }

func (f *filter) clear() { *f = filter{} }

// terms are ANDed and each may land anywhere in the name, so "cache node"
// narrows twice rather than searching for the literal phrase.
func (f *filter) terms() []string {
	return strings.Fields(strings.ToLower(string(f.query)))
}

func (f *filter) matches(name string, terms []string) bool {
	hay := strings.ToLower(name)
	for _, t := range terms {
		if !strings.Contains(hay, t) {
			return false
		}
	}
	return true
}

func (f *filter) insert(r rune) {
	f.query = append(f.query[:f.pos:f.pos], append([]rune{r}, f.query[f.pos:]...)...)
	f.pos++
}

func (f *filter) deleteBack() {
	if f.pos == 0 {
		return
	}
	f.query = append(f.query[:f.pos-1:f.pos-1], f.query[f.pos:]...)
	f.pos--
}

// deleteWord drops the run of spaces before the cursor and the word before
// that, the way ctrl-w does on a shell line.
func (f *filter) deleteWord() {
	i := f.pos
	for i > 0 && f.query[i-1] == ' ' {
		i--
	}
	for i > 0 && f.query[i-1] != ' ' {
		i--
	}
	f.query = append(f.query[:i:i], f.query[f.pos:]...)
	f.pos = i
}

// handleFilterKey edits the query while it has the keyboard, reporting whether
// the key was consumed. Up and down still move through the list, because left
// and right belong to the query here; enter acts on the selected row as it
// would without a filter.
func (m *Model) handleFilterKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	f := &m.filter
	switch msg.Type {
	case tea.KeyRunes:
		for _, r := range msg.Runes {
			f.insert(r)
		}
		m.queryChanged()
		return nil, true
	case tea.KeySpace:
		f.insert(' ')
		m.queryChanged()
		return nil, true
	}

	switch msg.String() {
	case "ctrl+c":
		return tea.Quit, true
	case "esc":
		f.clear()
		m.queryChanged()
	case "tab":
		f.editing = false
	case "left":
		f.pos = max(0, f.pos-1)
	case "right":
		f.pos = min(len(f.query), f.pos+1)
	case "ctrl+a", "home":
		f.pos = 0
	case "ctrl+e", "end":
		f.pos = len(f.query)
	case "backspace":
		f.deleteBack()
		m.queryChanged()
	case "ctrl+w":
		f.deleteWord()
		m.queryChanged()
	case "ctrl+u":
		f.query, f.pos = nil, 0
		m.queryChanged()
	case "enter":
		// The list keys take it from here, with the filter still applied.
		f.editing = false
		return nil, false
	case "up", "down", "pgup", "pgdown":
		return nil, false
	}
	return nil, true
}

// startFilter hands the keyboard to the query, picking up where an earlier
// query on this list left off.
func (m *Model) startFilter() {
	m.filter.editing = true
	m.filter.pos = len(m.filter.query)
}

// dropFilter forgets the query because the list it was typed against is gone.
func (m *Model) dropFilter() { m.filter.clear() }

// queryChanged re-applies the filter and starts the cursor over: after an edit
// the old position points at a row that may no longer be there.
func (m *Model) queryChanged() {
	if m.state == stateInspect {
		m.applyEntryFilter()
		m.browse.reset()
		return
	}
	m.applyRowFilter()
	m.table.reset()
}

// applyRowFilter derives the table's visible rows from the full ranking. With
// no terms it shares the ranking's backing array rather than copying it.
func (m *Model) applyRowFilter() {
	terms := m.filter.terms()
	if len(terms) == 0 {
		m.rows = m.ranked
	} else {
		m.rows = nil
		for _, row := range m.ranked {
			if m.filter.matches(m.rowName(row), terms) {
				m.rows = append(m.rows, row)
			}
		}
	}
	m.clampScroll()
}

func (m *Model) applyEntryFilter() {
	terms := m.filter.terms()
	if len(terms) == 0 {
		m.entries = m.contents
	} else {
		m.entries = nil
		for _, e := range m.contents {
			if m.filter.matches(m.entryLabel(e), terms) {
				m.entries = append(m.entries, e)
			}
		}
	}
	m.clampScroll()
}

// rowName is what a row is matched on: the path as displayed, or the label the
// own-files row is displayed under.
func (m *Model) rowName(row model.GrowthRow) string {
	if m.isFilesRow(row) {
		return "files here"
	}
	return prettyPath(row.Path)
}

func (m *Model) entryLabel(e level.Entry) string {
	if e.Files {
		return "files here"
	}
	return filepath.Base(e.Path)
}
