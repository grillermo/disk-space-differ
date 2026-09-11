package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/grillermo/disk-space-differ/internal/config"
	"github.com/grillermo/disk-space-differ/internal/model"
	"github.com/grillermo/disk-space-differ/internal/report"
)

func testModel(t *testing.T, results []*report.Result) *Model {
	t.Helper()
	cfg := config.Default()
	cfg.Top = 20
	m := New(cfg, nil, false, report.MinScans)
	m.roots = []string{"/tmp/sandbox"}
	m.width, m.height = 110, 26
	m.results = results
	m.state = stateTable
	m.rebuildRows()
	return m
}

func sampleResult() *report.Result {
	now := time.Now()
	return &report.Result{
		Root: "/tmp/sandbox",
		Current: &model.Snapshot{
			Root: "/tmp/sandbox", StartedAt: now, Duration: 127 * time.Millisecond,
			TotalUsage: 62914560, ItemCount: 32291,
			Dirs: []model.DirStat{
				{Path: "/tmp/sandbox", Usage: 62914560, SelfUsage: 1024},
				{Path: "/tmp/sandbox/grower", Usage: 35651584, SelfUsage: 35651584},
			},
		},
		Previous: &model.Snapshot{
			Root: "/tmp/sandbox", StartedAt: now.Add(-72 * time.Hour), TotalUsage: 36700160,
		},
		Rows: []model.GrowthRow{
			{
				Path: "/tmp/sandbox/grower/deep/nested", Kind: model.Changed,
				Delta: 31457280, SubtreeDelta: 31457280, Usage: 35651584, PrevUsage: 4194304,
				History: []int64{4194304, 4194304, 35651584},
			},
			{
				Path: "/tmp/sandbox/newthing", Kind: model.Added,
				Delta: 15728640, SubtreeDelta: 15728640, Usage: 15728640,
				History: []int64{0, 0, 15728640},
			},
			{
				Path: "/tmp/sandbox/shrinker", Kind: model.Removed,
				Delta: -20971520, Usage: 0, PrevUsage: 20971520,
				History: []int64{20971520, 20971520, 0},
			},
		},
	}
}

func TestTableViewRendersRowsAndTotals(t *testing.T) {
	m := testModel(t, []*report.Result{sampleResult()})
	out := m.View()
	t.Log("\n" + out)

	for _, want := range []string{
		"disk-space-differ",
		"+30.0 MiB", // the deep grower, charged exclusively
		"nested",    // its path
		"new",       // the added-subtree tag
		"+25.0 MiB", // root total: +30 +15 -20
		"growth",    // active view label
	} {
		if !strings.Contains(out, want) {
			t.Errorf("view is missing %q", want)
		}
	}
}

func TestGrowthViewHidesShrinkageButAllChangesShowsIt(t *testing.T) {
	m := testModel(t, []*report.Result{sampleResult()})

	if got := m.View(); strings.Contains(got, "-20.0 MiB") {
		t.Error("growth view should not list directories that shrank")
	}

	m.view = viewAllChanges
	m.rebuildRows()
	if got := m.View(); !strings.Contains(got, "-20.0 MiB") {
		t.Error("all-changes view should list directories that shrank")
	}
}

// The path view is an ordering, not a different ranking: it shows the rows the
// all-changes view would show, read top to bottom as the tree reads.
func TestByPathViewOrdersTheSameRowsAlphabetically(t *testing.T) {
	m := testModel(t, []*report.Result{sampleResult()})

	m.view = viewAllChanges
	m.rebuildRows()
	byChange := map[string]bool{}
	for _, row := range m.rows {
		byChange[row.Path] = true
	}

	m.view = viewByPath
	m.rebuildRows()

	if len(m.rows) != len(byChange) {
		t.Fatalf("path view has %d rows, want the %d the all-changes view has", len(m.rows), len(byChange))
	}
	for _, row := range m.rows {
		if !byChange[row.Path] {
			t.Errorf("%s appears in the path view but not in all changes", row.Path)
		}
	}
	for i := 1; i < len(m.rows); i++ {
		if m.rows[i-1].Path > m.rows[i].Path {
			t.Errorf("rows out of path order: %s before %s", m.rows[i-1].Path, m.rows[i].Path)
		}
	}
	if !strings.Contains(m.View(), "a→z") {
		t.Error("the table should say it is ordered by path")
	}
}

// tab must reach every view and come back round.
func TestTabCyclesThroughEveryViewAndWrapsAround(t *testing.T) {
	m := testModel(t, []*report.Result{sampleResult()})

	var seen []string
	for range int(viewCount) {
		press(m, tea.KeyTab)
		seen = append(seen, m.view.label())
	}

	want := []string{"all changes", "largest", "by path", "growth"}
	if strings.Join(seen, ",") != strings.Join(want, ",") {
		t.Errorf("tab cycled through %v, want %v", seen, want)
	}
}

func TestBaselineRunIsLabelled(t *testing.T) {
	res := sampleResult()
	res.Previous = nil
	res.Baseline = true

	out := testModel(t, []*report.Result{res}).View()
	if !strings.Contains(out, "first run") {
		t.Error("a baseline run should say so instead of showing a bogus comparison")
	}
}

func TestScanningViewShowsProgress(t *testing.T) {
	m := testModel(t, nil)
	m.state = stateScanning
	m.progress.ItemCount = 124331
	m.progress.TotalUsage = 44225085440

	out := m.View()
	t.Log("\n" + out)

	if !strings.Contains(out, "124,331") {
		t.Error("scanning view should show a thousands-separated item count")
	}
	if !strings.Contains(out, "41.2 GiB") {
		t.Error("scanning view should show bytes scanned so far")
	}
}

func TestConfirmViewNamesTargetAndSize(t *testing.T) {
	m := testModel(t, []*report.Result{sampleResult()})
	m.state = stateConfirmDelete
	m.deleteTarget = m.rows[0]

	out := m.View()
	t.Log("\n" + out)

	if !strings.Contains(out, "cannot be undone") {
		t.Error("delete confirmation must warn that it is irreversible")
	}
	if !strings.Contains(out, "nested") {
		t.Error("delete confirmation must name the target path")
	}
}

// Deleting a scan root, a home directory or / must be refused outright.
func TestUnsafeDeleteTargetsAreRefused(t *testing.T) {
	m := testModel(t, []*report.Result{sampleResult()})

	for _, path := range []string{"/", "/tmp/sandbox"} {
		if reason, unsafe := m.unsafeToDelete(path); !unsafe {
			t.Errorf("deleting %q should be refused, got reason %q", path, reason)
		}
	}
}

func TestNarrowTerminalStillRenders(t *testing.T) {
	m := testModel(t, []*report.Result{sampleResult()})
	m.width, m.height = 40, 12

	if out := m.View(); out == "" {
		t.Error("view collapsed to nothing on a narrow terminal")
	}
}
