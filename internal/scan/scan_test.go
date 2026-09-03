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
