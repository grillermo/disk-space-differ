package diff

import (
	"testing"

	"github.com/grillermo/disk-space-differ/internal/model"
)

// tree builds snapshot dirs from literal self sizes, deriving each directory's
// cumulative usage from its descendants the way a real scan would.
func tree(selfByPath map[string]int64) []model.DirStat {
	dirs := make([]model.DirStat, 0, len(selfByPath))
	for path, self := range selfByPath {
		var usage int64
		for other, otherSelf := range selfByPath {
			if other == path || isDescendant(other, path) {
				usage += otherSelf
			}
		}
		dirs = append(dirs, model.DirStat{Path: path, Usage: usage, SelfUsage: self})
	}
	return dirs
}

func isDescendant(child, parent string) bool {
	return len(child) > len(parent) && child[:len(parent)] == parent && child[len(parent)] == '/'
}

func rowFor(t *testing.T, rows []model.GrowthRow, path string) model.GrowthRow {
	t.Helper()
	for _, r := range rows {
		if r.Path == path {
			return r
		}
	}
	t.Fatalf("no row for %s; got %v", path, paths(rows))
	return model.GrowthRow{}
}

func paths(rows []model.GrowthRow) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Path
	}
	return out
}

// Growth in one deep directory must be charged to that directory alone, not
// repeated on every ancestor.
func TestGrowthIsNotDoubleCountedAcrossAncestors(t *testing.T) {
	prev := tree(map[string]int64{"/a": 10, "/a/b": 10, "/a/b/c": 10})
	curr := tree(map[string]int64{"/a": 10, "/a/b": 10, "/a/b/c": 5010})

	rows := Compute(prev, curr)

	if got := rowFor(t, rows, "/a/b/c").Delta; got != 5000 {
		t.Errorf("/a/b/c delta = %d, want 5000", got)
	}
	for _, ancestor := range []string{"/a", "/a/b"} {
		for _, r := range rows {
			if r.Path == ancestor {
				t.Errorf("ancestor %s should not be charged, got delta %d", ancestor, r.Delta)
			}
		}
	}
}

// An ancestor still reports its subtree total for context even though it is not
// charged for it.
func TestSubtreeDeltaKeepsAncestorContext(t *testing.T) {
	prev := tree(map[string]int64{"/a": 10, "/a/b": 10})
	curr := tree(map[string]int64{"/a": 60, "/a/b": 110})

	rows := Compute(prev, curr)

	a := rowFor(t, rows, "/a")
	if a.Delta != 50 {
		t.Errorf("/a delta = %d, want 50 (own files only)", a.Delta)
	}
	if a.SubtreeDelta != 150 {
		t.Errorf("/a subtree delta = %d, want 150 (own files plus child)", a.SubtreeDelta)
	}
}

// A wholly new subtree collapses to one row at its top, instead of fragmenting
// into a row per nested directory.
func TestNewSubtreeCollapsesToItsTopDirectory(t *testing.T) {
	prev := tree(map[string]int64{"/a": 10})
	curr := tree(map[string]int64{
		"/a": 10, "/a/new": 1, "/a/new/x": 100, "/a/new/x/y": 200,
	})

	rows := Compute(prev, curr)

	top := rowFor(t, rows, "/a/new")
	if top.Kind != model.Added {
		t.Errorf("/a/new kind = %v, want Added", top.Kind)
	}
	if top.Delta != 301 {
		t.Errorf("/a/new delta = %d, want 301 (whole subtree)", top.Delta)
	}
	for _, nested := range []string{"/a/new/x", "/a/new/x/y"} {
		for _, r := range rows {
			if r.Path == nested {
				t.Errorf("nested new dir %s should be folded into /a/new", nested)
			}
		}
	}
}

func TestRemovedSubtreeCollapsesAndIsNegative(t *testing.T) {
	prev := tree(map[string]int64{"/a": 10, "/a/old": 100, "/a/old/deep": 400})
	curr := tree(map[string]int64{"/a": 10})

	rows := Compute(prev, curr)

	gone := rowFor(t, rows, "/a/old")
	if gone.Kind != model.Removed {
		t.Errorf("kind = %v, want Removed", gone.Kind)
	}
	if gone.Delta != -500 {
		t.Errorf("delta = %d, want -500", gone.Delta)
	}
	for _, r := range rows {
		if r.Path == "/a/old/deep" {
			t.Error("nested removed dir should be folded into /a/old")
		}
	}
}

// The conservation invariant: attributed deltas must reconstruct the root's
// total change exactly. This is what guarantees nothing is counted twice or lost.
func TestDeltasSumToTotalRootChange(t *testing.T) {
	prev := tree(map[string]int64{
		"/a": 100, "/a/b": 200, "/a/b/c": 300, "/a/d": 50, "/a/gone": 900,
	})
	curr := tree(map[string]int64{
		"/a": 150, "/a/b": 10, "/a/b/c": 300, "/a/d": 50,
		"/a/fresh": 20, "/a/fresh/deep": 700,
	})

	rows := Compute(prev, curr)

	var summed int64
	for _, r := range rows {
		summed += r.Delta
	}

	var prevRoot, currRoot int64
	for _, d := range prev {
		if d.Path == "/a" {
			prevRoot = d.Usage
		}
	}
	for _, d := range curr {
		if d.Path == "/a" {
			currRoot = d.Usage
		}
	}

	if want := currRoot - prevRoot; summed != want {
		t.Errorf("attributed deltas sum to %d, want root change %d", summed, want)
	}
}

func TestUnchangedDirectoriesAreOmitted(t *testing.T) {
	dirs := map[string]int64{"/a": 10, "/a/b": 20}
	rows := Compute(tree(dirs), tree(dirs))
	if len(rows) != 0 {
		t.Errorf("expected no rows for an unchanged tree, got %v", paths(rows))
	}
}

// Pruned intermediate directories must not break subtree collapsing.
func TestNewSubtreeCollapsesAcrossMissingIntermediateDirs(t *testing.T) {
	prev := tree(map[string]int64{"/a": 10})
	// "/a/new/mid" is below the storage threshold and was never recorded.
	curr := []model.DirStat{
		{Path: "/a", Usage: 910, SelfUsage: 10},
		{Path: "/a/new", Usage: 900, SelfUsage: 0},
		{Path: "/a/new/mid/leaf", Usage: 900, SelfUsage: 900},
	}

	rows := Compute(prev, curr)

	if got := rowFor(t, rows, "/a/new").Delta; got != 900 {
		t.Errorf("/a/new delta = %d, want 900", got)
	}
	for _, r := range rows {
		if r.Path == "/a/new/mid/leaf" {
			t.Error("deep new dir should fold into /a/new even with a gap in recorded dirs")
		}
	}
}

func TestTopGrowthRanksLargestFirstAndDropsShrinkage(t *testing.T) {
	rows := []model.GrowthRow{
		{Path: "/small", Delta: 5},
		{Path: "/big", Delta: 500},
		{Path: "/shrunk", Delta: -900},
		{Path: "/mid", Delta: 50},
	}

	got := paths(TopGrowth(rows, 2))

	if len(got) != 2 || got[0] != "/big" || got[1] != "/mid" {
		t.Errorf("TopGrowth = %v, want [/big /mid]", got)
	}
}

func TestTopChangesIncludesLargeShrinkage(t *testing.T) {
	rows := []model.GrowthRow{
		{Path: "/big", Delta: 500},
		{Path: "/shrunk", Delta: -900},
	}

	if got := paths(TopChanges(rows, 1)); len(got) != 1 || got[0] != "/shrunk" {
		t.Errorf("TopChanges = %v, want [/shrunk]", got)
	}
}
