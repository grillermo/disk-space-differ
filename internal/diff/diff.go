// Package diff attributes growth between two snapshots to individual directories.
package diff

import (
	"path/filepath"
	"sort"

	"github.com/grillermo/disk-space-differ/internal/model"
)

// Compute attributes the change between prev and curr across directories so
// that every byte is charged to exactly one directory.
//
// Directory sizes are cumulative, so a naive comparison reports the same growth
// once for every ancestor of the directory that actually changed. A 5 GB
// download into ~/a/b/c would show up as +5 GB on ~/a, ~/a/b and ~/a/b/c alike,
// and a "top 20" list would be a chain of duplicates hiding the real cause.
//
// Two rules avoid that:
//
//   - A directory present in both scans is charged only the change in the bytes
//     held directly in it (SelfUsage). Its descendants account for themselves.
//   - A directory absent from the previous scan is charged its entire subtree,
//     but only if its parent already existed. Otherwise the whole new subtree
//     collapses onto its topmost new ancestor, so a freshly cloned repo reads as
//     one row rather than a few hundred leaf rows.
//
// Removals mirror the additions. The resulting deltas sum to the root's total
// change.
func Compute(prev, curr []model.DirStat) []model.GrowthRow {
	prevByPath := index(prev)
	currByPath := index(curr)

	rows := make([]model.GrowthRow, 0, len(curr))

	for _, d := range curr {
		before, existed := prevByPath[d.Path]
		if existed {
			delta := d.SelfUsage - before.SelfUsage
			if delta == 0 {
				continue
			}
			rows = append(rows, model.GrowthRow{
				Path:         d.Path,
				Kind:         model.Changed,
				Delta:        delta,
				SubtreeDelta: d.Usage - before.Usage,
				Usage:        d.Usage,
				PrevUsage:    before.Usage,
			})
			continue
		}

		// New directory. Skip it when an ancestor is also new, so the entire
		// new subtree is reported once at its highest point.
		if anc, ok := nearestAncestor(d.Path, currByPath); ok {
			if _, ancExisted := prevByPath[anc]; !ancExisted {
				continue
			}
		}
		rows = append(rows, model.GrowthRow{
			Path:         d.Path,
			Kind:         model.Added,
			Delta:        d.Usage,
			SubtreeDelta: d.Usage,
			Usage:        d.Usage,
		})
	}

	for _, d := range prev {
		if _, stillThere := currByPath[d.Path]; stillThere {
			continue
		}
		if anc, ok := nearestAncestor(d.Path, prevByPath); ok {
			if _, ancStillThere := currByPath[anc]; !ancStillThere {
				continue
			}
		}
		rows = append(rows, model.GrowthRow{
			Path:         d.Path,
			Kind:         model.Removed,
			Delta:        -d.Usage,
			SubtreeDelta: -d.Usage,
			PrevUsage:    d.Usage,
		})
	}

	return rows
}

// TopGrowth returns the n rows that grew the most, largest first.
func TopGrowth(rows []model.GrowthRow, n int) []model.GrowthRow {
	out := make([]model.GrowthRow, 0, len(rows))
	for _, r := range rows {
		if r.Delta > 0 {
			out = append(out, r)
		}
	}
	sortByAbsDelta(out)
	return clamp(out, n)
}

// TopChanges returns the n rows with the largest change in either direction.
func TopChanges(rows []model.GrowthRow, n int) []model.GrowthRow {
	out := append([]model.GrowthRow(nil), rows...)
	sortByAbsDelta(out)
	return clamp(out, n)
}

func sortByAbsDelta(rows []model.GrowthRow) {
	sort.Slice(rows, func(i, j int) bool {
		a, b := abs(rows[i].Delta), abs(rows[j].Delta)
		if a != b {
			return a > b
		}
		return rows[i].Path < rows[j].Path
	})
}

func clamp(rows []model.GrowthRow, n int) []model.GrowthRow {
	if n > 0 && len(rows) > n {
		return rows[:n]
	}
	return rows
}

func index(dirs []model.DirStat) map[string]model.DirStat {
	m := make(map[string]model.DirStat, len(dirs))
	for _, d := range dirs {
		m[d.Path] = d
	}
	return m
}

// nearestAncestor walks up from path and returns the closest ancestor present
// in set. Directories below the storage threshold are not recorded, so the
// immediate parent is not guaranteed to be there.
func nearestAncestor(path string, set map[string]model.DirStat) (string, bool) {
	for p := filepath.Dir(path); ; p = filepath.Dir(p) {
		if _, ok := set[p]; ok {
			return p, true
		}
		if p == "/" || p == "." || p == filepath.Dir(p) {
			return "", false
		}
	}
}

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
