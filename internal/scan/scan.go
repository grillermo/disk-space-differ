// Package scan walks a directory tree with gdu's parallel analyzer and turns
// the result into snapshot rows.
package scan

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dundee/gdu/v5/pkg/analyze"
	gdufs "github.com/dundee/gdu/v5/pkg/fs"
	log "github.com/sirupsen/logrus"

	"github.com/grillermo/disk-space-differ/internal/model"
)

// gdu logs unreadable paths to stderr through logrus at info level. On a home
// directory that is hundreds of lines of noise, and in the TUI it is written
// straight over the alternate screen. Unreadable entries are counted and
// reported instead, so the log output is discarded here rather than at the call
// site, where forgetting it would corrupt the display.
func init() {
	log.SetOutput(io.Discard)
}

// DefaultMinDirSize is the smallest cumulative size a directory needs before it
// is recorded on its own row. Smaller directories are folded into their nearest
// recorded ancestor, which keeps a snapshot of a home directory in the low
// megabytes instead of storing hundreds of thousands of near empty rows.
const DefaultMinDirSize = 1 << 20 // 1 MiB

// Progress reports how far a running scan has got.
type Progress struct {
	ItemCount   int64
	TotalUsage  int64
	CurrentItem string
}

// Options configures a scan.
type Options struct {
	// IgnorePaths are absolute directory paths to skip entirely.
	IgnorePaths []string
	// IgnoreNames are directory base names to skip anywhere in the tree.
	IgnoreNames []string
	// MinDirSize is the recording threshold; zero means DefaultMinDirSize.
	MinDirSize int64
	// FollowSymlinks counts symlinked content. Off by default, because
	// following links double counts targets and can cycle.
	FollowSymlinks bool
}

func (o Options) minDirSize() int64 {
	if o.MinDirSize > 0 {
		return o.MinDirSize
	}
	return DefaultMinDirSize
}

// Scan walks root and returns a snapshot. onProgress may be nil; when set it is
// called roughly every 100ms on a separate goroutine.
//
// Cancelling ctx stops the walk and returns ctx.Err().
func Scan(ctx context.Context, root string, opts Options, onProgress func(Progress)) (*model.Snapshot, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}

	analyzer := analyze.CreateAnalyzer()
	analyzer.SetFollowSymlinks(opts.FollowSymlinks)

	stop := make(chan struct{})
	defer close(stop)

	go func() {
		<-ctx.Done()
		analyzer.Cancel()
	}()

	if onProgress != nil {
		go func() {
			ticker := time.NewTicker(100 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-stop:
					return
				case <-ticker.C:
					p := analyzer.GetProgress()
					onProgress(Progress{
						ItemCount:   p.ItemCount,
						TotalUsage:  p.TotalUsage,
						CurrentItem: p.CurrentItemName,
					})
				}
			}
		}()
	}

	started := time.Now()
	// A nil file filter keeps every file; the ignore callback handles dirs.
	tree := analyzer.AnalyzeDir(absRoot, ignoreFunc(absRoot, opts), nil)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	tree.UpdateStats(make(gdufs.HardLinkedItems))
	elapsed := time.Since(started)

	dirs, unreadable := collect(tree, opts.minDirSize())
	snap := &model.Snapshot{
		Root:       absRoot,
		StartedAt:  started,
		Duration:   elapsed,
		TotalUsage: tree.GetUsage(),
		ItemCount:  tree.GetItemCount(),
		Dirs:       dirs,
		Unreadable: unreadable,
	}
	return snap, nil
}

// errorFlag is the marker gdu puts on an item it could not read.
const errorFlag = '!'

// collect flattens the tree into rows, dropping directories below threshold.
//
// Cumulative usage never increases going down the tree, so once a directory
// falls below the threshold its whole subtree does too. That makes the recorded
// set closed upwards, and lets a directory's recorded SelfUsage absorb every
// dropped descendant simply by subtracting only the children that were kept.
// The recorded SelfUsage values therefore still sum to the root's total.
func collect(root gdufs.Item, minSize int64) (dirs []model.DirStat, unreadable int64) {
	var out []model.DirStat

	var visit func(item gdufs.Item)
	visit = func(item gdufs.Item) {
		if !item.IsDir() {
			return
		}

		var keptChildren int64
		var kept []gdufs.Item
		for child := range item.GetFiles(gdufs.SortBySize, gdufs.SortDesc) {
			if child.GetFlag() == errorFlag {
				unreadable++
			}
			if child.IsDir() && child.GetUsage() >= minSize {
				keptChildren += child.GetUsage()
				kept = append(kept, child)
			}
		}

		out = append(out, model.DirStat{
			Path:      item.GetPath(),
			Usage:     item.GetUsage(),
			SelfUsage: item.GetUsage() - keptChildren,
			ItemCount: item.GetItemCount(),
		})

		for _, child := range kept {
			visit(child)
		}
	}

	visit(root)
	return out, unreadable
}

func ignoreFunc(root string, opts Options) func(name, path string) bool {
	ignorePaths := make(map[string]struct{}, len(opts.IgnorePaths))
	for _, p := range opts.IgnorePaths {
		if abs, err := filepath.Abs(ExpandHome(p)); err == nil {
			ignorePaths[abs] = struct{}{}
		}
	}
	ignoreNames := make(map[string]struct{}, len(opts.IgnoreNames))
	for _, n := range opts.IgnoreNames {
		ignoreNames[n] = struct{}{}
	}

	return func(name, path string) bool {
		if _, ok := ignoreNames[name]; ok {
			return true
		}
		_, ok := ignorePaths[path]
		return ok
	}
}

// ExpandHome resolves a leading ~ to the current user's home directory.
func ExpandHome(p string) string {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	if p == "~" {
		return home
	}
	return filepath.Join(home, p[2:])
}
