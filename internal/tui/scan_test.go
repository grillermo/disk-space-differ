package tui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/grillermo/disk-space-differ/internal/model"
	"github.com/grillermo/disk-space-differ/internal/report"
)

// growerResult is what a targeted scan of one folder from layeredResult comes
// back with: its own root, its own snapshots, and a change the wider scan never
// saw.
func growerResult() *report.Result {
	now := time.Now()
	dirs := []model.DirStat{
		{Path: "/tmp/sandbox/grower", Usage: 41943040, SelfUsage: 0},
		{Path: "/tmp/sandbox/grower/fresh", Usage: 41943040, SelfUsage: 41943040},
	}
	return &report.Result{
		Root: "/tmp/sandbox/grower",
		Current: &model.Snapshot{
			Root: "/tmp/sandbox/grower", StartedAt: now, TotalUsage: 41943040, Dirs: dirs,
		},
		Previous: &model.Snapshot{
			Root: "/tmp/sandbox/grower", StartedAt: now.Add(-time.Hour),
			TotalUsage: 35651584, Dirs: dirs,
		},
		Rows: []model.GrowthRow{
			{
				Path: "/tmp/sandbox/grower/fresh", Kind: model.Changed,
				Delta: 6291456, SubtreeDelta: 6291456, Usage: 41943040, PrevUsage: 35651584,
			},
		},
	}
}

// Scanning one folder is only worth doing if what comes back is what you then
// look at, so the table narrows into it the way → would.
func TestScanningAFolderNarrowsTheTableIntoIt(t *testing.T) {
	m := testModel(t, []*report.Result{layeredResult()})

	m.Update(subtreeDoneMsg{result: growerResult()})

	if m.state != stateTable {
		t.Fatalf("state %v after a targeted scan, want the table", m.state)
	}
	if got := m.focusPath(); got != "/tmp/sandbox/grower" {
		t.Errorf("narrowed to %q, want the folder that was scanned", got)
	}
	if got := m.rows[0].Path; got != "/tmp/sandbox/grower/fresh" {
		t.Errorf("top row = %s, want what the fresh scan found", got)
	}
}

// The scan of a folder replaces what its root's scan said about that folder, but
// only inside it: backing out has to show the roots that were configured, each
// once.
func TestBackingOutOfAScannedFolderReturnsToTheRoots(t *testing.T) {
	m := testModel(t, []*report.Result{layeredResult()})
	m.Update(subtreeDoneMsg{result: growerResult()})

	press(m, tea.KeyLeft)

	if len(m.focus) != 0 {
		t.Fatalf("← left the ranking narrowed to %v, want every root", m.focus)
	}
	seen := map[string]int{}
	for _, row := range m.rows {
		seen[row.Path]++
	}
	for path, n := range seen {
		if n > 1 {
			t.Errorf("%s ranked %d times; the scanned folder is being counted twice", path, n)
		}
	}
	if got := m.rows[0].Path; got != "/tmp/sandbox/grower" {
		t.Errorf("top row = %s, want what the configured root holds", got)
	}
}

// A second targeted scan of the same folder is a newer answer to the same
// question, not a second folder to rank alongside the first.
func TestRescanningAFolderReplacesItsEarlierScan(t *testing.T) {
	m := testModel(t, []*report.Result{layeredResult()})

	m.Update(subtreeDoneMsg{result: growerResult()})
	m.Update(subtreeDoneMsg{result: growerResult()})

	if len(m.results) != 2 {
		t.Fatalf("%d results held, want the root and the one folder", len(m.results))
	}
}

// The own-files row stands for the bytes of the folder being broken down, so
// scanning it would rescan the scope rather than anything inside it.
func TestTheOwnFilesRowCannotBeScanned(t *testing.T) {
	m := testModel(t, []*report.Result{ownFilesResult()})
	for i, row := range m.rows {
		if row.Path == "/tmp/sandbox" {
			m.table.cursor = i
		}
	}

	if cmd := m.scanSelected(); cmd != nil {
		t.Error("the own-files row should not start a scan of the folder it describes")
	}
	if m.state != stateTable {
		t.Errorf("state %v, want to stay on the table", m.state)
	}
}

// A deletion changes the tree under the folder that held it, so the numbers on
// screen are stale the moment it succeeds: the containing folder is measured
// again without being asked for.
func TestDeletingAFolderRescansTheOneThatHeldIt(t *testing.T) {
	m := testModel(t, []*report.Result{layeredResult()})

	cmd := m.handleDeleted(deletedMsg{path: "/tmp/sandbox/grower/deep/nested", freed: 35651584})

	if cmd == nil {
		t.Fatal("deleting a folder started no rescan of the folder that held it")
	}
	if m.subtree != "/tmp/sandbox/grower/deep" {
		t.Errorf("rescanning %q, want the folder the deleted one was in", m.subtree)
	}
	if m.state != stateScanning {
		t.Errorf("state %v after a delete, want the scanning screen", m.state)
	}
}

// The rescan is a consequence of the deletion, not a place the user asked to go,
// so it leaves the ranking narrowed where it already was.
func TestTheRescanAfterADeleteLeavesTheScopeAlone(t *testing.T) {
	m := testModel(t, []*report.Result{layeredResult()})
	m.handleDeleted(deletedMsg{path: "/tmp/sandbox/grower/fresh"})

	m.Update(subtreeDoneMsg{result: growerResult(), keepScope: true})

	if len(m.focus) != 0 {
		t.Errorf("the rescan narrowed the ranking to %v, want the scope untouched", m.focus)
	}
	if m.status != "deleted /tmp/sandbox/grower/fresh" {
		t.Errorf("status %q, want the deletion still reported", m.status)
	}
}
