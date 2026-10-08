// Package scan walks a directory tree in parallel and turns the result into
// snapshot rows.
package scan

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/grillermo/disk-space-differ/internal/model"
)

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
// Usage is allocated bytes, not apparent size, so sparse and compressed files
// count for what they actually take on disk. A hard-linked file is counted once.
//
// Cancelling ctx stops the walk and returns ctx.Err().
func Scan(ctx context.Context, root string, opts Options, onProgress func(Progress)) (*model.Snapshot, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}

	w := newWalker(ctx, ignoreFunc(opts), opts.FollowSymlinks)

	stop := make(chan struct{})
	defer close(stop)
	if onProgress != nil {
		go func() {
			ticker := time.NewTicker(100 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-stop:
					return
				case <-ticker.C:
					p := Progress{ItemCount: w.items.Load(), TotalUsage: w.usage.Load()}
					if cur := w.current.Load(); cur != nil {
						p.CurrentItem = *cur
					}
					onProgress(p)
				}
			}
		}()
	}

	started := time.Now()
	tree, err := w.walk(absRoot)
	if err != nil {
		return nil, err
	}
	elapsed := time.Since(started)

	return &model.Snapshot{
		Root:       absRoot,
		StartedAt:  started,
		Duration:   elapsed,
		TotalUsage: tree.usage,
		ItemCount:  tree.count,
		Dirs:       collect(tree, opts.minDirSize()),
		Unreadable: w.unreadable.Load(),
	}, nil
}

// collect flattens the tree into rows, dropping directories below threshold.
//
// Cumulative usage never increases going down the tree, so once a directory
// falls below the threshold its whole subtree does too. That makes the recorded
// set closed upwards, and lets a directory's recorded SelfUsage absorb every
// dropped descendant simply by subtracting only the children that were kept.
// The recorded SelfUsage values therefore still sum to the root's total.
func collect(root *node, minSize int64) []model.DirStat {
	var out []model.DirStat

	var visit func(n *node)
	visit = func(n *node) {
		var keptUsage int64
		var kept []*node
		for _, c := range n.children {
			if c.usage >= minSize {
				keptUsage += c.usage
				kept = append(kept, c)
			}
		}

		out = append(out, model.DirStat{
			Path:      n.path,
			Usage:     n.usage,
			SelfUsage: n.usage - keptUsage,
			ItemCount: n.count,
		})

		for _, c := range kept {
			visit(c)
		}
	}

	visit(root)
	return out
}

func ignoreFunc(opts Options) func(name, path string) bool {
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
