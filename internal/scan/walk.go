package scan

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
)

// entry is one item of a directory as a reader reports it. name aliases the
// reader's buffer and is only valid during the callback.
type entry struct {
	name    []byte
	dir     bool
	symlink bool
	failed  bool // its attributes could not be read
	usage   int64
	nlink   uint64
	ino     uint64
	dev     uint64
}

// node is one directory of the walked tree. Only directories are kept: the
// snapshot never needs an individual file, and not allocating an object for
// each of a few million files is a large part of what keeps the walk fast.
type node struct {
	path     string
	self     int64 // allocated bytes of the files held directly here
	items    int64 // entries held directly here, plus one for the directory
	children []*node

	usage, count int64 // cumulative, filled in by total
}

type inodeKey struct{ dev, ino uint64 }

type claim struct {
	n     *node
	usage int64
}

type walker struct {
	ctx    context.Context
	ignore func(name, path string) bool
	follow bool
	sem    chan struct{}
	wg     sync.WaitGroup
	bufs   sync.Pool

	unreadable atomic.Int64
	items      atomic.Int64
	usage      atomic.Int64
	current    atomic.Pointer[string]

	// A hard-linked file is charged once, to the directory with the smallest
	// path among those holding a link to it. Settling it by path rather than by
	// whichever goroutine got there first keeps the charge in the same place
	// from one scan to the next, so it never shows up as a spurious move.
	linksMu sync.Mutex
	links   map[inodeKey]claim
}

func newWalker(ctx context.Context, ignore func(name, path string) bool, follow bool) *walker {
	w := &walker{
		ctx:    ctx,
		ignore: ignore,
		follow: follow,
		// Directory reads mostly wait in the kernel, so more of them in flight
		// than there are CPUs keeps it busy. It also matters on macOS, where
		// opening a directory inside another app's container now and then
		// blocks for several seconds on a data protection check: with plenty of
		// slots the rest of the tree is read while that one waits.
		sem:   make(chan struct{}, 4*runtime.GOMAXPROCS(0)),
		links: map[inodeKey]claim{},
	}
	w.bufs.New = func() any { return new([]byte) }
	return w
}

// walk reads the tree under root and returns it with hard links settled and
// cumulative totals filled in.
func (w *walker) walk(root string) (*node, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, &os.PathError{Op: "scan", Path: root, Err: syscall.ENOTDIR}
	}

	top := &node{path: root}
	if err := w.readNode(top); err != nil {
		return nil, err
	}
	w.descend(top)
	w.wg.Wait()
	if err := w.ctx.Err(); err != nil {
		return nil, err
	}

	for _, c := range w.links {
		c.n.self += c.usage
	}
	total(top)
	return top, nil
}

// descend schedules n's children. A child gets its own goroutine while there
// is room under the concurrency limit and is walked inline otherwise, so the
// tree is read in parallel without a goroutine per directory.
func (w *walker) descend(n *node) {
	for _, c := range n.children {
		if w.ctx.Err() != nil {
			return
		}
		select {
		case w.sem <- struct{}{}:
			w.wg.Add(1)
			go func() {
				defer w.wg.Done()
				w.visit(c)
				<-w.sem
			}()
		default:
			w.visit(c)
		}
	}
}

func (w *walker) visit(n *node) {
	if err := w.readNode(n); err != nil {
		w.unreadable.Add(1)
		return
	}
	w.descend(n)
}

func (w *walker) readNode(n *node) error {
	n.items = 1
	buf := w.bufs.Get().(*[]byte)
	defer w.bufs.Put(buf)

	err := readDir(n.path, buf, func(e *entry) {
		if e.failed {
			w.unreadable.Add(1)
			return
		}
		if e.dir {
			name := string(e.name)
			path := filepath.Join(n.path, name)
			if w.ignore(name, path) {
				return
			}
			n.items++
			n.children = append(n.children, &node{path: path})
			return
		}
		n.items++
		usage := e.usage
		if e.symlink && w.follow {
			usage = w.followed(filepath.Join(n.path, string(e.name)), usage)
		}
		if e.nlink > 1 {
			w.claimLink(inodeKey{e.dev, e.ino}, n, usage)
		} else {
			n.self += usage
		}
		w.usage.Add(usage)
	})
	w.items.Add(n.items - 1)
	w.current.Store(&n.path)
	return err
}

// followed returns what a symlink points at when that is a file. A link to a
// directory keeps the link's own size: walking into it could double count the
// target or loop.
func (w *walker) followed(path string, own int64) int64 {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil || st.Mode&syscall.S_IFMT == syscall.S_IFDIR {
		return own
	}
	return int64(st.Blocks) * 512
}

func (w *walker) claimLink(k inodeKey, n *node, usage int64) {
	w.linksMu.Lock()
	defer w.linksMu.Unlock()
	if c, ok := w.links[k]; ok && c.n.path <= n.path {
		return
	}
	w.links[k] = claim{n, usage}
}

func total(n *node) {
	n.usage, n.count = n.self, n.items-int64(len(n.children))
	for _, c := range n.children {
		total(c)
		n.usage += c.usage
		n.count += c.count
	}
	// Children are appended in whatever order the filesystem lists them;
	// sorting makes the snapshot's row order independent of that.
	slices.SortFunc(n.children, func(a, b *node) int { return strings.Compare(a.path, b.path) })
}
