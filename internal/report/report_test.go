package report

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/grillermo/disk-space-differ/internal/model"
	"github.com/grillermo/disk-space-differ/internal/store"
)

func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "snapshots.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func record(t *testing.T, st *store.Store, root string, at time.Time, dirs ...model.DirStat) {
	t.Helper()
	var total int64
	for _, d := range dirs {
		total += d.SelfUsage
	}
	snap := &model.Snapshot{
		Root:       root,
		StartedAt:  at,
		TotalUsage: total,
		ItemCount:  int64(len(dirs)),
		Dirs:       dirs,
	}
	if _, err := st.Save(snap); err != nil {
		t.Fatalf("Save: %v", err)
	}
}

func TestAScanFreeReportComparesTheTwoStoredSnapshots(t *testing.T) {
	st := newStore(t)
	record(t, st, "/r", time.Unix(1000, 0),
		model.DirStat{Path: "/r", Usage: 300, SelfUsage: 100},
		model.DirStat{Path: "/r/a", Usage: 200, SelfUsage: 200},
	)
	record(t, st, "/r", time.Unix(2000, 0),
		model.DirStat{Path: "/r", Usage: 500, SelfUsage: 100},
		model.DirStat{Path: "/r/a", Usage: 400, SelfUsage: 400},
	)

	res, err := FromStore(st, "/r", 2)
	if err != nil {
		t.Fatalf("FromStore: %v", err)
	}
	if res.Baseline {
		t.Fatal("two stored snapshots should compare, not report a baseline")
	}
	if got := res.TotalDelta(); got != 200 {
		t.Fatalf("total delta %d, want 200", got)
	}

	rows := res.Growth(1, 10)
	if len(rows) != 1 || rows[0].Path != "/r/a" || rows[0].Delta != 200 {
		t.Fatalf("growth rows = %+v, want /r/a +200", rows)
	}
}

// A wider window is what makes slow growth visible: each run adds a little, no
// single pair of scans shows much, and the span from the oldest scan in the
// window does.
func TestAWiderWindowMeasuresFromFurtherBack(t *testing.T) {
	st := newStore(t)
	for i := range 4 {
		usage := int64(100 * (i + 1))
		record(t, st, "/r", time.Unix(int64(1000*(i+1)), 0),
			model.DirStat{Path: "/r", Usage: usage, SelfUsage: usage})
	}

	for _, tc := range []struct {
		scans     int
		wantSpan  int
		wantDelta int64
	}{
		{scans: 2, wantSpan: 2, wantDelta: 100},
		{scans: 4, wantSpan: 4, wantDelta: 300},
		// Reaching past the start of the history stops at the start of it.
		{scans: 99, wantSpan: 4, wantDelta: 300},
	} {
		res, err := FromStore(st, "/r", tc.scans)
		if err != nil {
			t.Fatalf("FromStore(%d): %v", tc.scans, err)
		}
		if res.Scans != tc.wantSpan {
			t.Errorf("FromStore(%d) spans %d scans, want %d", tc.scans, res.Scans, tc.wantSpan)
		}
		if got := res.TotalDelta(); got != tc.wantDelta {
			t.Errorf("FromStore(%d) delta %d, want %d", tc.scans, got, tc.wantDelta)
		}
	}
}

// Reading is not scanning, so the stored snapshots must survive a scan-free
// report unchanged — otherwise each run would silently become the next one's
// baseline and the same question would stop having the same answer.
func TestAScanFreeReportRecordsNothing(t *testing.T) {
	st := newStore(t)
	record(t, st, "/r", time.Unix(1000, 0), model.DirStat{Path: "/r", Usage: 100, SelfUsage: 100})
	record(t, st, "/r", time.Unix(2000, 0), model.DirStat{Path: "/r", Usage: 150, SelfUsage: 150})

	for range 2 {
		if _, err := FromStore(st, "/r", 2); err != nil {
			t.Fatalf("FromStore: %v", err)
		}
	}

	recent, err := st.Recent("/r", 10)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(recent) != 2 {
		t.Fatalf("%d snapshots stored, want the original 2", len(recent))
	}
}

func TestAScanFreeReportNeedsTwoRecordedScans(t *testing.T) {
	st := newStore(t)

	if _, err := FromStore(st, "/r", 2); !errors.Is(err, ErrNeedTwoScans) {
		t.Fatalf("with no snapshot: %v, want ErrNeedTwoScans", err)
	}

	record(t, st, "/r", time.Unix(1000, 0), model.DirStat{Path: "/r", Usage: 100, SelfUsage: 100})
	if _, err := FromStore(st, "/r", 2); !errors.Is(err, ErrNeedTwoScans) {
		t.Fatalf("with one snapshot: %v, want ErrNeedTwoScans", err)
	}

	n, err := StoredCount(st, "/r")
	if err != nil || n != 1 {
		t.Fatalf("StoredCount = %d, %v; want 1, nil", n, err)
	}
}
