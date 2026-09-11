// Command disk-space-differ reports which directories grew the most since the
// previous run, and lets you delete the ones you no longer want.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/grillermo/disk-space-differ/internal/config"
	"github.com/grillermo/disk-space-differ/internal/htmlreport"
	"github.com/grillermo/disk-space-differ/internal/humanize"
	"github.com/grillermo/disk-space-differ/internal/report"
	"github.com/grillermo/disk-space-differ/internal/store"
	"github.com/grillermo/disk-space-differ/internal/tui"
)

const version = "0.1.0"

type rootList []string

func (r *rootList) String() string     { return strings.Join(*r, ",") }
func (r *rootList) Set(v string) error { *r = append(*r, v); return nil }

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		roots      rootList
		configPath = flag.String("config", config.DefaultPath(), "path to the config file")
		dbPath     = flag.String("db", "", "path to the snapshot database (overrides config)")
		top        = flag.Int("top", 0, "how many directories to report (overrides config)")
		lvl        = flag.Int("level", 1, "tree level to report: 1 is leaf folders, 2 is the folders holding them, and so on")
		plain      = flag.Bool("plain", false, "print the report and exit instead of opening the TUI")
		asJSON     = flag.Bool("json", false, "print the report as JSON and exit")
		htmlReport = flag.Bool("report", false, "write report.html with charts here, replacing any earlier one, and open it")
		noScan     = flag.Bool("no-scan", false, "report the stored snapshots without scanning; starts instantly")
		scans      = flag.Int("scans", report.MinScans, "how many recorded scans the comparison spans: 2 is the last two, more reaches further back (+/- in the TUI)")
		initConfig = flag.Bool("init-config", false, "write a default config file and exit")
		showVer    = flag.Bool("version", false, "print the version and exit")
	)
	flag.Var(&roots, "root", "directory to scan; repeatable, overrides config")

	flag.Usage = usage
	flag.Parse()

	if *showVer {
		fmt.Println("disk-space-differ", version)
		return nil
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}

	if *initConfig {
		if err := config.Save(*configPath, cfg); err != nil {
			return err
		}
		fmt.Println("wrote", *configPath)
		return nil
	}

	// Positional arguments are treated as roots too, so `dsd ~/Downloads` works.
	if extra := flag.Args(); len(extra) > 0 {
		roots = append(roots, extra...)
	}
	if len(roots) > 0 {
		cfg.Roots = roots
	}
	if *dbPath != "" {
		cfg.DatabasePath = *dbPath
	}
	if *top > 0 {
		cfg.Top = *top
	}

	st, err := store.Open(cfg.DatabasePath)
	if err != nil {
		return err
	}
	defer st.Close()

	if *htmlReport {
		return runHTML(cfg, st, reportFile, *noScan)
	}
	window := max(report.MinScans, *scans)
	if *plain || *asJSON {
		return runPlain(cfg, st, *asJSON, max(1, *lvl), *noScan, window)
	}
	return runTUI(cfg, st, *noScan, window)
}

// reportFile is where -report always writes: one file in the working directory,
// replaced on every run, so the link the browser already has stays current.
const reportFile = "report.html"

// runHTML scans (unless told not to), writes the charted report and opens it.
func runHTML(cfg config.Config, st *store.Store, path string, noScan bool) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	roots := cfg.ResolvedRoots()
	if !noScan {
		for _, root := range roots {
			fmt.Fprintf(os.Stderr, "scanning %s...\n", root)
			if _, err := report.Run(ctx, st, cfg, root, nil); err != nil {
				return err
			}
		}
	}

	data, err := htmlreport.Build(st, roots, cfg.Retention, version)
	if err != nil {
		return err
	}
	if err := htmlreport.WriteFile(path, data); err != nil {
		return err
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	fmt.Println("wrote", abs)

	// A failure to open is worth saying out loud but not worth failing over:
	// the report is already on disk and the path was just printed.
	if err := browserOpen(abs); err != nil {
		fmt.Fprintln(os.Stderr, "could not open the report:", err)
	}
	return nil
}

// browserOpen asks the desktop to open a path with its default application.
func browserOpen(path string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", path).Start()
	case "windows":
		return exec.Command("explorer", path).Start()
	default:
		return exec.Command("xdg-open", path).Start()
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `disk-space-differ %s — find the directories that keep growing

Usage:
  disk-space-differ [flags] [dir...]

Each run scans the configured roots, records a snapshot, and reports the
directories that grew the most since the previous run. Growth is attributed
exclusively, so a directory is charged only for the bytes it is actually
responsible for rather than for everything beneath it.

With no previous run to compare against, the first run records a baseline and
lists the directories holding the most space.

Flags:
`, version)
	flag.PrintDefaults()
	fmt.Fprintf(os.Stderr, `
Config file: %s
Snapshots:   %s
`, config.DefaultPath(), store.DefaultPath())
}

func runTUI(cfg config.Config, st *store.Store, noScan bool, scans int) error {
	m := tui.New(cfg, st, noScan, scans)
	p := tea.NewProgram(m, tea.WithAltScreen())
	m.SetProgram(p)
	_, err := p.Run()
	return err
}

// runPlain produces the same report without the interactive layer, for cron
// jobs and shell pipelines.
func runPlain(cfg config.Config, st *store.Store, asJSON bool, lvl int, noScan bool, scans int) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	var results []*report.Result
	for _, root := range cfg.ResolvedRoots() {
		var res *report.Result
		var err error
		switch {
		case noScan:
			res, err = report.FromStore(st, root, scans)
		default:
			if !asJSON {
				fmt.Fprintf(os.Stderr, "scanning %s...\n", root)
			}
			res, err = report.Run(ctx, st, cfg, root, nil)
			// The fresh scan is now the newest stored one, so a wider window is
			// the same report read further back rather than a second scan.
			if err == nil && scans > report.MinScans && !res.Baseline {
				res, err = report.FromStore(st, root, scans)
			}
		}
		if errors.Is(err, report.ErrNeedTwoScans) {
			return fmt.Errorf("%w — run without -no-scan twice to record them", err)
		}
		if err != nil {
			return err
		}
		results = append(results, res)
	}

	if asJSON {
		return printJSON(results, lvl, cfg.Top)
	}
	printText(results, lvl, cfg.Top)
	return nil
}

func printText(results []*report.Result, lvl, top int) {
	for _, res := range results {
		fmt.Printf("\n%s\n", res.Root)

		if res.Baseline {
			fmt.Printf("  first run — baseline recorded (%s across %d items in %s)\n",
				humanize.Bytes(res.Current.TotalUsage),
				res.Current.ItemCount,
				humanize.Duration(res.Current.Duration))
			fmt.Printf("  largest directories:\n")
		} else {
			span := ""
			if res.Scans > report.MinScans {
				span = fmt.Sprintf(", across the last %d scans", res.Scans)
			}
			fmt.Printf("  %s since %s%s  (now %s, scanned in %s)\n",
				humanize.SignedBytes(res.TotalDelta()),
				humanize.Since(res.Previous.StartedAt),
				span,
				humanize.Bytes(res.Current.TotalUsage),
				humanize.Duration(res.Current.Duration))
		}

		if res.Current.Unreadable > 0 {
			fmt.Printf("  note: %d entries were unreadable and are missing from these totals\n",
				res.Current.Unreadable)
		}
		if !res.Baseline {
			fmt.Printf("  top %d by growth at level %d of %d:\n", top, min(lvl, res.MaxLevel()), res.MaxLevel())
		}

		rows := res.Growth(lvl, top)
		if len(rows) == 0 {
			fmt.Println("    (nothing grew)")
			continue
		}
		for i, row := range rows {
			tag := ""
			if s := row.Kind.String(); s != "" {
				tag = " [" + s + "]"
			}
			fmt.Printf("  %3d. %14s  %10s  %s%s\n",
				i+1, humanize.SignedBytes(row.Delta), humanize.Bytes(row.Usage), row.Path, tag)
		}
	}
}

type jsonRow struct {
	Path         string  `json:"path"`
	Delta        int64   `json:"delta"`
	SubtreeDelta int64   `json:"subtree_delta"`
	Usage        int64   `json:"usage"`
	PrevUsage    int64   `json:"prev_usage"`
	Kind         string  `json:"kind,omitempty"`
	History      []int64 `json:"history,omitempty"`
}

type jsonResult struct {
	Root       string    `json:"root"`
	Baseline   bool      `json:"baseline"`
	Scans      int       `json:"scans"`
	Level      int       `json:"level"`
	MaxLevel   int       `json:"max_level"`
	ScannedAt  string    `json:"scanned_at"`
	DurationMS int64     `json:"duration_ms"`
	TotalUsage int64     `json:"total_usage"`
	TotalDelta int64     `json:"total_delta"`
	ItemCount  int64     `json:"item_count"`
	Rows       []jsonRow `json:"rows"`
}

func printJSON(results []*report.Result, lvl, top int) error {
	out := make([]jsonResult, 0, len(results))
	for _, res := range results {
		jr := jsonResult{
			Root:       res.Root,
			Baseline:   res.Baseline,
			Scans:      res.Scans,
			Level:      min(lvl, res.MaxLevel()),
			MaxLevel:   res.MaxLevel(),
			ScannedAt:  res.Current.StartedAt.Format("2006-01-02T15:04:05Z07:00"),
			DurationMS: res.Current.Duration.Milliseconds(),
			TotalUsage: res.Current.TotalUsage,
			TotalDelta: res.TotalDelta(),
			ItemCount:  res.Current.ItemCount,
		}
		for _, row := range res.Growth(lvl, top) {
			jr.Rows = append(jr.Rows, jsonRow{
				Path:         row.Path,
				Delta:        row.Delta,
				SubtreeDelta: row.SubtreeDelta,
				Usage:        row.Usage,
				PrevUsage:    row.PrevUsage,
				Kind:         row.Kind.String(),
				History:      row.History,
			})
		}
		out = append(out, jr)
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}
