// Package model holds the types shared between scanning, storage and diffing.
package model

import "time"

// DirStat is one directory as captured by a scan.
//
// Usage is cumulative: it includes every descendant. SelfUsage counts only the
// bytes of files held directly in this directory. The distinction is what makes
// growth attributable to a single directory rather than to every ancestor of it.
type DirStat struct {
	Path      string
	Usage     int64
	SelfUsage int64
	ItemCount int64
}

// Snapshot is one complete scan of one root.
type Snapshot struct {
	ID         int64
	Root       string
	StartedAt  time.Time
	Duration   time.Duration
	TotalUsage int64
	ItemCount  int64
	Dirs       []DirStat

	// Unreadable counts entries the scan could not open, almost always due to
	// permissions. Their contents are missing from the totals, so a non-zero
	// count means the report understates real usage.
	Unreadable int64
}

// ChangeKind explains why a directory appears in a growth report.
type ChangeKind int

const (
	// Changed means the directory existed in both scans and its own files grew or shrank.
	Changed ChangeKind = iota
	// Added means the directory is new since the previous scan; its whole subtree is new.
	Added
	// Removed means the directory existed in the previous scan and is now gone.
	Removed
)

func (c ChangeKind) String() string {
	switch c {
	case Added:
		return "new"
	case Removed:
		return "gone"
	default:
		return ""
	}
}

// GrowthRow is one attributed change between two snapshots.
//
// Delta is the exclusive, non-overlapping growth charged to this directory. The
// Delta values across all rows of a report sum to the total change of the root,
// so no byte is ever counted twice.
type GrowthRow struct {
	Path string
	Kind ChangeKind

	// Delta is the exclusive growth attributed to this directory.
	Delta int64
	// SubtreeDelta is the cumulative growth of this directory including
	// descendants. Shown for context; it double counts across rows.
	SubtreeDelta int64

	// Usage is the cumulative size of the directory as of the newer snapshot.
	Usage int64
	// PrevUsage is the cumulative size as of the older snapshot.
	PrevUsage int64

	// History holds this directory's cumulative usage over recent snapshots,
	// oldest first, for trend display.
	History []int64
}
