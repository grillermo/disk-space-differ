package scan

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// cloneFile makes dst an APFS clone of src, sharing all of its blocks, the way
// pnpm installs a package out of its store.
func cloneFile(t *testing.T, src, dst string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("cp", "-c", src, dst).CombinedOutput(); err != nil {
		t.Skipf("clones unsupported here: %v: %s", err, out)
	}
}

func selfUsage(t *testing.T, root string) (map[string]int64, int64) {
	t.Helper()
	snap, err := Scan(context.Background(), root, Options{MinDirSize: 1}, nil)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	self := map[string]int64{}
	for _, d := range snap.Dirs {
		self[d.Path] = d.SelfUsage
	}
	return self, snap.TotalUsage
}

// A full clone has its own inode and a single link, so nothing but its clone
// ID says its blocks are already counted. Charging both copies would report a
// pnpm project's node_modules at the size of its store a second time.
func TestClonedFileIsChargedOnceToTheSamePlaceEveryScan(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "store", "data.bin"), 4<<20)
	cloneFile(t, filepath.Join(root, "store", "data.bin"), filepath.Join(root, "app", "data.bin"))

	for range 5 {
		self, total := selfUsage(t, root)
		if self[filepath.Join(root, "app")] < 4<<20 || self[filepath.Join(root, "store")] != 0 {
			t.Fatalf("clone should be charged to the first path only: app=%d store=%d",
				self[filepath.Join(root, "app")], self[filepath.Join(root, "store")])
		}
		if total >= 8<<20 {
			t.Fatalf("total %d counts the cloned file twice", total)
		}
	}
}

// Once either copy is written to, the two share only some blocks and the clone
// ID no longer ties them together. Both must then be charged in full: dropping
// one would lose bytes that are really on disk.
func TestClonesThatHaveDivergedAreChargedSeparately(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "store", "data.bin"), 4<<20)
	clone := filepath.Join(root, "app", "data.bin")
	cloneFile(t, filepath.Join(root, "store", "data.bin"), clone)

	f, err := os.OpenFile(clone, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte("diverged"), 1<<20); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	self, _ := selfUsage(t, root)
	if self[filepath.Join(root, "app")] < 4<<20 || self[filepath.Join(root, "store")] < 4<<20 {
		t.Fatalf("diverged clones should both be charged: app=%d store=%d",
			self[filepath.Join(root, "app")], self[filepath.Join(root, "store")])
	}
}

// A file hard-linked into two directories and also cloned into a third is one
// set of blocks reached by three names, and must settle on a single claim.
func TestHardLinksAndClonesOfOneFileShareOneCharge(t *testing.T) {
	root := t.TempDir()
	orig := filepath.Join(root, "c", "data.bin")
	writeFile(t, orig, 4<<20)
	if err := os.MkdirAll(filepath.Join(root, "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(orig, filepath.Join(root, "b", "data.bin")); err != nil {
		t.Skipf("hard links unsupported here: %v", err)
	}
	cloneFile(t, orig, filepath.Join(root, "a", "data.bin"))

	self, total := selfUsage(t, root)
	if total >= 8<<20 {
		t.Fatalf("total %d counts shared data more than once (a=%d b=%d c=%d)", total,
			self[filepath.Join(root, "a")], self[filepath.Join(root, "b")], self[filepath.Join(root, "c")])
	}
}
