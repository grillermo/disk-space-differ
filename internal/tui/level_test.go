package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/grillermo/disk-space-differ/internal/model"
	"github.com/grillermo/disk-space-differ/internal/report"
)

// layeredResult is a tree deep enough to walk up through several levels, with
// one branch that grew and one that has not moved in months.
//
//	/tmp/sandbox                      level 4
//	  /tmp/sandbox/grower             level 3
//	    /tmp/sandbox/grower/deep      level 2
//	      .../deep/nested             level 1  · grew
//	  /tmp/sandbox/steady             level 2
//	    /tmp/sandbox/steady/leaf      level 1  · unchanged
func layeredResult() *report.Result {
	now := time.Now()
	dirs := []model.DirStat{
		{Path: "/tmp/sandbox", Usage: 62914560, SelfUsage: 1024},
		{Path: "/tmp/sandbox/grower", Usage: 35651584, SelfUsage: 0},
		{Path: "/tmp/sandbox/grower/deep", Usage: 35651584, SelfUsage: 0},
		{Path: "/tmp/sandbox/grower/deep/nested", Usage: 35651584, SelfUsage: 35651584},
		{Path: "/tmp/sandbox/steady", Usage: 27261952, SelfUsage: 0},
		{Path: "/tmp/sandbox/steady/leaf", Usage: 27261952, SelfUsage: 27261952},
	}
	prev := make([]model.DirStat, len(dirs))
	copy(prev, dirs)

	return &report.Result{
		Root: "/tmp/sandbox",
		Current: &model.Snapshot{
			Root: "/tmp/sandbox", StartedAt: now, TotalUsage: 62914560, Dirs: dirs,
		},
		Previous: &model.Snapshot{
			Root: "/tmp/sandbox", StartedAt: now.Add(-72 * time.Hour),
			TotalUsage: 31457280, Dirs: prev,
		},
		Rows: []model.GrowthRow{
			{
				Path: "/tmp/sandbox/grower/deep/nested", Kind: model.Changed,
				Delta: 31457280, SubtreeDelta: 31457280, Usage: 35651584, PrevUsage: 4194304,
			},
		},
	}
}

func press(m *Model, key tea.KeyType) {
	m.handleKey(tea.KeyMsg{Type: key})
}

func TestLeftArrowClimbsToTheParentLevel(t *testing.T) {
	m := testModel(t, []*report.Result{layeredResult()})

	if m.level != 1 {
		t.Fatalf("level starts at %d, want the leaf folders", m.level)
	}
	if m.rows[0].Path != "/tmp/sandbox/grower/deep/nested" {
		t.Fatalf("level 1 top row = %s, want the leaf that grew", m.rows[0].Path)
	}

	press(m, tea.KeyLeft)
	if m.level != 2 {
		t.Fatalf("level = %d after ←, want 2", m.level)
	}
	if got := m.rows[0].Path; got != "/tmp/sandbox/grower/deep" {
		t.Errorf("level 2 top row = %s, want the folder holding the leaf", got)
	}

	press(m, tea.KeyLeft)
	if got := m.rows[0].Path; got != "/tmp/sandbox/grower" {
		t.Errorf("level 3 top row = %s, want a folder one higher again", got)
	}

	press(m, tea.KeyRight)
	if got := m.rows[0].Path; got != "/tmp/sandbox/grower/deep" {
		t.Errorf("→ should come back down; top row = %s", got)
	}
}

// The growth charged to a folder must not change as you climb, only which
// folder carries it.
func TestClimbingLevelsPreservesTheDelta(t *testing.T) {
	m := testModel(t, []*report.Result{layeredResult()})

	for lvl := 1; lvl <= m.maxLevel; lvl++ {
		m.setLevel(lvl)
		var total int64
		for _, row := range m.rows {
			total += row.Delta
		}
		if total != 31457280 {
			t.Errorf("level %d rows total %d, want the unchanged 31457280", lvl, total)
		}
	}
}

func TestLevelStopsAtBothEndsOfTheTree(t *testing.T) {
	m := testModel(t, []*report.Result{layeredResult()})

	press(m, tea.KeyRight)
	if m.level != 1 {
		t.Errorf("→ at the leaves moved to level %d, want to stay at 1", m.level)
	}

	for range m.maxLevel + 5 {
		press(m, tea.KeyLeft)
	}
	if m.level != m.maxLevel {
		t.Errorf("← past the root reached level %d, want to stop at %d", m.level, m.maxLevel)
	}
}

// The report ranks by change, not by size. A folder holding a lot of space that
// has not moved must not take a slot from a smaller folder that did.
func TestUnchangedFoldersNeverRankAtAnyLevel(t *testing.T) {
	m := testModel(t, []*report.Result{layeredResult()})

	for _, view := range []viewMode{viewGrowth, viewAllChanges} {
		m.view = view
		for lvl := 1; lvl <= m.maxLevel; lvl++ {
			m.setLevel(lvl)
			m.rebuildRows()
			for _, row := range m.rows {
				if strings.HasPrefix(row.Path, "/tmp/sandbox/steady") {
					t.Errorf("%s view, level %d: unchanged %s ranked with delta %d",
						view.label(), lvl, row.Path, row.Delta)
				}
			}
		}
	}
}

// The size view is the one place ranking by bytes is wanted, and it must follow
// the level along with everything else.
func TestLargestViewFollowsTheLevel(t *testing.T) {
	m := testModel(t, []*report.Result{layeredResult()})
	m.view = viewLargest
	m.setLevel(2)
	m.rebuildRows()

	if got := m.rows[0].Path; got != "/tmp/sandbox/grower/deep" {
		t.Errorf("largest at level 2 = %s, want the biggest level 2 folder", got)
	}
	if !strings.Contains(m.View(), "level 2/4") {
		t.Error("the table should say which level it is showing")
	}
}
