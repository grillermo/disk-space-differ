package humanize

import "testing"

func TestBytes(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.00 KiB"},
		{1536, "1.50 KiB"},
		{20 * 1024, "20.0 KiB"},
		{500 * 1024, "500 KiB"},
		{1 << 30, "1.00 GiB"},
		{-1 << 30, "-1.00 GiB"},
	}
	for _, c := range cases {
		if got := Bytes(c.in); got != c.want {
			t.Errorf("Bytes(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSignedBytesAlwaysShowsSign(t *testing.T) {
	if got := SignedBytes(1024); got != "+1.00 KiB" {
		t.Errorf("SignedBytes(1024) = %q, want +1.00 KiB", got)
	}
	if got := SignedBytes(-1024); got != "-1.00 KiB" {
		t.Errorf("SignedBytes(-1024) = %q, want -1.00 KiB", got)
	}
}

func TestSparklineScalesToItsOwnRange(t *testing.T) {
	if got := Sparkline([]int64{0, 50, 100}); got != "▁▄█" {
		t.Errorf("Sparkline = %q, want ▁▄█", got)
	}
	// A flat series must not divide by zero.
	if got := Sparkline([]int64{7, 7, 7}); got != "▁▁▁" {
		t.Errorf("flat Sparkline = %q, want ▁▁▁", got)
	}
	if got := Sparkline(nil); got != "" {
		t.Errorf("empty Sparkline = %q, want empty", got)
	}
}

func TestTruncateKeepsPathTail(t *testing.T) {
	if got := Truncate("/home/user/projects/app", 10); got != "…jects/app" {
		t.Errorf("Truncate = %q, want …jects/app", got)
	}
	if got := Truncate("/short", 20); got != "/short" {
		t.Errorf("Truncate should leave short strings alone, got %q", got)
	}
}
