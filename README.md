# disk-space-differ

Finds the directories that keep growing, and lets you delete them.

Each run scans your configured roots, records a snapshot, and reports the top
directories by **growth since the previous run** — not by size. A 200 GB
directory that never changes is not your problem; a 2 GB cache that regrows
every week is.

```
 disk-space-differ                                       compared with 3d ago
      +4.21 GiB  ~                    77.0 GiB total · 2,388,362 items · 28.9s

 #           GROWTH      TREND              TOTAL PATH
 ─────────────────────────────────────────────────────────────────────────────
 1        +2.31 GiB      ▁▁▂▃▅█          4.10 GiB ~/Library/Caches/Homebrew
 2        +1.02 GiB new  ▁▁▁▁▁█          1.02 GiB ~/c/app/node_modules
 3         +512 MiB      ▁▂▃▄▅█           980 MiB ~/Library/Caches/go-build

 1/20 · growth · ↑↓ move · tab view · d delete · o open · r rescan · q quit
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
disk-space-differ --plain         # print and exit (for cron)
disk-space-differ --json          # machine-readable output
disk-space-differ --html report.html --open   # charted HTML report
disk-space-differ --init-config   # write a default config file
```

| Key | Action |
|---|---|
| `↑` `↓` / `j` `k` | move |
| `tab` | cycle view: growth → all changes → largest |
| `d` | delete the selected directory (asks first) |
| `o` | reveal in the file manager |
| `r` | rescan |
| `q` | quit |

The first run has nothing to compare against, so it records a baseline and lists
the directories holding the most space. Growth appears from the second run on.

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
disk-space-differ --html growth.html --open   # scan, then write the report
disk-space-differ --html growth.html --no-scan  # rebuild from stored snapshots
```

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
  "top": 20,
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
- **`follow_symlinks`** — off by default, because following links double counts
  targets and can cycle.

## Running it on a schedule

Growth is only meaningful against a previous run, so a regular cadence is what
makes the trend column useful:

```cron
0 9 * * * /usr/local/bin/disk-space-differ --plain >> ~/disk-growth.log 2>&1
0 9 * * * /usr/local/bin/disk-space-differ --html ~/disk-growth.html
```

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

The diff attribution, storage round-trip, scanner invariants and TUI rendering
are all covered.
