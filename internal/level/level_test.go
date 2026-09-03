package level

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

// sample is a tree with branches of deliberately different lengths, so that
// levels counted from the leaves and levels counted from the root disagree.
//
//	/r            self 1
//	  /r/deep     self 2
//	    /mid      self 4
//	      /leaf   self 8
//	  /r/flat     self 16   (a leaf sitting right next to a three deep branch)
var sample = map[string]int64{
	"/r":               1,
	"/r/deep":          2,
	"/r/deep/mid":      4,
	"/r/deep/mid/leaf": 8,
	"/r/flat":          16,
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

func TestLevelsAreCountedFromTheLeavesUp(t *testing.T) {
	ix := Build(tree(sample), nil)

	want := map[string]int{
		"/r/deep/mid/leaf": 1,
		"/r/flat":          1,
		"/r/deep/mid":      2,
		"/r/deep":          3,
		"/r":               4,
	}
	for path, lvl := range want {
		if got := ix.Level(path); got != lvl {
			t.Errorf("Level(%s) = %d, want %d", path, got, lvl)
		}
	}
	if got := ix.Max(); got != 4 {
		t.Errorf("Max() = %d, want 4", got)
	}
}

// Levels are heights, so a directory is always strictly above its children.
// That is what keeps rows at one level from containing one another.
func TestParentAlwaysOutranksItsChildren(t *testing.T) {
	ix := Build(tree(sample), nil)

	for _, pair := range [][2]string{
		{"/r", "/r/deep"},
		{"/r/deep", "/r/deep/mid"},
		{"/r/deep/mid", "/r/deep/mid/leaf"},
		{"/r", "/r/flat"},
	} {
		parent, child := pair[0], pair[1]
		if ix.Level(parent) <= ix.Level(child) {
			t.Errorf("%s (level %d) should outrank %s (level %d)",
				parent, ix.Level(parent), child, ix.Level(child))
		}
	}
}

func TestFoldClimbsToTheFirstDirectoryAtOrAboveTheLevel(t *testing.T) {
	ix := Build(tree(sample), nil)

	for _, tc := range []struct {
		path string
		lvl  int
		want string
	}{
		{"/r/deep/mid/leaf", 1, "/r/deep/mid/leaf"},
		{"/r/deep/mid/leaf", 2, "/r/deep/mid"},
		{"/r/deep/mid/leaf", 3, "/r/deep"},
		{"/r/deep/mid/leaf", 4, "/r"},
		// The short branch has no level 2 or 3 directory of its own, so it
		// folds straight onto the root rather than vanishing.
		{"/r/flat", 2, "/r"},
		{"/r/flat", 3, "/r"},
	} {
		if got := ix.Fold(tc.path, tc.lvl); got != tc.want {
			t.Errorf("Fold(%s, %d) = %s, want %s", tc.path, tc.lvl, got, tc.want)
		}
	}
}

// The conservation invariant, one level up: folding must move bytes around
// without creating or losing any.
func TestAggregatedDeltasSumToTheSameTotalAtEveryLevel(t *testing.T) {
	ix := Build(tree(sample), tree(sample))

	rows := []model.GrowthRow{
		{Path: "/r/deep/mid/leaf", Delta: 800},
		{Path: "/r/deep", Delta: -50},
		{Path: "/r/flat", Delta: 30},
		{Path: "/r", Delta: 5},
	}

	var want int64
	for _, r := range rows {
		want += r.Delta
	}

	for lvl := 1; lvl <= ix.Max(); lvl++ {
		var got int64
		for _, r := range ix.Aggregate(rows, lvl) {
			got += r.Delta
		}
		if got != want {
			t.Errorf("level %d deltas sum to %d, want %d", lvl, got, want)
		}
	}
}

func TestAggregateChargesEachParentOnlyWhatNoDeeperRowClaims(t *testing.T) {
	ix := Build(tree(sample), tree(sample))

	rows := []model.GrowthRow{
		{Path: "/r/deep/mid/leaf", Delta: 800},
		{Path: "/r/deep", Delta: 60},
		{Path: "/r/flat", Delta: 30},
	}

	got := ix.Aggregate(rows, 2)

	// /r/deep/mid absorbs the leaf; /r/deep keeps only its own change even
	// though /r/deep/mid sits beneath it; /r takes the short branch.
	if d := rowFor(t, got, "/r/deep/mid").Delta; d != 800 {
		t.Errorf("/r/deep/mid delta = %d, want 800", d)
	}
	if d := rowFor(t, got, "/r/deep").Delta; d != 60 {
		t.Errorf("/r/deep delta = %d, want 60 (its own change only)", d)
	}
	if d := rowFor(t, got, "/r").Delta; d != 30 {
		t.Errorf("/r delta = %d, want 30 (the folded short branch)", d)
	}
}

// A folder whose subtree churned but whose size did not move is not what anyone
// is hunting for, so it must not occupy a slot in the top list.
func TestFoldersThatNetToNoChangeDropOut(t *testing.T) {
	ix := Build(tree(sample), tree(sample))

	rows := []model.GrowthRow{
		{Path: "/r/deep/mid/leaf", Delta: 500},
		{Path: "/r/deep/mid", Delta: -500},
		{Path: "/r/flat", Delta: 7},
	}

	for _, r := range ix.Aggregate(rows, 2) {
		if r.Path == "/r/deep/mid" {
			t.Errorf("/r/deep/mid nets to zero and should not be listed, got delta %d", r.Delta)
		}
	}
}

func TestAggregateLeavesLevelOneAlone(t *testing.T) {
	ix := Build(tree(sample), tree(sample))
	rows := []model.GrowthRow{{Path: "/r/flat", Delta: 3}}

	if got := paths(ix.Aggregate(rows, 1)); len(got) != 1 || got[0] != "/r/flat" {
		t.Errorf("level 1 rows = %v, want them untouched", got)
	}
}

func TestSizesConserveTotalUsageAtEveryLevel(t *testing.T) {
	ix := Build(tree(sample), nil)

	var want int64
	for _, self := range sample {
		want += self
	}

	for lvl := 1; lvl <= ix.Max(); lvl++ {
		var got int64
		for _, r := range ix.Sizes(lvl) {
			got += r.Delta
		}
		if got != want {
			t.Errorf("level %d sizes sum to %d, want the root total %d", lvl, got, want)
		}
	}

	if top := ix.Sizes(ix.Max()); len(top) != 1 || top[0].Path != "/r" {
		t.Errorf("the top level should be the root alone, got %v", paths(top))
	}
}

// Reading a directory as a tree must account for the whole of it: its recorded
// subdirectories plus the bytes it holds itself come back to its own usage.
func TestContentsAddUpToTheDirectoryTheyDescribe(t *testing.T) {
	dirs := tree(sample)
	ix := Build(dirs, nil)

	for _, d := range dirs {
		entries, ok := ix.Contents(d.Path)
		if !ok {
			t.Fatalf("no contents for recorded directory %s", d.Path)
		}
		var total int64
		for _, e := range entries {
			total += e.Usage
		}
		if total != d.Usage {
			t.Errorf("contents of %s total %d, want its usage %d", d.Path, total, d.Usage)
		}
	}
}

func TestContentsListSubfoldersAndTheFilesHeldDirectly(t *testing.T) {
	ix := Build(tree(sample), nil)

	entries, ok := ix.Contents("/r/deep")
	if !ok {
		t.Fatal("/r/deep has no contents")
	}
	if len(entries) != 2 {
		t.Fatalf("/r/deep holds %d entries, want the subfolder and its own files", len(entries))
	}
	if entries[0].Path != "/r/deep/mid" || entries[0].Files {
		t.Errorf("largest entry = %+v, want the /r/deep/mid subfolder", entries[0])
	}
	if !entries[1].Files || entries[1].Usage != 2 {
		t.Errorf("second entry = %+v, want the 2 bytes /r/deep holds itself", entries[1])
	}

	// A leaf holds no recorded folders, so its files are all there is to show.
	leaf, _ := ix.Contents("/r/deep/mid/leaf")
	if len(leaf) != 1 || !leaf[0].Files || leaf[0].Usage != 8 {
		t.Errorf("leaf contents = %+v, want its 8 bytes of files alone", leaf)
	}
}

// A folder deleted since the previous scan is exactly what someone browsing is
// looking for, so it stays listed with the space it used to take.
func TestContentsStillListAFolderThatIsGone(t *testing.T) {
	prev := tree(map[string]int64{"/r": 1, "/r/keep": 2, "/r/keep/gone": 400})
	curr := tree(map[string]int64{"/r": 1, "/r/keep": 2})

	entries, ok := Build(curr, prev).Contents("/r/keep")
	if !ok {
		t.Fatal("/r/keep has no contents")
	}
	got := entries[0]
	if got.Path != "/r/keep/gone" || got.Kind != model.Removed {
		t.Fatalf("first entry = %+v, want /r/keep/gone tagged as removed", got)
	}
	if got.Usage != 0 || got.PrevUsage != 400 {
		t.Errorf("removed folder shows %d now and %d before, want 0 and 400", got.Usage, got.PrevUsage)
	}
}

// A directory gone since the previous scan still has a level to fold onto,
// otherwise removals would disappear the moment you moved up a level.
func TestRemovedDirectoriesStillFold(t *testing.T) {
	prev := tree(map[string]int64{"/r": 1, "/r/keep": 2, "/r/keep/gone": 400})
	curr := tree(map[string]int64{"/r": 1, "/r/keep": 2})

	ix := Build(curr, prev)
	rows := []model.GrowthRow{{Path: "/r/keep/gone", Kind: model.Removed, Delta: -400}}

	got := ix.Aggregate(rows, 2)
	if len(got) != 1 || got[0].Path != "/r/keep" || got[0].Delta != -400 {
		t.Errorf("folded removal = %v, want /r/keep charged -400", got)
	}
}

// With no previous scan there is nothing to be new relative to, so a baseline
// run must not tag every folder as added.
func TestBaselineRunTagsNothingAsNew(t *testing.T) {
	ix := Build(tree(sample), nil)

	for _, r := range ix.Sizes(2) {
		if r.Kind != model.Changed {
			t.Errorf("%s tagged %v on a baseline run, want no tag", r.Path, r.Kind)
		}
	}
}
