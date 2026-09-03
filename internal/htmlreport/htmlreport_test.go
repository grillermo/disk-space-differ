package htmlreport

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/grillermo/disk-space-differ/internal/model"
	"github.com/grillermo/disk-space-differ/internal/store"
)

func newStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "snap.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// save records a snapshot whose directory sizes are derived from literal
// self-byte counts, the way a real scan would.
func save(t *testing.T, s *store.Store, root string, at int64, selfBytes map[string]int64) {
	t.Helper()

	var dirs []model.DirStat
	var total int64
	for path, own := range selfBytes {
		var usage int64
		for other, otherOwn := range selfBytes {
			if other == path || strings.HasPrefix(other, path+"/") {
				usage += otherOwn
			}
		}
		dirs = append(dirs, model.DirStat{Path: path, Usage: usage, SelfUsage: own})
		total += own
	}

	if _, err := s.Save(&model.Snapshot{
		Root: root, StartedAt: time.Unix(at, 0), Duration: time.Second,
		TotalUsage: total, ItemCount: int64(len(dirs)), Dirs: dirs,
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
}

// widestRange returns the broadest range offered. Ranges are emitted narrowest
// first and deduplicated, so with only two runs "all runs" collapses into
// "since last run" and the widest is the one to assert against.
func widestRange(rd RootData) Range {
	return rd.Ranges[len(rd.Ranges)-1]
}

// The trend chart must never plot a directory alongside its own ancestor: both
// lines would draw the same bytes, which is the double counting the whole tool
// exists to avoid.
func TestTrendSeriesNeverPlotsNestedDirectories(t *testing.T) {
	s := newStore(t)
	save(t, s, "/r", 100, map[string]int64{
		"/r": 1000, "/r/a": 0, "/r/a/deep": 1000, "/r/b": 500,
	})
	save(t, s, "/r", 200, map[string]int64{
		"/r": 1000, "/r/a": 0, "/r/a/deep": 9000, "/r/b": 3000,
	})

	data, err := Build(s, []string{"/r"}, 30, "test")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	rng := widestRange(data.Roots[0])

	var paths []string
	for _, ser := range rng.Series {
		paths = append(paths, ser.Path)
	}
	if len(paths) == 0 {
		t.Fatal("expected at least one series")
	}
	for i, a := range paths {
		for j, b := range paths {
			if i != j && strings.HasPrefix(b, a+"/") {
				t.Errorf("series %q is an ancestor of %q; both draw the same bytes", a, b)
			}
		}
	}
	if paths[0] != "/r/a/deep" {
		t.Errorf("top series = %q, want /r/a/deep (the biggest exclusive grower)", paths[0])
	}
}

func TestSeriesRankedByExclusiveGrowthNotCumulative(t *testing.T) {
	s := newStore(t)
	// /r/small grows 8000 of its own bytes; /r/big holds a lot but grows little.
	save(t, s, "/r", 100, map[string]int64{"/r": 0, "/r/big": 100000, "/r/small": 10})
	save(t, s, "/r", 200, map[string]int64{"/r": 0, "/r/big": 100100, "/r/small": 8010})

	data, err := Build(s, []string{"/r"}, 30, "test")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	rng := widestRange(data.Roots[0])

	if len(rng.Series) == 0 || rng.Series[0].Path != "/r/small" {
		t.Errorf("top series should be the biggest grower /r/small, got %+v", rng.Series)
	}
}

func TestRangesCoverWhatIsAvailable(t *testing.T) {
	s := newStore(t)
	for i := 0; i < 3; i++ {
		save(t, s, "/r", int64(100+i), map[string]int64{"/r": int64(1000 * (i + 1))})
	}

	data, err := Build(s, []string{"/r"}, 30, "test")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	var keys []string
	for _, r := range data.Roots[0].Ranges {
		keys = append(keys, r.Key)
	}
	// Only three runs exist, so "last 7" and "last 30" must not be offered.
	want := []string{"last", "all"}
	if strings.Join(keys, ",") != strings.Join(want, ",") {
		t.Errorf("ranges = %v, want %v", keys, want)
	}
}

func TestGrewAndFreedAreSeparated(t *testing.T) {
	s := newStore(t)
	save(t, s, "/r", 100, map[string]int64{"/r": 0, "/r/up": 100, "/r/down": 900})
	save(t, s, "/r", 200, map[string]int64{"/r": 0, "/r/up": 600, "/r/down": 400})

	data, err := Build(s, []string{"/r"}, 30, "test")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	rng := widestRange(data.Roots[0])

	if rng.Grew != 500 {
		t.Errorf("grew = %d, want 500", rng.Grew)
	}
	if rng.Freed != 500 {
		t.Errorf("freed = %d, want 500", rng.Freed)
	}
	if rng.Delta != 0 {
		t.Errorf("net delta = %d, want 0", rng.Delta)
	}
}

func TestSingleSnapshotIsMarkedBaseline(t *testing.T) {
	s := newStore(t)
	save(t, s, "/r", 100, map[string]int64{"/r": 1000})

	data, err := Build(s, []string{"/r"}, 30, "test")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !data.Roots[0].Baseline {
		t.Error("a single run should be marked as a baseline")
	}
}

func TestBuildFailsWhenNothingWasEverScanned(t *testing.T) {
	s := newStore(t)
	if _, err := Build(s, []string{"/nope"}, 30, "test"); err == nil {
		t.Error("expected an error when no snapshots exist")
	}
}

func TestRenderProducesSelfContainedPage(t *testing.T) {
	s := newStore(t)
	save(t, s, "/r", 100, map[string]int64{"/r": 0, "/r/a": 100})
	save(t, s, "/r", 200, map[string]int64{"/r": 0, "/r/a": 900})

	data, err := Build(s, []string{"/r"}, 30, "9.9.9")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	var buf bytes.Buffer
	if err := Render(&buf, data); err != nil {
		t.Fatalf("Render: %v", err)
	}
	out := buf.String()

	for _, want := range []string{"<!doctype html>", "const DATA = {", "9.9.9", "/r/a"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered page is missing %q", want)
		}
	}
	// No external requests: the page has to work offline, straight from disk.
	// The SVG namespace URI is not a fetch, so only real load points count.
	for _, bad := range []string{"<script src", "<link ", "@import", "cdn.", "fonts.googleapis"} {
		if strings.Contains(out, bad) {
			t.Errorf("page references something external (%q); it must be self-contained", bad)
		}
	}
}

// A directory name is untrusted input and must not be able to close the script
// element that carries the payload.
func TestScriptInjectionInAPathIsEscaped(t *testing.T) {
	s := newStore(t)
	evil := `/r/</script><img src=x onerror=alert(1)>`
	save(t, s, "/r", 100, map[string]int64{"/r": 0, evil: 100})
	save(t, s, "/r", 200, map[string]int64{"/r": 0, evil: 900})

	data, err := Build(s, []string{"/r"}, 30, "test")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	var buf bytes.Buffer
	if err := Render(&buf, data); err != nil {
		t.Fatalf("Render: %v", err)
	}

	if strings.Contains(buf.String(), "</script><img") {
		t.Error("a path closed the script element; the payload is not escaped")
	}
}
