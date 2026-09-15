package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/grillermo/disk-space-differ/internal/model"
	"github.com/grillermo/disk-space-differ/internal/report"
)

// inspecting opens the scan root of layeredResult, which is the folder with
// something to browse into.
func inspecting(t *testing.T) *Model {
	t.Helper()

	m := testModel(t, []*report.Result{layeredResult()})
	// Inspect opens whatever row is selected. The scan root is never a row of its
	// own — the table starts at what the roots hold — so it is selected directly
	// here, being the folder in this tree with the most to browse through.
	m.rows = []model.GrowthRow{{Path: "/tmp/sandbox"}}

	press(m, tea.KeyEnter)
	if m.state != stateInspect {
		t.Fatal("enter did not open inspect mode")
	}
	return m
}

func TestEnterInspectsTheSelectedFolderAndEscLeaves(t *testing.T) {
	m := inspecting(t)
	t.Log("\n" + m.View())

	press(m, tea.KeyEsc)
	if m.state != stateTable {
		t.Error("esc should leave inspect mode, not quit or stay")
	}
	if len(m.inspect) != 0 {
		t.Errorf("inspect trail survived esc: %v", m.inspect)
	}
}

// Inspect answers what a folder is made of: every subfolder it holds, plus its
// own files as one group rather than a listing of every file.
func TestInspectShowsSubfoldersAndTheFilesHeldDirectly(t *testing.T) {
	m := inspecting(t)
	out := m.View()

	for _, want := range []string{"grower/", "steady/", "files here", "34.0 MiB", "26.0 MiB"} {
		if !strings.Contains(out, want) {
			t.Errorf("inspect view is missing %q", want)
		}
	}

	// The listing accounts for the whole folder, so it can be read as shares of
	// it. /tmp/sandbox holds 60 MiB: 34 in grower, 26 in steady, 1 KiB of files.
	var total int64
	for _, e := range m.entries {
		total += e.Usage
	}
	if total != 62914560 {
		t.Errorf("entries total %d, want the folder's own 62914560", total)
	}
	if len(m.entries) != 3 {
		t.Errorf("listed %d entries, want two subfolders and one file group", len(m.entries))
	}
}

// The file group stands for bytes, not for a folder, so opening it is refused
// rather than silently doing nothing.
func TestTheFileGroupCannotBeOpened(t *testing.T) {
	m := inspecting(t)

	m.browse.cursor = len(m.entries) - 1
	if !m.entries[m.browse.cursor].Files {
		t.Fatal("the smallest entry should be the file group")
	}

	press(m, tea.KeyEnter)
	if m.inspectPath() != "/tmp/sandbox" {
		t.Errorf("opening the file group moved to %s, want to stay put", m.inspectPath())
	}
	if m.status == "" {
		t.Error("opening the file group should say why nothing happened")
	}
}

func TestBrowsingIntoAFolderAndBackLandsOnTheFolderYouOpened(t *testing.T) {
	m := inspecting(t)

	press(m, tea.KeyDown)
	opened := m.entries[m.browse.cursor].Path
	if opened != "/tmp/sandbox/steady" {
		t.Fatalf("second entry = %s, want the smaller subfolder", opened)
	}

	press(m, tea.KeyEnter)
	if m.inspectPath() != opened {
		t.Fatalf("inspecting %s after opening %s", m.inspectPath(), opened)
	}
	if len(m.entries) != 1 || m.entries[0].Path != "/tmp/sandbox/steady/leaf" {
		t.Fatalf("contents of steady = %v, want its one subfolder", m.entries)
	}

	press(m, tea.KeyLeft)
	if m.inspectPath() != "/tmp/sandbox" {
		t.Fatalf("← went to %s, want back to the folder above", m.inspectPath())
	}
	if got := m.entries[m.browse.cursor].Path; got != opened {
		t.Errorf("came back onto %s, want the %s row that was opened", got, opened)
	}
}

// Going back out of the folder inspect started at has nowhere left to return to,
// so it leaves inspect mode rather than climbing into folders never opened.
func TestGoingBackPastTheStartingFolderLeavesInspectMode(t *testing.T) {
	m := inspecting(t)

	press(m, tea.KeyLeft)
	if m.state != stateTable {
		t.Errorf("state = %v after ← at the starting folder, want the table", m.state)
	}
}

func TestNarrowTerminalStillRendersInspect(t *testing.T) {
	m := inspecting(t)
	m.width, m.height = 40, 12

	if out := m.View(); out == "" {
		t.Error("inspect view collapsed to nothing on a narrow terminal")
	}
}
