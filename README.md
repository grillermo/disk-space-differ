# disk-space-differ

Finds the directories that keep growing, and lets you delete them.

Each run scans your configured roots, records a snapshot, and reports the top
directories by **growth since the previous run** — not by size. A 200 GB
directory that never changes is not your problem; a 2 GB cache that regrows
every week is.

```
 disk-space-differ                                       compared with 3d ago
      +4.21 GiB  ~                    77.0 GiB total · 2,388,362 items · 28.9s

 #           GROWTH      TREND              TOTAL PATH · level 1/9 · leaf folders
 ─────────────────────────────────────────────────────────────────────────────
 1        +2.31 GiB      ▁▁▂▃▅█          4.10 GiB ~/Library/Caches/Homebrew
 2        +1.02 GiB new  ▁▁▁▁▁█          1.02 GiB ~/c/app/node_modules
 3         +512 MiB      ▁▂▃▄▅█           980 MiB ~/Library/Caches/go-build

 1/40 · growth · level 1/9 · ↑↓ move · ←→ level · tab view · d delete · q quit
```

## Install

```bash
go build -o disk-space-differ .
```

Requires Go 1.24+. The scanner is [gdu](https://github.com/dundee/gdu)'s parallel
analyzer, used as a library, so the result is a single static binary with no
external tools to install.

## Usage

```bash
disk-space-differ                 # scan configured roots, open the TUI
disk-space-differ ~/Downloads     # scan a specific directory instead
disk-space-differ --no-scan       # open the stored scans, instantly
disk-space-differ --scans 10      # compare across the last ten scans
disk-space-differ --plain         # print and exit (for cron)
disk-space-differ --json          # machine-readable output
disk-space-differ --report         # charted HTML report, opened in the browser
disk-space-differ --init-config   # write a default config file
```

| Key | Action |
|---|---|
| `↑` `↓` / `j` `k` | move |
| `←` / `h` | up a tree level (coarser) |
| `→` / `l` | down a tree level (finer) |
| `enter` | inspect the selected directory (see below) |
| `+` / `-` | widen / narrow the comparison window (how many scans back) |
| `tab` | cycle view: growth → all changes → largest → by path |
| `d` | delete the selected directory (asks first) |
| `o` | reveal in the file manager |
| `r` | rescan |
| `q` | quit |

The first run has nothing to compare against, so it records a baseline and lists
the directories holding the most space. Growth appears from the second run on.

### How far back to compare

By default a report compares the last two scans. `+` widens that window one scan
at a time and `-` narrows it back down, with two — something to measure, and
something to measure it against — as the floor. The window reads out of the
database, so it changes instantly and never rescans; `--scans N` sets it up
front, for `--plain` and `--json` too.

Widening is how slow growth becomes visible. A cache that gains 40 MB a day
looks like noise between two runs and like 1.2 GB across thirty of them. The
newest scan is always the near end of the window, so a wider one measures from
further back rather than showing a different tree — the table stays where it is,
and only the numbers reach deeper. `+` past the oldest stored scan stops there
and says so.

### `--no-scan`

`--no-scan` skips scanning entirely and reports the scans already in the
database, so it opens instantly and works with `--plain`, `--json` and
`--report` too. It records nothing, so the same command keeps giving the same
answer instead of each run becoming the next one's baseline — and the numbers
are the disk as of the last scan, not as of now. Press `r` in the TUI to rescan.

Comparing needs two recorded scans. With fewer, the TUI says so and offers to
scan now or quit; `--plain` and `--json` exit with that message.

## Levels

`←` and `→` change how coarsely the same scan is read, without rescanning: `←`
moves up the tree towards the root, `→` back down towards the files.

Levels are counted **from the leaves up**, not from the root down:

- **Level 1** — folders holding no other folder. Where the bytes actually sit.
- **Level 2** — folders holding one or more level 1 folders.
- **Level 3** — folders holding one or more level 2 folders, and so on up to the
  scan root, which is the highest level there is.

Counting from the leaves is what makes the levels comparable across branches of
different lengths. `~/Library` is nine folders deep and `~/Downloads` is one, so
"three folders down from the root" means nothing in common between them, while
"three levels up from the files" always means the same kind of thing.

Level 1 tells you *what* grew; the levels above tell you *whose fault it is*. Two
hundred packages each adding 4 MB is invisible at level 1 and a single 800 MB row
at level 3.

Moving up a level never changes the total. Growth is re-charged to the folder
representing each row at that level, so the numbers on screen always still sum to
the root's change — the same conservation invariant that governs level 1. A
folder whose subtree churned but whose size did not move drops off the list at
every level, which is the whole point of ranking by change rather than size.

`--level N` does the same thing for `--plain` and `--json`.

## Inspecting a folder

The report tells you *which* folder grew. `enter` opens it to show *what it is
made of*, from the same scan — no rescan, no waiting.

```
             0 B  ~/Library/Caches                     4.21 GiB here · 4 items

        SIZE        CHANGE      SHARE           HOLDS
 ─────────────────────────────────────────────────────────────────────────────
    2.40 GiB     +1.80 GiB      ██████░░░░  57% Homebrew/
    1.31 GiB       +112 MiB     ███░░░░░░░  31% go-build/
     404 MiB new     +404 MiB   █░░░░░░░░░  10% typescript/
    98.0 MiB          -4.0 MiB  █░░░░░░░░░   2% files here  (and folders under 1.00 MiB)

 1/4 · inspect · ↑↓ move · enter open folder · ← back · esc leave · o reveal · q quit
```

| Key | Action |
|---|---|
| `enter` / `→` | open the selected subfolder |
| `←` / `backspace` | back to the folder above |
| `esc` | leave inspect mode |

Files are shown **as one group**, not listed individually: the point is to find
which subfolder holds the space, and a folder holding ten thousand files is not
made clearer by ten thousand rows. The `files here` row also carries the
subfolders too small to have been recorded (1 MiB by default), so the rows always
add up to the whole folder — which is what makes the `SHARE` column meaningful.

A subfolder deleted since the previous scan stays on the list, showing what it
used to hold. Sizes here are cumulative and overlap with the rows inside them:
this is the one screen that answers "what is in here" rather than "what changed",
so it is read one folder at a time and never summed across levels.

## How growth is attributed

Directory sizes are cumulative, so the obvious approach double counts badly. If
`~/a/b/c` grows by 5 GB, then `~/a`, `~/a/b` and `~/a/b/c` have all grown by
5 GB, and a naive "top 20" becomes a chain of the same number repeated down a
path, hiding every other cause.

So growth is attributed **exclusively**: every byte is charged to exactly one
directory.

- A directory in both scans is charged only for the bytes held *directly* in it.
  Its subdirectories answer for themselves.
- A directory that did not exist before is charged for its whole subtree, but
  only if its parent already existed. Otherwise the entire new subtree collapses
  onto its topmost new directory, so a freshly installed `node_modules` reads as
  one row rather than four hundred.
- Removals mirror additions, as negative numbers.

The deltas therefore sum exactly to the root's total change. That invariant is
enforced by a test, and it is what makes the ranking trustworthy.

The `TOTAL` column still shows each directory's full cumulative size, so you
keep the context you need before deleting something.

## The HTML report

```bash
disk-space-differ --report            # scan, then write the report and open it
disk-space-differ --report --no-scan  # rebuild from stored snapshots
```

`--report` always writes `report.html` in the working directory, replacing the
one from the last run, and then opens it — so a browser tab left on that file
shows the newest report on reload.

A single self-contained file — no external requests, no CDN, no network. It
opens straight from disk and keeps working offline.

It contains:

- **A KPI row** — total size, net change, space added and space reclaimed.
- **Total size over time** — one line, so the chart carries no legend; the title
  names it.
- **Fastest growing folders** — the folders driving that growth, over time.
- **Change by folder** — a diverging bar chart: growth to the right, space
  reclaimed to the left, around a neutral zero line.
- **A table view** of every changed folder, so no value is reachable only by
  hovering.

A range control above the charts (since last run / last 7 / last 30 / all runs)
scopes everything below it at once, so the charts, the stats and the table
always agree. Each range is computed server-side rather than sliced in the
browser, because exclusive attribution needs each folder's own-bytes figure at
both endpoints — cumulative sizes alone would reintroduce the double counting.

The trend chart never plots a folder together with one of its own ancestors:
two such lines would draw the same bytes twice. Series are picked by exclusive
growth and any nested candidate is skipped, so the lines cover disjoint parts of
the tree.

Charts are keyboard reachable — focus one and use `←` / `→` to move the
crosshair; the readout is the same as on hover. Light and dark are both
first-class, and the palette is validated for colour-vision deficiency rather
than eyeballed.

The report needs at least two runs to show trends. With one it says so.

## Configuration

Written to `~/.config/disk-space-differ/config.json` by `--init-config`:

```json
{
  "roots": ["/Users/you"],
  "ignore_names": [],
  "ignore_paths": [],
  "min_dir_size": 1048576,
  "retention": 30,
  "top": 40,
  "follow_symlinks": false,
  "database_path": "/Users/you/.local/share/disk-space-differ/snapshots.db"
}
```

- **`roots`** — what to scan. Defaults to your home directory. `~` is expanded.
- **`ignore_names`** — directory names skipped anywhere, e.g. `["node_modules"]`.
- **`min_dir_size`** — directories smaller than this are folded into their
  nearest recorded ancestor instead of getting their own row. This is the main
  lever on database size; their bytes are still counted, just not itemised.
- **`retention`** — snapshots kept per root. A home directory snapshot is
  roughly 4 MB, so the default of 30 costs about 130 MB. Lower it, or raise
  `min_dir_size`, if that bothers you.
- **`top`** — how many rows the report shows.
- **`follow_symlinks`** — off by default, because following links double counts
  targets and can cycle.

## Running it on a schedule

Growth is only meaningful against a previous run, so a regular cadence is what
makes the trend column useful:

```cron
0 9 * * * /usr/local/bin/disk-space-differ --plain >> ~/disk-growth.log 2>&1
```

`--report` is not for cron: it opens a browser. Scan on a schedule with
`--plain`, then read the history whenever you like with `--report --no-scan`.

## Deleting

`d` asks for confirmation and shows the full path and the space it will free.
Deletion is permanent — it does not go through the trash. Scan roots, your home
directory and `/` are refused outright.

## Notes and limits

- Sizes are **allocated blocks** (what the disk actually gives up), not apparent
  size. These differ, especially with many small files.
- Directories the scanner cannot open — usually macOS privacy protections — are
  counted and reported. Their contents are missing from the totals, so a
  non-zero count means the report understates real usage.
- Hard links are counted once per scan.
- Parallel scanning is tuned for SSDs. On spinning disks seek time dominates and
  the advantage shrinks.

## Development

```bash
go test ./...
```

The diff attribution, level folding, storage round-trip, scanner invariants and
TUI rendering are all covered.
