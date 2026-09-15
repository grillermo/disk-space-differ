// Package report runs a scan, records it, and diffs it against the previous run.
package report

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/grillermo/disk-space-differ/internal/config"
	"github.com/grillermo/disk-space-differ/internal/diff"
	"github.com/grillermo/disk-space-differ/internal/level"
	"github.com/grillermo/disk-space-differ/internal/model"
	"github.com/grillermo/disk-space-differ/internal/scan"
	"github.com/grillermo/disk-space-differ/internal/store"
)

// historyDepth is how many past snapshots feed the trend sparkline.
const historyDepth = 12

// annotated is how many rows per level get a trend series attached. Ranking only
// needs the deltas, so the history query is limited to the rows that can
// plausibly be displayed rather than every directory on disk.
const annotated = 200

// Result is one root's outcome for a single run.
type Result struct {
	Root     string
	Current  *model.Snapshot
	Previous *model.Snapshot

	// Rows are every attributed change at level 1, largest absolute change first.
	Rows []model.GrowthRow

	// Baseline is true when this run had nothing to compare against. Rows then
	// rank directories by the space they hold rather than by growth.
	Baseline bool

	// Scans is how many recorded scans the comparison spans, Previous being the
	// oldest of them. Two is the narrowest window there is; a wider one reaches
	// further back, so slow growth that each single run barely registers adds up
	// into something visible.
	Scans int

	// Levels and the caches below are built on first use rather than in Run, so
	// that a Result assembled by hand still answers level queries.
	levels  *level.Index
	changes map[int][]model.GrowthRow
	sizes   map[int][]model.GrowthRow
}

// TotalDelta is how much the root grew since the previous run.
func (r *Result) TotalDelta() int64 {
	if r.Previous == nil {
		return 0
	}
	return r.Current.TotalUsage - r.Previous.TotalUsage
}

// Growth returns the n directories at lvl that grew the most.
func (r *Result) Growth(lvl, n int) []model.GrowthRow { return diff.TopGrowth(r.RowsAt(lvl), n) }

// Changes returns the n largest changes at lvl in either direction.
func (r *Result) Changes(lvl, n int) []model.GrowthRow { return diff.TopChanges(r.RowsAt(lvl), n) }

// Largest returns the n directories at lvl holding the most space. Ranking is by
// the bytes charged to each directory, so a big directory does not drag its
// whole chain of ancestors into the list alongside it.
func (r *Result) Largest(lvl, n int) []model.GrowthRow {
	return head(r.SizesAt(lvl), n)
}

// Within ranks what dir holds by the change charged to each part of it: the one
// child containing a change carries it, and dir's own files carry the rest. It
// is what drilling into a folder shows, and unlike Contents it keeps the
// exclusive attribution the rest of the report ranks by, so the rows still sum
// to dir's own change.
func (r *Result) Within(dir string) ([]model.GrowthRow, bool) {
	return r.levelIndex().Within(r.Rows, dir)
}

// SizesWithin ranks what dir holds by size instead of by change, for the size
// view. Like Largest it is a deliberate exception to ranking by change.
func (r *Result) SizesWithin(dir string) ([]model.GrowthRow, bool) {
	return r.levelIndex().SizesWithin(dir)
}

// Contents lists what dir holds directly, for reading one directory as a tree
// instead of as a row in a ranking. See level.Index.Contents: these sizes are
// cumulative and overlap, unlike the deltas everything else here reports.
func (r *Result) Contents(dir string) ([]level.Entry, bool) {
	return r.levelIndex().Contents(dir)
}

// MaxLevel is the coarsest level available, the level of the scan root itself.
func (r *Result) MaxLevel() int { return r.levelIndex().Max() }

// RowsAt returns every attributed change folded onto lvl.
func (r *Result) RowsAt(lvl int) []model.GrowthRow {
	if lvl <= 1 {
		return r.Rows
	}
	if cached, ok := r.changes[lvl]; ok {
		return cached
	}
	rows := r.levelIndex().Aggregate(r.Rows, lvl)
	if r.changes == nil {
		r.changes = map[int][]model.GrowthRow{}
	}
	r.changes[lvl] = rows
	return rows
}

// SizesAt returns the directories at lvl ranked by the space charged to them.
func (r *Result) SizesAt(lvl int) []model.GrowthRow {
	if cached, ok := r.sizes[lvl]; ok {
		return cached
	}
	rows := r.levelIndex().Sizes(lvl)
	if r.sizes == nil {
		r.sizes = map[int][]model.GrowthRow{}
	}
	r.sizes[lvl] = rows
	return rows
}

func (r *Result) levelIndex() *level.Index {
	if r.levels == nil {
		r.levels = level.Build(dirsOf(r.Current), dirsOf(r.Previous))
	}
	return r.levels
}

func dirsOf(snap *model.Snapshot) []model.DirStat {
	if snap == nil {
		return nil
	}
	return snap.Dirs
}

// Run scans root, stores the snapshot, and reports what changed since the
// previous run of the same root.
func Run(
	ctx context.Context,
	st *store.Store,
	cfg config.Config,
	root string,
	onProgress func(scan.Progress),
) (*Result, error) {
	// Read the previous snapshot before storing the new one, so "previous"
	// cannot accidentally resolve to the run in progress.
	previous, prevDirs, err := latest(st, root)
	if err != nil {
		return nil, err
	}

	current, err := scan.Scan(ctx, root, cfg.ScanOptions(), onProgress)
	if err != nil {
		return nil, fmt.Errorf("scanning %s: %w", root, err)
	}

	if _, err := st.Save(current); err != nil {
		return nil, fmt.Errorf("saving snapshot: %w", err)
	}
	if err := st.Prune(root, cfg.Retention); err != nil {
		return nil, fmt.Errorf("pruning old snapshots: %w", err)
	}

	res := build(root, current, previous, prevDirs)
	if err := attachHistory(st, root, res); err != nil {
		return nil, err
	}
	return res, nil
}

// RunSubtree scans one folder inside an already scanned root, records it under
// its own root, and reports what changed in it. It is the targeted rescan the
// TUI's `s` runs: a folder deep in a home directory takes a moment where the
// whole root takes minutes.
//
// The comparison is against the most recent recording that covers dir, which on
// the first targeted scan is the enclosing root's last full scan narrowed to
// dir. Comparing only against dir's own history would make the first press a
// baseline that reports no change at all, and the interesting question — what
// has this folder done since I last looked at the whole tree — would need two
// presses to answer.
//
// enclosing is the scanned root dir sits in; pass "" or dir itself when there is
// none, and only dir's own history is used.
func RunSubtree(
	ctx context.Context,
	st *store.Store,
	cfg config.Config,
	dir, enclosing string,
	onProgress func(scan.Progress),
) (*Result, error) {
	// Read every candidate baseline before storing, so "previous" cannot resolve
	// to the scan in progress.
	previous, prevDirs, err := latest(st, dir)
	if err != nil {
		return nil, err
	}
	if enclosing != "" && enclosing != dir {
		outer, outerDirs, err := latest(st, enclosing)
		if err != nil {
			return nil, err
		}
		if outer != nil && (previous == nil || outer.StartedAt.After(previous.StartedAt)) {
			if snap, dirs := withinSubtree(outer, outerDirs, dir); snap != nil {
				previous, prevDirs = snap, dirs
			}
		}
	}

	current, err := scan.Scan(ctx, dir, cfg.ScanOptions(), onProgress)
	if err != nil {
		return nil, fmt.Errorf("scanning %s: %w", dir, err)
	}

	if _, err := st.Save(current); err != nil {
		return nil, fmt.Errorf("saving snapshot: %w", err)
	}
	if err := st.Prune(dir, cfg.Retention); err != nil {
		return nil, fmt.Errorf("pruning old snapshots: %w", err)
	}

	res := build(dir, current, previous, prevDirs)
	if err := attachHistory(st, dir, res); err != nil {
		return nil, err
	}
	return res, nil
}

// withinSubtree narrows a recorded snapshot to the part of it under dir, so that
// a freshly scanned folder can be diffed against it as if it had been scanned on
// its own. Both sides then describe the same path space, which is what keeps
// diff.Compute's attribution — and its conservation invariant — intact.
//
// A dir the snapshot never recorded (it was below the size threshold, or did not
// exist yet) yields nil: there is nothing to measure against, and an empty
// baseline would report the whole folder as new.
func withinSubtree(snap *model.Snapshot, dirs []model.DirStat, dir string) (*model.Snapshot, []model.DirStat) {
	prefix := strings.TrimSuffix(dir, "/") + "/"
	var kept []model.DirStat
	var root *model.DirStat
	for i, d := range dirs {
		switch {
		case d.Path == dir:
			root = &dirs[i]
		case !strings.HasPrefix(d.Path, prefix):
			continue
		}
		kept = append(kept, d)
	}
	if root == nil {
		return nil, nil
	}

	return &model.Snapshot{
		ID:         snap.ID,
		Root:       dir,
		StartedAt:  snap.StartedAt,
		Duration:   snap.Duration,
		TotalUsage: root.Usage,
		ItemCount:  root.ItemCount,
		Dirs:       kept,
	}, kept
}

// ErrNeedTwoScans reports that a root has fewer than the two recorded snapshots
// a scan-free comparison needs. Callers that can offer to scan (the TUI) match
// on it rather than on the message.
var ErrNeedTwoScans = errors.New("needs two recorded scans to compare")

// MinScans is the narrowest window a comparison can have: something to measure,
// and something to measure it against.
const MinScans = 2

// StoredCount is how many snapshots root has recorded, which is how far back a
// scan-free report can be asked to reach.
func StoredCount(st *store.Store, root string) (int, error) {
	n, err := st.Count(root)
	if err != nil {
		return 0, fmt.Errorf("counting snapshots for %s: %w", root, err)
	}
	return n, nil
}

// FromStore reports the change across the last scans recorded for root, without
// touching the disk. It is what -no-scan reads: the comparison ends where the
// last scan ended, so it stays put rather than drifting with the filesystem.
//
// scans is how many recorded scans the window spans — 2 compares the last two
// runs, 5 measures the newest against the fifth-newest. A root with fewer than
// that stored is compared across everything it has, so widening the window past
// the end of the history is harmless rather than an error.
//
// Unlike Run it records nothing, so repeated calls keep answering the same
// question instead of each one becoming the next one's baseline. That is also
// why a single stored snapshot is an error rather than a baseline report: Run
// records a baseline as a side effect of scanning, and this never scans.
func FromStore(st *store.Store, root string, scans int) (*Result, error) {
	recent, err := st.Recent(root, max(MinScans, scans))
	if err != nil {
		return nil, fmt.Errorf("loading snapshots for %s: %w", root, err)
	}
	if len(recent) < MinScans {
		return nil, fmt.Errorf("%s has %d of %d recorded scans: %w",
			root, len(recent), MinScans, ErrNeedTwoScans)
	}

	current, previous := &recent[0], &recent[len(recent)-1]
	if current.Dirs, err = st.Dirs(current.ID); err != nil {
		return nil, fmt.Errorf("loading directories: %w", err)
	}
	prevDirs, err := st.Dirs(previous.ID)
	if err != nil {
		return nil, fmt.Errorf("loading previous directories: %w", err)
	}
	previous.Dirs = prevDirs

	res := build(root, current, previous, prevDirs)
	res.Scans = len(recent)
	if err := attachHistory(st, root, res); err != nil {
		return nil, err
	}
	return res, nil
}

// build turns a pair of snapshots into a ranked result. With nothing to compare
// against it falls back to ranking by the space each directory holds.
func build(root string, current, previous *model.Snapshot, prevDirs []model.DirStat) *Result {
	res := &Result{Root: root, Current: current, Previous: previous, Scans: MinScans}
	if previous == nil {
		res.Baseline = true
		res.Scans = 1
		res.Rows = baselineRows(current.Dirs)
		return res
	}

	res.Rows = diff.Compute(prevDirs, current.Dirs)
	sort.Slice(res.Rows, func(i, j int) bool {
		return absInt64(res.Rows[i].Delta) > absInt64(res.Rows[j].Delta)
	})
	return res
}

// latest loads the most recent stored snapshot for root, or nil if there is none.
func latest(st *store.Store, root string) (*model.Snapshot, []model.DirStat, error) {
	recent, err := st.Recent(root, 1)
	if err != nil {
		return nil, nil, fmt.Errorf("loading previous snapshot: %w", err)
	}
	if len(recent) == 0 {
		return nil, nil, nil
	}

	snap := recent[0]
	dirs, err := st.Dirs(snap.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("loading previous directories: %w", err)
	}
	snap.Dirs = dirs
	return &snap, dirs, nil
}

// baselineRows ranks directories by the bytes they hold directly, matching how
// growth is attributed. Ranking by cumulative size instead would fill the list
// with each big directory's chain of ancestors.
func baselineRows(dirs []model.DirStat) []model.GrowthRow {
	rows := make([]model.GrowthRow, 0, len(dirs))
	for _, d := range dirs {
		rows = append(rows, model.GrowthRow{
			Path:  d.Path,
			Kind:  model.Changed,
			Delta: d.SelfUsage,
			Usage: d.Usage,
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Delta > rows[j].Delta })
	return rows
}

// attachHistory gives every level's displayable rows a usage series for the
// trend sparkline.
//
// All levels are annotated up front because the level being viewed is chosen
// after the scan has finished, and rescanning a home directory just to fill in a
// sparkline would be absurd. Each level's own rows are annotated in place, so
// the caches keep the series; the ranking helpers copy rows and would not.
func attachHistory(st *store.Store, root string, res *Result) error {
	sets := make([][]model.GrowthRow, 0, 2*res.MaxLevel())
	for lvl := 1; lvl <= res.MaxLevel(); lvl++ {
		sets = append(sets,
			head(res.RowsAt(lvl), annotated),
			head(res.SizesAt(lvl), annotated),
		)
	}

	seen := map[string]bool{}
	var paths []string
	for _, set := range sets {
		for _, row := range set {
			if !seen[row.Path] {
				seen[row.Path] = true
				paths = append(paths, row.Path)
			}
		}
	}
	if len(paths) == 0 {
		return nil
	}

	hist, err := st.History(root, paths, historyDepth)
	if err != nil {
		return fmt.Errorf("loading history: %w", err)
	}
	for _, set := range sets {
		for i := range set {
			set[i].History = hist[set[i].Path]
		}
	}
	return nil
}

// head returns the first n rows, sharing storage with rows so that annotating
// the result annotates the original.
func head(rows []model.GrowthRow, n int) []model.GrowthRow {
	if n > 0 && len(rows) > n {
		return rows[:n]
	}
	return rows
}

func absInt64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
