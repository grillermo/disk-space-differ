// Package htmlreport turns stored snapshots into a self-contained HTML report.
package htmlreport

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/grillermo/disk-space-differ/internal/diff"
	"github.com/grillermo/disk-space-differ/internal/model"
	"github.com/grillermo/disk-space-differ/internal/store"
)

const (
	// maxSeries caps the trend chart. Categorical hues are assigned in a fixed
	// order and never cycled, so the tail is folded away rather than given
	// invented colours.
	maxSeries = 6
	// maxChanges caps the change chart; the table below it carries the rest.
	maxChanges = 25
	// candidates is how many directories are considered for the trend chart
	// before ranking them by growth across the range.
	candidates = 60
)

// Series is one directory's usage across a range, aligned to that range's Times.
type Series struct {
	Path   string  `json:"path"`
	Label  string  `json:"label"`
	Points []int64 `json:"points"`
	Growth int64   `json:"growth"`
}

// Change is one directory's exclusively attributed change across a range.
type Change struct {
	Path  string `json:"path"`
	Label string `json:"label"`
	Delta int64  `json:"delta"`
	Usage int64  `json:"usage"`
	Kind  string `json:"kind"`
}

// Range is the report scoped to the last N runs. Every chart, stat and table on
// the page reads from exactly one of these, so switching range moves all of
// them together and the numbers always agree.
//
// Ranges are precomputed here rather than sliced in the browser because growth
// is attributed exclusively, which needs each directory's own-bytes figure at
// both endpoints. Cumulative sizes alone would reintroduce the double counting
// the attribution exists to remove.
type Range struct {
	Key   string `json:"key"`
	Label string `json:"label"`

	Times  []int64 `json:"times"`
	Totals []int64 `json:"totals"`
	Items  []int64 `json:"items"`

	Series  []Series `json:"series"`
	Changes []Change `json:"changes"`

	Runs       int   `json:"runs"`
	StartTotal int64 `json:"startTotal"`
	EndTotal   int64 `json:"endTotal"`
	Delta      int64 `json:"delta"`
	Grew       int64 `json:"grew"`
	Freed      int64 `json:"freed"`
}

// RootData is everything the page needs about one scanned root.
type RootData struct {
	Root     string  `json:"root"`
	Label    string  `json:"label"`
	Ranges   []Range `json:"ranges"`
	LastScan int64   `json:"lastScan"`
	Baseline bool    `json:"baseline"`
}

// Data is the whole report payload.
type Data struct {
	Generated int64      `json:"generated"`
	Version   string     `json:"version"`
	Roots     []RootData `json:"roots"`
}

// Build assembles report data for the given roots. Roots with no stored
// snapshots are skipped.
func Build(st *store.Store, roots []string, window int, version string) (Data, error) {
	data := Data{Generated: time.Now().Unix(), Version: version}

	for _, root := range roots {
		rd, ok, err := buildRoot(st, root, window)
		if err != nil {
			return data, err
		}
		if ok {
			data.Roots = append(data.Roots, rd)
		}
	}

	if len(data.Roots) == 0 {
		return data, fmt.Errorf("no stored snapshots found for %s; run a scan first",
			strings.Join(roots, ", "))
	}
	return data, nil
}

func buildRoot(st *store.Store, root string, window int) (RootData, bool, error) {
	recent, err := st.Recent(root, window)
	if err != nil {
		return RootData{}, false, err
	}
	if len(recent) == 0 {
		return RootData{}, false, nil
	}

	// Recent is newest first; charts read left to right through time.
	snaps := make([]model.Snapshot, len(recent))
	for i, s := range recent {
		snaps[len(recent)-1-i] = s
	}

	rd := RootData{
		Root:     root,
		Label:    prettyPath(root),
		LastScan: snaps[len(snaps)-1].StartedAt.Unix(),
		Baseline: len(snaps) < 2,
	}

	dirCache := map[int64][]model.DirStat{}
	loadDirs := func(id int64) ([]model.DirStat, error) {
		if d, ok := dirCache[id]; ok {
			return d, nil
		}
		d, err := st.Dirs(id)
		if err != nil {
			return nil, err
		}
		dirCache[id] = d
		return d, nil
	}

	for _, spec := range rangeSpecs(len(snaps)) {
		r, err := buildRange(st, root, snaps, spec, loadDirs)
		if err != nil {
			return rd, false, err
		}
		rd.Ranges = append(rd.Ranges, r)
	}

	return rd, true, nil
}

type rangeSpec struct {
	key   string
	label string
	runs  int
}

// rangeSpecs returns the range options worth offering for the number of runs
// available, widest last so the page can default to the full history.
func rangeSpecs(available int) []rangeSpec {
	if available < 2 {
		return []rangeSpec{{key: "all", label: "All runs", runs: available}}
	}

	var out []rangeSpec
	seen := map[int]bool{}
	add := func(key, label string, runs int) {
		if runs < 2 || runs > available || seen[runs] {
			return
		}
		seen[runs] = true
		out = append(out, rangeSpec{key: key, label: label, runs: runs})
	}

	add("last", "Since last run", 2)
	add("7", "Last 7 runs", 7)
	add("30", "Last 30 runs", 30)
	add("all", "All runs", available)
	return out
}

func buildRange(
	st *store.Store,
	root string,
	snaps []model.Snapshot,
	spec rangeSpec,
	loadDirs func(int64) ([]model.DirStat, error),
) (Range, error) {
	slice := snaps[len(snaps)-spec.runs:]

	r := Range{
		Key:        spec.key,
		Label:      spec.label,
		Runs:       len(slice),
		StartTotal: slice[0].TotalUsage,
		EndTotal:   slice[len(slice)-1].TotalUsage,
	}
	r.Delta = r.EndTotal - r.StartTotal
	for _, s := range slice {
		r.Times = append(r.Times, s.StartedAt.Unix())
		r.Totals = append(r.Totals, s.TotalUsage)
		r.Items = append(r.Items, s.ItemCount)
	}

	endDirs, err := loadDirs(slice[len(slice)-1].ID)
	if err != nil {
		return r, err
	}

	var changes []model.GrowthRow
	if len(slice) >= 2 {
		startDirs, err := loadDirs(slice[0].ID)
		if err != nil {
			return r, err
		}
		changes = diff.Compute(startDirs, endDirs)
	}

	for _, c := range changes {
		if c.Delta > 0 {
			r.Grew += c.Delta
		} else {
			r.Freed -= c.Delta
		}
	}
	r.Changes = topChanges(changes)

	series, err := trendSeries(st, root, len(slice), endDirs, changes)
	if err != nil {
		return r, err
	}
	r.Series = series

	return r, nil
}

// topChanges picks the largest changes in either direction for the change chart.
func topChanges(rows []model.GrowthRow) []Change {
	sorted := append([]model.GrowthRow(nil), rows...)
	sort.Slice(sorted, func(i, j int) bool {
		a, b := abs(sorted[i].Delta), abs(sorted[j].Delta)
		if a != b {
			return a > b
		}
		return sorted[i].Path < sorted[j].Path
	})
	if len(sorted) > maxChanges {
		sorted = sorted[:maxChanges]
	}

	out := make([]Change, 0, len(sorted))
	for _, r := range sorted {
		out = append(out, Change{
			Path:  r.Path,
			Label: prettyPath(r.Path),
			Delta: r.Delta,
			Usage: r.Usage,
			Kind:  r.Kind.String(),
		})
	}
	return out
}

// trendSeries picks the directories worth plotting over time and loads their
// history.
//
// Selection is by *exclusive* growth, never by cumulative growth. Ranking on
// cumulative size would fill the chart with a directory and all of its
// ancestors, each drawing the same bytes again — the very double counting the
// attribution exists to remove. Nested candidates are then skipped outright, so
// the plotted lines cover disjoint parts of the tree and can be read together.
func trendSeries(
	st *store.Store,
	root string,
	runs int,
	endDirs []model.DirStat,
	changes []model.GrowthRow,
) ([]Series, error) {
	movers := make([]model.GrowthRow, 0, len(changes))
	for _, c := range changes {
		if c.Delta > 0 {
			movers = append(movers, c)
		}
	}
	if len(movers) == 0 {
		return nil, nil
	}

	sort.Slice(movers, func(i, j int) bool {
		if movers[i].Delta != movers[j].Delta {
			return movers[i].Delta > movers[j].Delta
		}
		return movers[i].Path < movers[j].Path
	})
	if len(movers) > candidates {
		movers = movers[:candidates]
	}

	var chosen []model.GrowthRow
	for _, m := range movers {
		if len(chosen) >= maxSeries {
			break
		}
		if overlapsAny(m.Path, chosen) {
			continue
		}
		chosen = append(chosen, m)
	}
	if len(chosen) == 0 {
		return nil, nil
	}

	paths := make([]string, len(chosen))
	for i, c := range chosen {
		paths[i] = c.Path
	}

	hist, err := st.History(root, paths, runs)
	if err != nil {
		return nil, err
	}

	series := make([]Series, 0, len(chosen))
	for _, c := range chosen {
		points, ok := hist[c.Path]
		if !ok || len(points) == 0 {
			continue
		}
		series = append(series, Series{
			Path:   c.Path,
			Label:  prettyPath(c.Path),
			Points: points,
			Growth: c.Delta,
		})
	}
	return series, nil
}

// overlapsAny reports whether path is an ancestor or descendant of one already
// chosen, which would put the same bytes on the chart twice.
func overlapsAny(path string, chosen []model.GrowthRow) bool {
	for _, c := range chosen {
		if path == c.Path ||
			strings.HasPrefix(path, c.Path+"/") ||
			strings.HasPrefix(c.Path, path+"/") {
			return true
		}
	}
	return false
}

// prettyPath shortens the home directory to ~ so labels stay readable.
func prettyPath(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if p == home {
		return "~"
	}
	if strings.HasPrefix(p, home+"/") {
		return "~" + p[len(home):]
	}
	return p
}

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
