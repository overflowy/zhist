package main

import "testing"

func TestFmtDur(t *testing.T) {
	cases := []struct {
		ms   int64
		want string
	}{
		{0, ""},
		{-5, ""},
		{1, "1ms"},
		{999, "999ms"},
		{1000, "1.0s"},
		{1250, "1.2s"},
		{59_999, "59.9s"},
		{60_000, "1m00s"},
		{83_000, "1m23s"},
		{3_599_000, "59m59s"},
		{3_600_000, "1h00m"},
		{9_000_000, "2h30m"},
		{90_000_000, "25h00m"},
	}
	for _, c := range cases {
		if got := fmtDur(c.ms); got != c.want {
			t.Errorf("fmtDur(%d) = %q, want %q", c.ms, got, c.want)
		}
	}
}
