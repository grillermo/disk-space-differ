package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/grillermo/disk-space-differ/internal/report"
)

func typeText(m *Model, s string) {
	for _, r := range s {
		if r == ' ' {
			press(m, tea.KeySpace)
			continue
		}
		m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
}

func rowPaths(m *Model) []string {
	var out []string
	for _, row := range m.rows {
		out = append(out, row.Path)
	}
	return out
}

func filteredTable(t *testing.T) *Model {
	t.Helper()
	m := testModel(t, []*report.Result{sampleResult()})
	m.view = viewAllChanges
	m.rebuildRows()
	if len(m.rows) < 3 {
		t.Fatalf("sample has %d rows, want grower, newthing and shrinker", len(m.rows))
	}
	return m
}

// Terms are ANDed and case-insensitive, and each may land anywhere in the path.
func TestSlashNarrowsTheTableToRowsMatchingEveryTerm(t *testing.T) {
	m := filteredTable(t)

	typeText(m, "/GROW")
	if got := rowPaths(m); len(got) != 1 || got[0] != "/tmp/sandbox/grower" {
		t.Fatalf("/GROW left %v, want only grower", got)
	}

	press(m, tea.KeyCtrlU)
	typeText(m, "sandbox ing")
	if got := rowPaths(m); len(got) != 1 || got[0] != "/tmp/sandbox/newthing" {
		t.Errorf("'sandbox ing' left %v, want only newthing", got)
	}
	t.Log("\n" + m.View())
}

// Letters that are commands in the list — q among them — are text in the query.
func TestKeysTypedIntoTheFilterAreNotCommands(t *testing.T) {
	m := filteredTable(t)

	typeText(m, "/")
	for _, r := range "qdrs" {
		if cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}); cmd != nil {
			t.Fatalf("typing %q into the filter ran a command", r)
		}
	}
	if string(m.filter.query) != "qdrs" {
		t.Errorf("query = %q, want qdrs", string(m.filter.query))
	}
	if m.state != stateTable {
		t.Errorf("state = %v, want the table still", m.state)
	}
}

// Quitting and losing the query to the same keystroke is never what was meant,
// so esc takes the filter away first and only then quits.
func TestEscClearsTheFilterBeforeItQuits(t *testing.T) {
	m := filteredTable(t)
	all := len(m.rows)

	typeText(m, "/grow")
	press(m, tea.KeyTab) // back to the list, filter kept
	if m.filter.editing || len(m.rows) != 1 {
		t.Fatalf("tab should keep the filter and return keys to the list")
	}
	if cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEsc}); cmd != nil {
		t.Fatal("esc quit with a filter on screen")
	}
	if len(m.rows) != all || m.filter.shown() {
		t.Errorf("esc left %d rows and query %q, want all %d and none",
			len(m.rows), string(m.filter.query), all)
	}
	if cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEsc}); cmd == nil {
		t.Error("esc with no filter should quit")
	}
}

// Ctrl-w eats the word before the cursor and the spaces before it, as on a shell
// line, and the list widens back out to match.
func TestCtrlWDeletesTheLastWord(t *testing.T) {
	m := filteredTable(t)
	typeText(m, "/sandbox grow")
	press(m, tea.KeyCtrlW)
	if string(m.filter.query) != "sandbox " {
		t.Errorf("query = %q, want %q", string(m.filter.query), "sandbox ")
	}
	if len(m.rows) < 3 {
		t.Errorf("rows = %v, want every sandbox row again", rowPaths(m))
	}
}

// Stepping into a folder replaces the list the query was typed against.
func TestSteppingIntoAFolderDropsTheFilter(t *testing.T) {
	m := testModel(t, []*report.Result{layeredResult()})
	typeText(m, "/grow")
	press(m, tea.KeyTab)
	press(m, tea.KeyRight)

	if m.filter.shown() {
		t.Errorf("filter %q survived →", string(m.filter.query))
	}
	if got := rowPaths(m); len(got) == 0 || got[0] != "/tmp/sandbox/grower/deep" {
		t.Errorf("→ showed %v, want what grower holds", got)
	}
}

// The filter only hides rows: the folder's total and every share bar are still
// worked out from everything it holds.
func TestFilteringInspectKeepsTheWholeFoldersTotal(t *testing.T) {
	m := inspecting(t)
	total := m.inspectUsage()

	typeText(m, "/steady")
	if len(m.entries) != 1 || !strings.HasSuffix(m.entries[0].Path, "/steady") {
		t.Fatalf("entries = %v, want only steady", m.entries)
	}
	if got := m.inspectUsage(); got != total {
		t.Errorf("total under a filter = %d, want the folder's %d", got, total)
	}

	press(m, tea.KeyEnter)
	if got := m.inspectPath(); got != "/tmp/sandbox/steady" {
		t.Errorf("enter opened %s, want the one row the filter left", got)
	}
	if m.filter.shown() {
		t.Error("the filter survived opening a folder")
	}
}

// Opening inspect drops the table's filter, so leaving it must find the row it
// came from in the unfiltered table rather than trusting the old cursor.
func TestLeavingInspectLandsOnTheRowItWasOpenedFromAfterFiltering(t *testing.T) {
	m := testModel(t, []*report.Result{layeredResult()})
	m.view = viewLargest // steady never changed, so only this view ranks it
	m.rebuildRows()
	if got := rowPaths(m); len(got) < 2 || got[0] == "/tmp/sandbox/steady" {
		t.Fatalf("rows = %v, want steady below the first row", got)
	}

	typeText(m, "/steady")
	press(m, tea.KeyEnter)
	if m.state != stateInspect {
		t.Fatal("enter did not inspect the row the filter left")
	}

	press(m, tea.KeyEsc)
	if m.state != stateTable {
		t.Fatalf("esc left state %v, want the table", m.state)
	}
	if got := m.rows[m.table.cursor].Path; got != "/tmp/sandbox/steady" {
		t.Errorf("came back onto %s, want steady", got)
	}
}
