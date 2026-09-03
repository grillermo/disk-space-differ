// Package humanize renders byte counts and durations for terminal display.
package humanize

import (
	"fmt"
	"strings"
	"time"
)

var units = []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB"}

// Bytes formats a byte count with a binary unit, e.g. "1.3 GiB".
func Bytes(n int64) string {
	neg := n < 0
	if neg {
		n = -n
	}

	value := float64(n)
	unit := 0
	for value >= 1024 && unit < len(units)-1 {
		value /= 1024
		unit++
	}

	var s string
	switch {
	case unit == 0:
		s = fmt.Sprintf("%d B", int64(value))
	case value < 10:
		s = fmt.Sprintf("%.2f %s", value, units[unit])
	case value < 100:
		s = fmt.Sprintf("%.1f %s", value, units[unit])
	default:
		s = fmt.Sprintf("%.0f %s", value, units[unit])
	}

	if neg {
		return "-" + s
	}
	return s
}

// SignedBytes formats a delta, always showing its sign.
func SignedBytes(n int64) string {
	if n > 0 {
		return "+" + Bytes(n)
	}
	return Bytes(n)
}

// Duration renders a scan duration at a sensible precision.
func Duration(d time.Duration) string {
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	default:
		return d.Round(time.Second).String()
	}
}

// Since renders how long ago t was, in coarse terms.
func Since(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

var sparkLevels = []rune("▁▂▃▄▅▆▇█")

// Sparkline draws a series as block characters, scaled between its own minimum
// and maximum so that small movements in a large directory stay visible.
func Sparkline(series []int64) string {
	if len(series) == 0 {
		return ""
	}

	minV, maxV := series[0], series[0]
	for _, v := range series {
		if v < minV {
			minV = v
		}
		if v > maxV {
			maxV = v
		}
	}

	var b strings.Builder
	span := maxV - minV
	for _, v := range series {
		if span == 0 {
			b.WriteRune(sparkLevels[0])
			continue
		}
		idx := (v - minV) * int64(len(sparkLevels)-1) / span
		b.WriteRune(sparkLevels[idx])
	}
	return b.String()
}

// Truncate shortens a path to width, keeping the tail, which carries the
// identifying part of a path.
func Truncate(s string, width int) string {
	if width <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= width {
		return s
	}
	if width <= 1 {
		return string(runes[len(runes)-width:])
	}
	return "…" + string(runes[len(runes)-(width-1):])
}
