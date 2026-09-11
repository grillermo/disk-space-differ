# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

```bash
./build                    # build ./disk-space-differ (add --release to strip debug info)
go test ./...              # full suite
go test ./internal/level/  # one package
go test ./internal/tui/ -run TestLeftArrowClimbsToTheParentLevel -v   # one test
go vet ./... && gofmt -l ./internal ./main.go                          # must both be clean
```

Exercising the tool end to end needs a throwaway tree and database, because
every run records a snapshot and thereby becomes the next run's baseline. To
compare the *same* change at different flag values, restore the database between
runs rather than scanning repeatedly:

```bash
dsd -plain -db /tmp/t/s.db -root /tmp/tree     # run 1 records the baseline
cp -r /tmp/t /tmp/t.bak                        # ...then make changes on disk
rm -rf /tmp/t && cp -r /tmp/t.bak /tmp/t       # restore before each comparison run
dsd -plain -level 3 -db /tmp/t/s.db -root /tmp/tree
```

Go 1.26. Only stdlib plus bubbletea/lipgloss, gdu (scanner, used as a library)
and modernc.org/sqlite (pure Go, no cgo).

## Architecture

One pipeline, one root at a time:

```
scan ──> store (SQLite) ──> report.Run ──> diff.Compute ──> level.Index
                                                                │
                                          tui (interactive) ────┴──── main.go (-plain/-json)
```

`htmlreport` is a second, independent renderer: it reads snapshots straight out
of `store` and never touches `report.Result`. Changes to `report` do not reach
the HTML report, and vice versa.

### The invariant everything is built on

Directory sizes are cumulative, so comparing them naively charges the same
growth to every ancestor and the top list degenerates into one path repeated.
Instead **every byte is charged to exactly one directory**, and the attributed
deltas sum exactly to the root's total change.

`diff.Compute` enforces this at level 1 (a directory in both scans is charged
only its `SelfUsage` change; a wholly new subtree collapses onto its topmost new
directory). `level.Index` preserves it when folding upwards. Both packages have
a conservation test — `TestDeltasSumToTotalRootChange`,
`TestAggregatedDeltasSumToTheSameTotalAtEveryLevel`. Any change to attribution
must keep those green; they are the reason the ranking can be trusted.

A corollary, also load-bearing: **ranking is by change, never by size.** A
directory whose size did not move produces no row at all. The `largest` tab view
and the inspect screen are the two deliberate exceptions.

`level.Index.Contents` (reached through `report.Result.Contents`, rendered by the
TUI's inspect mode) is the one API that returns **cumulative, overlapping**
sizes: one entry per recorded subdirectory plus one for the directory's own
`SelfUsage`, summing to the directory's own usage. It answers "what is in here",
not "what changed", and its numbers must never be summed across nesting levels or
mixed into a ranking.

### Directories below the threshold are not recorded

`scan.collect` drops directories under `MinDirSize` (1 MiB), folding their bytes
into the nearest recorded ancestor's `SelfUsage`. Cumulative usage never grows
going down a tree, so the recorded set is **closed upwards**: an ancestor is
always present, but the *immediate* parent may not be. Both `diff.nearestAncestor`
and `level.Index.ancestor` exist for exactly this reason — never assume
`filepath.Dir(path)` is in the map.

### Levels are heights, not depths

`internal/level` counts from the leaves up: a directory holding no other recorded
directory is level 1, one holding a level 1 directory is level 2, and so on to
the scan root. Depth from the root would not work — branches of different lengths
would not be comparable.

Height gives the property the folding depends on: a parent's level is always
*strictly* above its children's, so no two directories at one level can contain
one another, and folding rows onto their level-N representatives is a partition
rather than a double count. `Fold(path, n)` climbs to the closest
ancestor-or-self at or above level n.

`computeHeights` sorts by path length descending instead of recursing: an
ancestor's path is always a strict prefix, hence shorter, so a directory's height
is final before its ancestor reads it.

### Two ways to build a Result

`report.Run` scans, records, and compares against the run before it.
`report.FromStore` compares snapshots that are already recorded and **writes
nothing** — it is what `-no-scan` reads and what `+`/`-` re-read. Recording there
would make every read the next read's baseline, so the same command would stop
giving the same answer.

`FromStore(st, root, scans)` takes the window in *scans*, not in time:
`recent[0]` against `recent[scans-1]`, clamped to what the history holds
(`Result.Scans` reports the width actually used). Both ends being real recorded
snapshots is what keeps attribution intact — a window is a different pair of
snapshots fed to the same `diff.Compute`, never a sum of per-run deltas, which
would double-charge directories that moved in more than one run.

### report.Result caching

`Result` builds its `level.Index` and per-level row sets lazily, on first use
rather than in `Run`, so a `Result` assembled by hand in a test still answers
level queries. `RowsAt`/`SizesAt` return the **cached, mutable** slices;
`Growth`/`Changes`/`Largest` return ranked copies.

This distinction matters: `attachHistory` annotates rows in place via `head()`,
which deliberately shares backing storage with the cache. Annotating the output
of a ranking helper instead would silently do nothing. History is attached for
every level up front, because the level being viewed is chosen after the scan and
rescanning to fill in a sparkline would be absurd.

### TUI

`internal/tui` is Bubble Tea; `tui.go` holds state and messages, `view.go` all
rendering. Two orthogonal axes: `view` (growth / all changes / largest, `tab`)
and `level` (`←` up toward the root, `→` down toward the files). Both funnel
through `rebuildRows`, which merges every root into one ranking.

`enter` opens `stateInspect`, a third mode that leaves both axes behind and reads
one directory as a tree (`inspect` is the trail of opened directories, `entries`
what the last of them holds). It owns `esc`, which everywhere else quits the
program, so `handleKey` dispatches on state *before* the global quit keys. The
table and the inspect listing keep separate `scroll` cursors so that leaving a
directory lands back on the row that opened it.

Scans run on a background goroutine and push `progressMsg` through the
`*tea.Program` handle set by `SetProgram`. A rescan drops the inspect trail: it
replaces the tree those paths were read from.

`scan`'s `init()` discards logrus output. gdu logs every unreadable path at info
level, which would otherwise be painted straight over the alternate screen —
unreadable entries are counted into `Snapshot.Unreadable` and reported instead.

## Conventions

Comments explain *why*, never what the code plainly says. Package and exported
doc comments state the invariant the code exists to uphold, and non-obvious
choices carry the reasoning that would otherwise be lost (why height and not
depth, why `head()` shares storage, why logrus is silenced). Match this density;
it is the dominant style throughout.

Test names are sentences describing the behaviour being guarded
(`TestFoldersThatNetToNoChangeDropOut`), not the function under test. Prefer a
test that pins an invariant over one that pins an implementation detail.

Deletion is permanent and does not use the trash. `unsafeToDelete` refuses `/`,
the home directory and any scan root; keep that guard intact.

## Workflow

After every successful and complete change ./build the binary
