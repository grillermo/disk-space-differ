package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/grillermo/disk-space-differ/internal/model"
	"github.com/grillermo/disk-space-differ/internal/report"
)

// layeredResult is a tree deep enough to walk down through several folders, with
// one branch that grew and one that has not moved in months.
//
//	/tmp/sandbox
//	  /tmp/sandbox/grower
//	    /tmp/sandbox/grower/deep
//	      .../deep/nested        · grew
//	  /tmp/sandbox/steady
//	    /tmp/sandbox/steady/leaf · unchanged
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

// The table opens on what the scanned roots hold, and → steps into whichever of
// those folders is selected, one folder at a time down to where the bytes are.
func TestRightArrowShowsTheFoldersInsideTheSelectedOne(t *testing.T) {
	m := testModel(t, []*report.Result{layeredResult()})

	if got := m.rows[0].Path; got != "/tmp/sandbox/grower" {
		t.Fatalf("top row = %s, want the folder the root holds", got)
	}

	press(m, tea.KeyRight)
	if got := m.rows[0].Path; got != "/tmp/sandbox/grower/deep" {
		t.Errorf("→ into grower showed %s, want what grower holds", got)
	}

	press(m, tea.KeyRight)
	if got := m.rows[0].Path; got != "/tmp/sandbox/grower/deep/nested" {
		t.Errorf("→ into deep showed %s, want what deep holds", got)
	}

	press(m, tea.KeyLeft)
	if got := m.rows[0].Path; got != "/tmp/sandbox/grower/deep" {
		t.Errorf("← went to %s, want back to what grower holds", got)
	}
	if got := m.rows[m.table.cursor].Path; got != "/tmp/sandbox/grower/deep" {
		t.Errorf("came back onto %s, want the row that was stepped into", got)
	}
}

// The growth charged to a folder must not change as you walk down, only which
// folder inside it carries it.
func TestDrillingDownPreservesTheDelta(t *testing.T) {
	m := testModel(t, []*report.Result{layeredResult()})

	for depth := range 3 {
		var total int64
		for _, row := range m.rows {
			total += row.Delta
		}
		if total != 31457280 {
			t.Errorf("rows %d folders down total %d, want the unchanged 31457280", depth, total)
		}
		press(m, tea.KeyRight)
	}
}

// A folder holding nothing but its own files is the end of the walk, and says so
// rather than appearing to do nothing.
func TestSteppingIntoALeafFolderIsRefused(t *testing.T) {
	m := testModel(t, []*report.Result{layeredResult()})
	press(m, tea.KeyRight)
	press(m, tea.KeyRight)

	press(m, tea.KeyRight)
	if got := m.rows[0].Path; got != "/tmp/sandbox/grower/deep/nested" {
		t.Errorf("→ at the leaf moved to %s, want to stay put", got)
	}
	if m.status == "" {
		t.Error("→ at the leaf should say why nothing happened")
	}
}

func TestLeftArrowAtTheTopLevelStaysThere(t *testing.T) {
	m := testModel(t, []*report.Result{layeredResult()})

	press(m, tea.KeyLeft)
	if len(m.focus) != 0 {
		t.Errorf("← at the top narrowed to %v, want to stay across every root", m.focus)
	}
	if got := m.rows[0].Path; got != "/tmp/sandbox/grower" {
		t.Errorf("← at the top showed %s, want what the root holds", got)
	}
}

// The report ranks by change, not by size. A folder holding a lot of space that
// has not moved must not take a slot from a smaller folder that did.
func TestUnchangedFoldersNeverRankAtAnyDepth(t *testing.T) {
	for _, view := range []viewMode{viewGrowth, viewAllChanges} {
		m := testModel(t, []*report.Result{layeredResult()})
		m.view = view
		m.rebuildRows()

		for range 3 {
			for _, row := range m.rows {
				if strings.HasPrefix(row.Path, "/tmp/sandbox/steady") {
					t.Errorf("%s view: unchanged %s ranked with delta %d",
						view.label(), row.Path, row.Delta)
				}
			}
			press(m, tea.KeyRight)
		}
	}
}

// The size view is the one place ranking by bytes is wanted, and it must follow
// the walk along with everything else.
func TestLargestViewRanksWhatTheFolderHolds(t *testing.T) {
	m := testModel(t, []*report.Result{layeredResult()})
	m.view = viewLargest
	m.rebuildRows()

	if got := m.rows[0].Path; got != "/tmp/sandbox/grower" {
		t.Errorf("largest = %s, want the biggest folder the root holds", got)
	}
	// Unlike the growth views, this one lists the folder that has not moved.
	if got := m.rows[1].Path; got != "/tmp/sandbox/steady" {
		t.Errorf("second largest = %s, want the unchanged folder", got)
	}

	press(m, tea.KeyRight)
	if got := m.rows[0].Path; got != "/tmp/sandbox/grower/deep" {
		t.Errorf("largest inside grower = %s, want what grower holds", got)
	}
	if !strings.Contains(m.View(), "inside /tmp/sandbox/grower") {
		t.Error("the table should say which folder it has been narrowed to")
	}
}

// ownFilesResult is a root whose own files grew alongside a subfolder, so the
// breakdown of it has to account for both.
func ownFilesResult() *report.Result {
	res := layeredResult()
	res.Rows = append(res.Rows, model.GrowthRow{
		Path: "/tmp/sandbox", Kind: model.Changed,
		Delta: 5242880, SubtreeDelta: 5242880, Usage: 62914560, PrevUsage: 57671680,
	})
	return res
}

// The bytes a folder holds itself belong to no subfolder, so they get a row of
// their own: without it the breakdown would not add up to the folder.
func TestAFoldersOwnFilesGetTheirOwnRow(t *testing.T) {
	m := testModel(t, []*report.Result{ownFilesResult()})

	var own model.GrowthRow
	for _, row := range m.rows {
		if row.Path == "/tmp/sandbox" {
			own = row
		}
	}
	if own.Delta != 5242880 {
		t.Fatalf("the root's own files charged %d, want 5242880", own.Delta)
	}
	if !strings.Contains(m.View(), "files here") {
		t.Error("the own-files row should be labelled, not shown as the folder itself")
	}
}

// That row names the folder it is a breakdown of, so stepping into it or
// deleting it would act on the whole folder rather than on the bytes shown.
func TestTheOwnFilesRowCannotBeSteppedIntoOrDeleted(t *testing.T) {
	m := testModel(t, []*report.Result{ownFilesResult()})
	for i, row := range m.rows {
		if row.Path == "/tmp/sandbox" {
			m.table.cursor = i
		}
	}

	press(m, tea.KeyRight)
	if len(m.focus) != 0 {
		t.Errorf("→ on the own-files row narrowed to %v, want to stay put", m.focus)
	}

	m.beginDelete()
	if m.state == stateConfirmDelete {
		t.Error("the own-files row should not offer to delete the folder it describes")
	}
}
