// Package level groups directories into tree levels so a report can be read at
// any granularity, from the leaves up to the scan root.
package level

import (
	"path/filepath"
	"sort"

	"github.com/grillermo/disk-space-differ/internal/model"
)

// Index assigns every recorded directory a level and folds paths onto whichever
// directory represents them at a coarser level.
//
// A directory's level is its height in the recorded tree: a directory holding no
// other recorded directory is level 1, one holding at least one level 1
// directory is level 2, and so on up to the scan root. Height rather than depth
// is what makes levels comparable across branches of different lengths — level 1
// always means "where the bytes actually sit", wherever in the tree you look.
//
// Height strictly increases from a directory to its parent, so no two
// directories at the same level can contain one another. Folding rows onto their
// level N representatives is therefore a partition: the folded deltas still sum
// to the total change, with nothing counted twice.
type Index struct {
	nodes   map[string]*node
	max     int
	hasPrev bool

	// children is the tree read downwards, built on first use because only the
	// inspect view needs it.
	children map[string][]string
}

type node struct {
	height        int
	usage         int64
	selfUsage     int64
	prevUsage     int64
	prevSelfUsage int64
	inCurr        bool
	inPrev        bool
}

// Build indexes the directories of the current scan, using the previous scan's
// directories so that removed paths still have a level to fold onto.
func Build(curr, prev []model.DirStat) *Index {
	ix := &Index{
		nodes:   make(map[string]*node, len(curr)+len(prev)),
		hasPrev: len(prev) > 0,
	}
	for _, d := range curr {
		n := ix.node(d.Path)
		n.usage, n.selfUsage, n.inCurr = d.Usage, d.SelfUsage, true
	}
	for _, d := range prev {
		n := ix.node(d.Path)
		n.prevUsage, n.prevSelfUsage, n.inPrev = d.Usage, d.SelfUsage, true
	}
	ix.computeHeights()
	return ix
}

func (ix *Index) node(path string) *node {
	n, ok := ix.nodes[path]
	if !ok {
		n = &node{height: 1}
		ix.nodes[path] = n
	}
	return n
}

// computeHeights raises each recorded directory to one above its tallest
// recorded descendant.
//
// An ancestor's path is always a strict prefix of its descendants', hence always
// shorter. Visiting the longest paths first therefore guarantees a directory's
// own height is final by the time it hands it to its nearest recorded ancestor,
// with no recursion and no need for the tree itself.
func (ix *Index) computeHeights() {
	paths := make([]string, 0, len(ix.nodes))
	for p := range ix.nodes {
		paths = append(paths, p)
	}
	sort.Slice(paths, func(i, j int) bool {
		if len(paths[i]) != len(paths[j]) {
			return len(paths[i]) > len(paths[j])
		}
		return paths[i] < paths[j]
	})

	for _, p := range paths {
		h := ix.nodes[p].height
		if h > ix.max {
			ix.max = h
		}
		if anc, ok := ix.ancestor(p); ok && h+1 > ix.nodes[anc].height {
			ix.nodes[anc].height = h + 1
		}
	}
}

// ancestor returns the closest recorded ancestor of path. Directories below the
// scan threshold are never recorded, so the immediate parent may be absent.
func (ix *Index) ancestor(path string) (string, bool) {
	for p := filepath.Dir(path); ; p = filepath.Dir(p) {
		if _, ok := ix.nodes[p]; ok {
			return p, true
		}
		if p == filepath.Dir(p) {
			return "", false
		}
	}
}

// Max is the highest level available, which is the level of the scan root.
func (ix *Index) Max() int {
	if ix.max < 1 {
		return 1
	}
	return ix.max
}

// Level is the level of path, or 0 if it was never recorded.
func (ix *Index) Level(path string) int {
	if n, ok := ix.nodes[path]; ok {
		return n.height
	}
	return 0
}

// Fold returns the directory representing path at lvl: the closest
// ancestor-or-self whose level is at least lvl. A path with no such ancestor
// represents itself.
func (ix *Index) Fold(path string, lvl int) string {
	if lvl <= 1 {
		return path
	}
	for p := path; ; {
		if n, ok := ix.nodes[p]; ok && n.height >= lvl {
			return p
		}
		parent := filepath.Dir(p)
		if parent == p {
			return path
		}
		p = parent
	}
}

// Aggregate folds rows onto their level lvl representatives and sums the deltas,
// largest absolute change first. Level 1 returns rows untouched, since there
// every recorded directory already represents itself.
//
// A representative whose members cancel out is dropped: a folder whose subtree
// churned but did not change size is not something anyone is looking for.
func (ix *Index) Aggregate(rows []model.GrowthRow, lvl int) []model.GrowthRow {
	if lvl <= 1 {
		return rows
	}

	sums := make(map[string]int64, len(rows))
	reps := make([]string, 0, len(rows))
	for _, r := range rows {
		rep := ix.Fold(r.Path, lvl)
		if _, seen := sums[rep]; !seen {
			reps = append(reps, rep)
		}
		sums[rep] += r.Delta
	}

	out := make([]model.GrowthRow, 0, len(reps))
	for _, rep := range reps {
		if sums[rep] == 0 {
			continue
		}
		out = append(out, ix.row(rep, sums[rep]))
	}
	sortByAbsDelta(out)
	return out
}

// Sizes ranks the level lvl directories by the bytes charged to them: everything
// they hold that no deeper directory at the same level already accounts for.
func (ix *Index) Sizes(lvl int) []model.GrowthRow {
	sums := make(map[string]int64, len(ix.nodes))
	for path, n := range ix.nodes {
		if !n.inCurr {
			continue
		}
		sums[ix.Fold(path, lvl)] += n.selfUsage
	}

	out := make([]model.GrowthRow, 0, len(sums))
	for rep, size := range sums {
		out = append(out, ix.row(rep, size))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Delta != out[j].Delta {
			return out[i].Delta > out[j].Delta
		}
		return out[i].Path < out[j].Path
	})
	return out
}

// Entry is one thing a directory holds: either a recorded subdirectory, or the
// aggregate of the bytes the directory holds itself.
type Entry struct {
	Path      string
	Kind      model.ChangeKind
	Usage     int64
	PrevUsage int64

	// Files marks the aggregate entry: the files sitting directly in the
	// directory, plus every subdirectory too small to have been recorded. It
	// stands for bytes rather than for a directory, so there is nothing inside
	// it to open.
	Files bool
}

// Contents lists what path holds directly, largest first: one entry per recorded
// subdirectory plus one for the bytes path holds itself. The second return is
// false for a path that was never recorded, which has no contents rather than
// empty ones.
//
// Sizes here are cumulative and add up to path's own usage, because reading a
// directory as a tree asks "what is in here" and the answer has to account for
// the whole of it. That is deliberately not the exclusive attribution the report
// ranks by; these numbers overlap between an entry and the entries inside it,
// and must never be summed across levels.
func (ix *Index) Contents(path string) ([]Entry, bool) {
	n, ok := ix.nodes[path]
	if !ok {
		return nil, false
	}

	kids := ix.childIndex()[path]
	entries := make([]Entry, 0, len(kids)+1)
	for _, kid := range kids {
		c := ix.nodes[kid]
		entries = append(entries, Entry{
			Path:      kid,
			Kind:      ix.kind(c),
			Usage:     c.usage,
			PrevUsage: c.prevUsage,
		})
	}

	// A directory whose bytes all sit in recorded subdirectories holds no files
	// of its own; a row of zeroes would only be noise.
	if n.selfUsage != 0 || n.prevSelfUsage != 0 {
		entries = append(entries, Entry{
			Path:      path,
			Files:     true,
			Usage:     n.selfUsage,
			PrevUsage: n.prevSelfUsage,
		})
	}

	// Ranked by the space an entry accounts for in either scan, not just in the
	// current one: a folder that has just been emptied or deleted holds nothing
	// now, and is the last thing that should be pushed to the bottom of the list.
	sort.Slice(entries, func(i, j int) bool {
		a, b := entries[i].peak(), entries[j].peak()
		if a != b {
			return a > b
		}
		return entries[i].Path < entries[j].Path
	})
	return entries, true
}

// peak is the larger of an entry's two sizes.
func (e Entry) peak() int64 {
	if e.PrevUsage > e.Usage {
		return e.PrevUsage
	}
	return e.Usage
}

// childIndex maps each recorded directory to the directories it holds.
//
// The tree is reassembled by nearest recorded ancestor rather than by parent
// path: directories below the scan threshold are never recorded, and a path that
// was recorded by the previous scan alone may sit under one that the current
// scan dropped.
func (ix *Index) childIndex() map[string][]string {
	if ix.children != nil {
		return ix.children
	}
	ix.children = make(map[string][]string, len(ix.nodes))
	for path := range ix.nodes {
		if anc, ok := ix.ancestor(path); ok {
			ix.children[anc] = append(ix.children[anc], path)
		}
	}
	return ix.children
}

// row describes rep as a growth row carrying delta.
func (ix *Index) row(rep string, delta int64) model.GrowthRow {
	n, ok := ix.nodes[rep]
	if !ok {
		return model.GrowthRow{Path: rep, Delta: delta, SubtreeDelta: delta}
	}
	return model.GrowthRow{
		Path:         rep,
		Kind:         ix.kind(n),
		Delta:        delta,
		SubtreeDelta: n.usage - n.prevUsage,
		Usage:        n.usage,
		PrevUsage:    n.prevUsage,
	}
}

// kind tags a representative as new or gone. With no previous scan to compare
// against, nothing is new, so a baseline run is left untagged.
func (ix *Index) kind(n *node) model.ChangeKind {
	switch {
	case !ix.hasPrev:
		return model.Changed
	case !n.inCurr:
		return model.Removed
	case !n.inPrev:
		return model.Added
	default:
		return model.Changed
	}
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

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
