// Package store persists scan snapshots in SQLite so runs can be compared over time.
package store

import (
	"database/sql"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure Go driver, no cgo

	"github.com/grillermo/disk-space-differ/internal/model"
)

// DefaultRetention is how many snapshots per root are kept before the oldest
// are discarded.
const DefaultRetention = 30

// Store is a handle on the snapshot database.
type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS snapshots (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	root        TEXT    NOT NULL,
	started_at  INTEGER NOT NULL,
	duration_ms INTEGER NOT NULL,
	total_usage INTEGER NOT NULL,
	item_count  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_snapshots_root_time ON snapshots(root, started_at DESC);

CREATE TABLE IF NOT EXISTS dirs (
	snapshot_id INTEGER NOT NULL REFERENCES snapshots(id) ON DELETE CASCADE,
	path        TEXT    NOT NULL,
	usage       INTEGER NOT NULL,
	self_usage  INTEGER NOT NULL,
	item_count  INTEGER NOT NULL,
	PRIMARY KEY (snapshot_id, path)
) WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS idx_dirs_path ON dirs(path);
`

// DefaultPath is where snapshots live unless overridden.
func DefaultPath() string {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "snapshots.db"
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "disk-space-differ", "snapshots.db")
}

// Open opens, creating the database and its parent directory if needed.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("creating data directory: %w", err)
	}

	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("creating schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

// Save writes a snapshot and its directory rows, returning the new snapshot ID.
func (s *Store) Save(snap *model.Snapshot) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op once committed

	res, err := tx.Exec(
		`INSERT INTO snapshots (root, started_at, duration_ms, total_usage, item_count) VALUES (?, ?, ?, ?, ?)`,
		snap.Root, snap.StartedAt.Unix(), snap.Duration.Milliseconds(), snap.TotalUsage, snap.ItemCount,
	)
	if err != nil {
		return 0, fmt.Errorf("inserting snapshot: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}

	stmt, err := tx.Prepare(`INSERT INTO dirs (snapshot_id, path, usage, self_usage, item_count) VALUES (?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	for _, d := range snap.Dirs {
		if _, err := stmt.Exec(id, d.Path, d.Usage, d.SelfUsage, d.ItemCount); err != nil {
			return 0, fmt.Errorf("inserting dir %s: %w", d.Path, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	snap.ID = id
	return id, nil
}

// Recent returns up to limit snapshots for root, newest first, without dir rows.
func (s *Store) Recent(root string, limit int) ([]model.Snapshot, error) {
	rows, err := s.db.Query(
		`SELECT id, root, started_at, duration_ms, total_usage, item_count
		   FROM snapshots WHERE root = ? ORDER BY started_at DESC, id DESC LIMIT ?`,
		root, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.Snapshot
	for rows.Next() {
		var (
			snap       model.Snapshot
			startedAt  int64
			durationMS int64
		)
		if err := rows.Scan(&snap.ID, &snap.Root, &startedAt, &durationMS, &snap.TotalUsage, &snap.ItemCount); err != nil {
			return nil, err
		}
		snap.StartedAt = time.Unix(startedAt, 0)
		snap.Duration = time.Duration(durationMS) * time.Millisecond
		out = append(out, snap)
	}
	return out, rows.Err()
}

// Count is how many snapshots are recorded for root, which bounds how far back
// a comparison can reach.
func (s *Store) Count(root string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT count(*) FROM snapshots WHERE root = ?`, root).Scan(&n)
	return n, err
}

// Dirs loads the directory rows of one snapshot.
func (s *Store) Dirs(snapshotID int64) ([]model.DirStat, error) {
	rows, err := s.db.Query(
		`SELECT path, usage, self_usage, item_count FROM dirs WHERE snapshot_id = ?`, snapshotID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.DirStat
	for rows.Next() {
		var d model.DirStat
		if err := rows.Scan(&d.Path, &d.Usage, &d.SelfUsage, &d.ItemCount); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// History returns each requested path's cumulative usage across the most recent
// snapshots of root, oldest first. Paths absent from a snapshot record 0, so a
// directory that appeared partway through reads as growth from nothing.
func (s *Store) History(root string, paths []string, limit int) (map[string][]int64, error) {
	if len(paths) == 0 {
		return map[string][]int64{}, nil
	}

	snaps, err := s.Recent(root, limit)
	if err != nil {
		return nil, err
	}
	if len(snaps) == 0 {
		return map[string][]int64{}, nil
	}

	// Recent is newest first; walk backwards to build oldest-first series.
	order := make([]int64, 0, len(snaps))
	for i := len(snaps) - 1; i >= 0; i-- {
		order = append(order, snaps[i].ID)
	}

	slot := make(map[int64]int, len(order))
	for i, id := range order {
		slot[id] = i
	}

	out := make(map[string][]int64, len(paths))
	for _, p := range paths {
		out[p] = make([]int64, len(order))
	}

	// Callers ask about every level of the tree at once, which can run to
	// thousands of paths. SQLite caps how many bind parameters one statement may
	// carry, so the paths go in batches rather than one enormous IN list.
	for batch := range chunks(paths, historyBatch) {
		if err := s.fillHistory(batch, order, slot, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// historyBatch is how many paths one history query binds at a time, well inside
// any SQLite build's parameter limit.
const historyBatch = 400

func (s *Store) fillHistory(paths []string, order []int64, slot map[int64]int, out map[string][]int64) error {
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(paths)), ",")
	snapPlaceholders := strings.TrimSuffix(strings.Repeat("?,", len(order)), ",")
	args := make([]any, 0, len(paths)+len(order))
	for _, p := range paths {
		args = append(args, p)
	}
	for _, id := range order {
		args = append(args, id)
	}

	query := fmt.Sprintf(
		`SELECT path, snapshot_id, usage FROM dirs WHERE path IN (%s) AND snapshot_id IN (%s)`,
		placeholders, snapPlaceholders,
	)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			path       string
			snapshotID int64
			usage      int64
		)
		if err := rows.Scan(&path, &snapshotID, &usage); err != nil {
			return err
		}
		if series, ok := out[path]; ok {
			series[slot[snapshotID]] = usage
		}
	}
	return rows.Err()
}

func chunks(paths []string, size int) iter.Seq[[]string] {
	return func(yield func([]string) bool) {
		for start := 0; start < len(paths); start += size {
			end := min(start+size, len(paths))
			if !yield(paths[start:end]) {
				return
			}
		}
	}
}

// Prune keeps the newest keep snapshots for root and deletes the rest.
func (s *Store) Prune(root string, keep int) error {
	if keep <= 0 {
		keep = DefaultRetention
	}
	_, err := s.db.Exec(
		`DELETE FROM dirs WHERE snapshot_id IN (
			SELECT id FROM snapshots WHERE root = ?
			ORDER BY started_at DESC, id DESC LIMIT -1 OFFSET ?
		)`, root, keep)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`DELETE FROM snapshots WHERE root = ? AND id NOT IN (
			SELECT id FROM snapshots WHERE root = ? ORDER BY started_at DESC, id DESC LIMIT ?
		)`, root, root, keep)
	return err
}

// Roots lists every root that has at least one stored snapshot.
func (s *Store) Roots() ([]string, error) {
	rows, err := s.db.Query(`SELECT DISTINCT root FROM snapshots ORDER BY root`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
