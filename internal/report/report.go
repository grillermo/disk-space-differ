// Package report runs a scan, records it, and diffs it against the previous run.
package report

import (
	"context"
	"fmt"
	"sort"

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

	res := &Result{Root: root, Current: current, Previous: previous}
	if previous == nil {
		res.Baseline = true
		res.Rows = baselineRows(current.Dirs)
	} else {
		res.Rows = diff.Compute(prevDirs, current.Dirs)
		sort.Slice(res.Rows, func(i, j int) bool {
			return absInt64(res.Rows[i].Delta) > absInt64(res.Rows[j].Delta)
		})
	}

	if err := attachHistory(st, root, res); err != nil {
		return nil, err
	}
	return res, nil
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
