package scan

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// writeFile creates a file of exactly size bytes.
func writeFile(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
}

func scanTree(t *testing.T, root string, opts Options) map[string]int64 {
	t.Helper()
	snap, err := Scan(context.Background(), root, opts, nil)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	usage := map[string]int64{}
	for _, d := range snap.Dirs {
		usage[d.Path] = d.Usage
	}
	return usage
}

// The recorded SelfUsage values must still add up to the root's total even
// after small directories are dropped, otherwise pruning would silently lose
// bytes and corrupt every later diff.
func TestSelfUsageSumsToRootTotalAfterPruning(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "big", "a.bin"), 4<<20)
	writeFile(t, filepath.Join(root, "big", "tiny", "b.bin"), 1024)
	writeFile(t, filepath.Join(root, "small", "c.bin"), 2048)
	writeFile(t, filepath.Join(root, "d.bin"), 3<<20)

	snap, err := Scan(context.Background(), root, Options{MinDirSize: 1 << 20}, nil)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	var summed int64
	for _, d := range snap.Dirs {
		summed += d.SelfUsage
	}
	if summed != snap.TotalUsage {
		t.Errorf("self usage sums to %d, want root total %d", summed, snap.TotalUsage)
	}
}

func TestDirectoriesBelowThresholdAreNotRecorded(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "big", "a.bin"), 4<<20)
	writeFile(t, filepath.Join(root, "small", "c.bin"), 2048)

	usage := scanTree(t, root, Options{MinDirSize: 1 << 20})

	if _, ok := usage[filepath.Join(root, "big")]; !ok {
		t.Error("directory above the threshold should be recorded")
	}
	if _, ok := usage[filepath.Join(root, "small")]; ok {
		t.Error("directory below the threshold should be folded into its parent")
	}
}

func TestLoweringThresholdRecordsSmallDirectories(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "small", "c.bin"), 2048)

	usage := scanTree(t, root, Options{MinDirSize: 1})

	if _, ok := usage[filepath.Join(root, "small")]; !ok {
		t.Error("small directory should be recorded when the threshold allows it")
	}
}

func TestIgnoreNamesSkipsMatchingDirectories(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "keep", "a.bin"), 4<<20)
	writeFile(t, filepath.Join(root, "node_modules", "b.bin"), 4<<20)

	usage := scanTree(t, root, Options{MinDirSize: 1 << 20, IgnoreNames: []string{"node_modules"}})

	if _, ok := usage[filepath.Join(root, "node_modules")]; ok {
		t.Error("ignored directory should not appear in the snapshot")
	}
	if _, ok := usage[filepath.Join(root, "keep")]; !ok {
		t.Error("non-ignored sibling should still be scanned")
	}
}

func TestIgnorePathsSkipsExactDirectory(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a", "x.bin"), 4<<20)
	writeFile(t, filepath.Join(root, "b", "x.bin"), 4<<20)
	skipped := filepath.Join(root, "b")

	usage := scanTree(t, root, Options{MinDirSize: 1 << 20, IgnorePaths: []string{skipped}})

	if _, ok := usage[skipped]; ok {
		t.Error("path in IgnorePaths should be skipped")
	}
	if _, ok := usage[filepath.Join(root, "a")]; !ok {
		t.Error("unrelated path should still be scanned")
	}
}

func TestCumulativeUsageIncludesDescendants(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "parent", "child", "a.bin"), 4<<20)

	usage := scanTree(t, root, Options{MinDirSize: 1 << 20})

	parent := usage[filepath.Join(root, "parent")]
	child := usage[filepath.Join(root, "parent", "child")]
	if parent < child || child == 0 {
		t.Errorf("parent usage %d should include child usage %d", parent, child)
	}
}

func TestCancelledContextStopsScan(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.bin"), 1024)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := Scan(ctx, root, Options{}, nil); err == nil {
		t.Error("expected an error from a cancelled scan")
	}
}

func TestExpandHomeResolvesTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	if got := ExpandHome("~"); got != home {
		t.Errorf("ExpandHome(~) = %q, want %q", got, home)
	}
	if got := ExpandHome("~/x"); got != filepath.Join(home, "x") {
		t.Errorf("ExpandHome(~/x) = %q", got)
	}
	if got := ExpandHome("/abs/path"); got != "/abs/path" {
		t.Errorf("ExpandHome should leave absolute paths alone, got %q", got)
	}
}

// A file hard-linked into two directories occupies its blocks once. Charging
// it to both would inflate the total, and charging it to whichever directory a
// parallel walk reached first would move it between scans and report growth
// that never happened.
func TestHardLinkedFileIsChargedOnceToTheSamePlaceEveryScan(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "b", "data.bin"), 4<<20)
	if err := os.MkdirAll(filepath.Join(root, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(root, "b", "data.bin"), filepath.Join(root, "a", "data.bin")); err != nil {
		t.Skipf("hard links unsupported here: %v", err)
	}

	for range 5 {
		snap, err := Scan(context.Background(), root, Options{MinDirSize: 1}, nil)
		if err != nil {
			t.Fatalf("Scan: %v", err)
		}
		self := map[string]int64{}
		for _, d := range snap.Dirs {
			self[d.Path] = d.SelfUsage
		}
		if self[filepath.Join(root, "a")] < 4<<20 || self[filepath.Join(root, "b")] != 0 {
			t.Fatalf("link should be charged to the first path only: a=%d b=%d",
				self[filepath.Join(root, "a")], self[filepath.Join(root, "b")])
		}
		if snap.TotalUsage >= 8<<20 {
			t.Fatalf("total %d counts the linked file twice", snap.TotalUsage)
		}
	}
}

// The platform reader decodes packed kernel records by hand; reading the same
// directory through plain readdir and lstat must give the same answer, or the
// offsets are wrong.
func TestPlatformReaderAgreesWithPortableReader(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "one.bin"), 3<<20)
	writeFile(t, filepath.Join(root, "two.bin"), 12345)
	writeFile(t, filepath.Join(root, "a-long-file-name-to-move-the-name-offset.bin"), 1)
	writeFile(t, filepath.Join(root, "sub", "x.bin"), 1)
	if err := os.Symlink("one.bin", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}

	type got struct {
		dir, symlink bool
		usage        int64
	}
	read := func(reader func(fn func(e *entry)) error) map[string]got {
		out := map[string]got{}
		if err := reader(func(e *entry) {
			out[string(e.name)] = got{e.dir, e.symlink, e.usage}
		}); err != nil {
			t.Fatal(err)
		}
		return out
	}
	var buf []byte
	platform := read(func(fn func(e *entry)) error { return readDir(root, &buf, fn) })
	portable := read(func(fn func(e *entry)) error { return readDirPortable(root, fn) })

	if len(platform) != len(portable) {
		t.Fatalf("platform reader saw %v, portable saw %v", platform, portable)
	}
	for name, want := range portable {
		if platform[name] != want {
			t.Errorf("%s: platform reader %+v, portable %+v", name, platform[name], want)
		}
	}
}

func TestUnreadableDirectoryIsCountedNotFatal(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads everything")
	}
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "locked", "a.bin"), 1024)
	locked := filepath.Join(root, "locked")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o755) })

	snap, err := Scan(context.Background(), root, Options{}, nil)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if snap.Unreadable != 1 {
		t.Errorf("Unreadable = %d, want 1", snap.Unreadable)
	}
}

// Recording an empty snapshot for a root that is not there would make the next
// real scan report every byte in it as new.
func TestMissingRootIsAnError(t *testing.T) {
	if _, err := Scan(context.Background(), filepath.Join(t.TempDir(), "nope"), Options{}, nil); err == nil {
		t.Error("expected an error scanning a missing root")
	}
}
