package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/grillermo/disk-space-differ/internal/model"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "snapshots.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func snapshot(root string, at time.Time, dirs ...model.DirStat) *model.Snapshot {
	var total int64
	for _, d := range dirs {
		total += d.SelfUsage
	}
	return &model.Snapshot{
		Root:       root,
		StartedAt:  at,
		Duration:   150 * time.Millisecond,
		TotalUsage: total,
		ItemCount:  int64(len(dirs)),
		Dirs:       dirs,
	}
}

func TestSaveAndReloadRoundTrip(t *testing.T) {
	s := newStore(t)
	in := snapshot("/home/x", time.Unix(1000, 0),
		model.DirStat{Path: "/home/x", Usage: 300, SelfUsage: 100, ItemCount: 3},
		model.DirStat{Path: "/home/x/sub", Usage: 200, SelfUsage: 200, ItemCount: 2},
	)

	id, err := s.Save(in)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := s.Dirs(id)
	if err != nil {
		t.Fatalf("Dirs: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("loaded %d dirs, want 2", len(got))
	}

	byPath := map[string]model.DirStat{}
	for _, d := range got {
		byPath[d.Path] = d
	}
	if d := byPath["/home/x/sub"]; d.Usage != 200 || d.SelfUsage != 200 || d.ItemCount != 2 {
		t.Errorf("round trip lost data: %+v", d)
	}
}

func TestRecentReturnsNewestFirst(t *testing.T) {
	s := newStore(t)
	for i, ts := range []int64{100, 200, 300} {
		snap := snapshot("/r", time.Unix(ts, 0), model.DirStat{Path: "/r", Usage: int64(i), SelfUsage: int64(i)})
		if _, err := s.Save(snap); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}

	recent, err := s.Recent("/r", 10)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(recent) != 3 {
		t.Fatalf("got %d snapshots, want 3", len(recent))
	}
	if !recent[0].StartedAt.Equal(time.Unix(300, 0)) {
		t.Errorf("newest first violated: got %v", recent[0].StartedAt)
	}
	if recent[0].Duration != 150*time.Millisecond {
		t.Errorf("duration = %v, want 150ms", recent[0].Duration)
	}
}

func TestRecentIsScopedToRoot(t *testing.T) {
	s := newStore(t)
	mustSave(t, s, snapshot("/a", time.Unix(100, 0), model.DirStat{Path: "/a", Usage: 1, SelfUsage: 1}))
	mustSave(t, s, snapshot("/b", time.Unix(200, 0), model.DirStat{Path: "/b", Usage: 2, SelfUsage: 2}))

	recent, err := s.Recent("/a", 10)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(recent) != 1 || recent[0].Root != "/a" {
		t.Errorf("Recent leaked across roots: %+v", recent)
	}
}

func TestHistoryIsOldestFirstAndZeroFillsMissing(t *testing.T) {
	s := newStore(t)
	// "/r/late" only exists in the second and third snapshots.
	mustSave(t, s, snapshot("/r", time.Unix(100, 0),
		model.DirStat{Path: "/r", Usage: 10, SelfUsage: 10}))
	mustSave(t, s, snapshot("/r", time.Unix(200, 0),
		model.DirStat{Path: "/r", Usage: 60, SelfUsage: 10},
		model.DirStat{Path: "/r/late", Usage: 50, SelfUsage: 50}))
	mustSave(t, s, snapshot("/r", time.Unix(300, 0),
		model.DirStat{Path: "/r", Usage: 110, SelfUsage: 10},
		model.DirStat{Path: "/r/late", Usage: 100, SelfUsage: 100}))

	hist, err := s.History("/r", []string{"/r/late"}, 5)
	if err != nil {
		t.Fatalf("History: %v", err)
	}

	want := []int64{0, 50, 100}
	got := hist["/r/late"]
	if len(got) != len(want) {
		t.Fatalf("history length = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("history = %v, want %v", got, want)
		}
	}
}

func TestPruneKeepsNewestAndDropsDirRows(t *testing.T) {
	s := newStore(t)
	var oldest int64
	for i, ts := range []int64{100, 200, 300, 400} {
		id := mustSave(t, s, snapshot("/r", time.Unix(ts, 0),
			model.DirStat{Path: "/r", Usage: int64(i), SelfUsage: int64(i)}))
		if ts == 100 {
			oldest = id
		}
	}

	if err := s.Prune("/r", 2); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	recent, err := s.Recent("/r", 10)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(recent) != 2 {
		t.Fatalf("kept %d snapshots, want 2", len(recent))
	}
	if !recent[0].StartedAt.Equal(time.Unix(400, 0)) {
		t.Errorf("pruned the wrong end: newest is %v", recent[0].StartedAt)
	}

	orphans, err := s.Dirs(oldest)
	if err != nil {
		t.Fatalf("Dirs: %v", err)
	}
	if len(orphans) != 0 {
		t.Errorf("pruned snapshot left %d orphan dir rows", len(orphans))
	}
}

func TestRootsListsDistinctRoots(t *testing.T) {
	s := newStore(t)
	mustSave(t, s, snapshot("/b", time.Unix(100, 0), model.DirStat{Path: "/b"}))
	mustSave(t, s, snapshot("/a", time.Unix(200, 0), model.DirStat{Path: "/a"}))
	mustSave(t, s, snapshot("/a", time.Unix(300, 0), model.DirStat{Path: "/a"}))

	roots, err := s.Roots()
	if err != nil {
		t.Fatalf("Roots: %v", err)
	}
	if len(roots) != 2 || roots[0] != "/a" || roots[1] != "/b" {
		t.Errorf("Roots = %v, want [/a /b]", roots)
	}
}

func mustSave(t *testing.T, s *Store, snap *model.Snapshot) int64 {
	t.Helper()
	id, err := s.Save(snap)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	return id
}
