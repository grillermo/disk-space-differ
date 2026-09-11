package tui

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/grillermo/disk-space-differ/internal/config"
	"github.com/grillermo/disk-space-differ/internal/model"
	"github.com/grillermo/disk-space-differ/internal/report"
	"github.com/grillermo/disk-space-differ/internal/store"
)

// noScanModel opens on the stored snapshots of a database holding `snapshots`
// scans of one root.
func noScanModel(t *testing.T, snapshots int) *Model {
	t.Helper()

	st, err := store.Open(filepath.Join(t.TempDir(), "snapshots.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	for i := range snapshots {
		usage := int64(100 * (i + 1))
		_, err := st.Save(&model.Snapshot{
			Root:       "/r",
			StartedAt:  time.Unix(int64(1000*(i+1)), 0),
			TotalUsage: usage,
			Dirs:       []model.DirStat{{Path: "/r", Usage: usage, SelfUsage: usage}},
		})
		if err != nil {
			t.Fatalf("Save: %v", err)
		}
	}

	cfg := config.Default()
	cfg.Top = 20
	m := New(cfg, st, true, report.MinScans)
	m.roots = []string{"/r"}
	m.width, m.height = 110, 26
	return m
}

// update runs the model's own Init command, the way Bubble Tea would.
func (m *Model) runInit(t *testing.T) {
	t.Helper()
	cmd := m.Init()
	if cmd == nil {
		t.Fatal("Init returned no command")
	}
	m.Update(cmd())
}

// press sends a key and applies whatever it produced, the way the runtime would.
func (m *Model) press(t *testing.T, key string) {
	t.Helper()
	cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
	if cmd != nil {
		m.Update(cmd())
	}
}

func TestPlusAndMinusWidenAndNarrowTheComparisonWindow(t *testing.T) {
	m := noScanModel(t, 4)
	m.runInit(t)

	if got := m.displayedScans(); got != 2 {
		t.Fatalf("opened on %d scans, want the default 2", got)
	}

	m.press(t, "+")
	m.press(t, "+")
	if got := m.displayedScans(); got != 4 {
		t.Fatalf("after two widenings: %d scans, want 4", got)
	}
	// The window is a window on the same newest scan, so widening it measures
	// from further back rather than reporting on a different tree.
	if delta := m.results[0].TotalDelta(); delta != 300 {
		t.Fatalf("delta across 4 scans = %d, want 300", delta)
	}

	m.press(t, "-")
	if got := m.displayedScans(); got != 3 {
		t.Fatalf("after narrowing: %d scans, want 3", got)
	}
}

func TestTheWindowStopsAtTwoScansAndAtTheEndOfTheHistory(t *testing.T) {
	m := noScanModel(t, 3)
	m.runInit(t)

	m.press(t, "-")
	if got := m.displayedScans(); got != 2 {
		t.Fatalf("narrowed below the minimum to %d scans", got)
	}
	if !strings.Contains(m.status, "already comparing the last two") {
		t.Fatalf("status = %q, want the minimum explained", m.status)
	}

	for range 5 {
		m.press(t, "+")
	}
	if got := m.displayedScans(); got != 3 {
		t.Fatalf("widened past the history to %d scans, want 3", got)
	}
	if !strings.Contains(m.status, "only 3 scans recorded") {
		t.Fatalf("status = %q, want the limit explained", m.status)
	}
}

func TestOpeningWithoutScanningShowsTheStoredComparison(t *testing.T) {
	m := noScanModel(t, 2)
	m.runInit(t)

	if m.state != stateTable {
		t.Fatalf("state = %v, want the table", m.state)
	}
	if len(m.results) != 1 || m.results[0].Previous == nil {
		t.Fatalf("results = %+v, want one comparison", m.results)
	}
}

func TestOpeningWithoutScanningAsksWhenThereIsNothingToCompare(t *testing.T) {
	m := noScanModel(t, 1)
	m.runInit(t)

	if m.state != stateNeedScan {
		t.Fatalf("state = %v, want the scan prompt", m.state)
	}
	if view := m.View(); !strings.Contains(view, "1 of 2 scans recorded") {
		t.Fatalf("prompt does not say what is missing:\n%s", view)
	}
}

func TestTheScanPromptScansOnSAndQuitsOnQ(t *testing.T) {
	m := noScanModel(t, 1)
	m.runInit(t)

	if cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")}); cmd == nil {
		t.Fatal("s returned no command, want a scan")
	}
	if m.state != stateScanning {
		t.Fatalf("state = %v, want scanning", m.state)
	}

	m = noScanModel(t, 1)
	m.runInit(t)
	cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if cmd == nil {
		t.Fatal("q returned no command, want quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("q did not quit")
	}
}
