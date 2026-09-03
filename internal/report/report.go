// Package report runs a scan, records it, and diffs it against the previous run.
package report

import (
	"context"
	"fmt"
	"sort"

	"github.com/grillermo/disk-space-differ/internal/config"
	"github.com/grillermo/disk-space-differ/internal/diff"
	"github.com/grillermo/disk-space-differ/internal/model"
	"github.com/grillermo/disk-space-differ/internal/scan"
	"github.com/grillermo/disk-space-differ/internal/store"
)

// historyDepth is how many past snapshots feed the trend sparkline.
const historyDepth = 12

// annotated is how many rows get a trend series attached. Ranking only needs
// the deltas, so the history query is limited to the rows that can plausibly be
// displayed rather than every directory on disk.
const annotated = 200

// Result is one root's outcome for a single run.
type Result struct {
	Root     string
	Current  *model.Snapshot
	Previous *model.Snapshot

	// Rows are every attributed change, largest absolute change first.
	Rows []model.GrowthRow

	// Baseline is true when this run had nothing to compare against. Rows then
	// rank directories by the space they hold rather than by growth.
	Baseline bool
}

// TotalDelta is how much the root grew since the previous run.
func (r *Result) TotalDelta() int64 {
	if r.Previous == nil {
		return 0
	}
	return r.Current.TotalUsage - r.Previous.TotalUsage
}

// Growth returns the n rows that grew the most.
func (r *Result) Growth(n int) []model.GrowthRow { return diff.TopGrowth(r.Rows, n) }

// Changes returns the n largest changes in either direction.
func (r *Result) Changes(n int) []model.GrowthRow { return diff.TopChanges(r.Rows, n) }

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

	if err := attachHistory(st, root, res.Rows); err != nil {
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

func attachHistory(st *store.Store, root string, rows []model.GrowthRow) error {
	limit := min(len(rows), annotated)
	if limit == 0 {
		return nil
	}

	paths := make([]string, limit)
	for i := range paths {
		paths[i] = rows[i].Path
	}

	hist, err := st.History(root, paths, historyDepth)
	if err != nil {
		return fmt.Errorf("loading history: %w", err)
	}
	for i := range rows[:limit] {
		rows[i].History = hist[rows[i].Path]
	}
	return nil
}

func absInt64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
