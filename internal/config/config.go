// Package config loads user settings, falling back to defaults on first run.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/grillermo/disk-space-differ/internal/scan"
	"github.com/grillermo/disk-space-differ/internal/store"
)

// Config controls what gets scanned and how much history is kept.
type Config struct {
	// Roots are the directories to scan. Defaults to the user's home directory.
	Roots []string `json:"roots"`
	// IgnoreNames are directory base names skipped anywhere in the tree.
	IgnoreNames []string `json:"ignore_names"`
	// IgnorePaths are absolute directory paths skipped entirely.
	IgnorePaths []string `json:"ignore_paths"`
	// MinDirSize is the smallest directory recorded on its own row, in bytes.
	MinDirSize int64 `json:"min_dir_size"`
	// Retention is how many snapshots to keep per root.
	Retention int `json:"retention"`
	// Top is how many rows the report shows.
	Top int `json:"top"`
	// FollowSymlinks counts symlinked content. Off by default.
	FollowSymlinks bool `json:"follow_symlinks"`
	// DatabasePath overrides where snapshots are stored.
	DatabasePath string `json:"database_path"`
}

// Default returns the configuration used when no config file exists.
func Default() Config {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return Config{
		Roots:          []string{home},
		IgnoreNames:    []string{},
		IgnorePaths:    []string{},
		MinDirSize:     scan.DefaultMinDirSize,
		Retention:      store.DefaultRetention,
		Top:            40,
		FollowSymlinks: false,
		DatabasePath:   store.DefaultPath(),
	}
}

// DefaultPath is where the config file lives unless overridden.
func DefaultPath() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "config.json"
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "disk-space-differ", "config.json")
}

// Load reads the config file, returning defaults if it does not exist. Fields
// omitted from the file keep their default values.
func Load(path string) (Config, error) {
	cfg := Default()

	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("reading %s: %w", path, err)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parsing %s: %w", path, err)
	}

	return cfg.withFallbacks(), nil
}

// withFallbacks repairs values a hand written config may have left empty or
// nonsensical, so a partial file never produces a broken scan.
func (c Config) withFallbacks() Config {
	def := Default()
	if len(c.Roots) == 0 {
		c.Roots = def.Roots
	}
	if c.MinDirSize <= 0 {
		c.MinDirSize = def.MinDirSize
	}
	if c.Retention <= 0 {
		c.Retention = def.Retention
	}
	if c.Top <= 0 {
		c.Top = def.Top
	}
	if c.DatabasePath == "" {
		c.DatabasePath = def.DatabasePath
	}
	return c
}

// Save writes the config file, creating its directory if needed.
func Save(path string, cfg Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// ScanOptions projects the config onto the scanner's options.
func (c Config) ScanOptions() scan.Options {
	return scan.Options{
		IgnorePaths:    c.IgnorePaths,
		IgnoreNames:    c.IgnoreNames,
		MinDirSize:     c.MinDirSize,
		FollowSymlinks: c.FollowSymlinks,
	}
}

// ResolvedRoots expands ~ and relative paths in the configured roots.
func (c Config) ResolvedRoots() []string {
	out := make([]string, 0, len(c.Roots))
	for _, r := range c.Roots {
		abs, err := filepath.Abs(scan.ExpandHome(r))
		if err != nil {
			continue
		}
		out = append(out, abs)
	}
	return out
}
