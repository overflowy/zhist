package main

import (
	"slices"
	"testing"
)

func searchFixture() []Row {
	return []Row{
		{Entry: Entry{T: 1, D: "/a", C: "git status"}},
		{Entry: Entry{T: 2, D: "/b", C: "git push"}},
		{Entry: Entry{T: 3, D: "/a", C: "ls -la"}},
		{Entry: Entry{T: 4, D: "/b", C: "git status"}},
		{Entry: Entry{T: 5, D: "/a", C: "go test ./..."}},
	}
}

func TestSearchRowsPrefixNewestFirstDedup(t *testing.T) {
	got := searchRows(searchFixture(), "git", "", 0)
	// "git status" ran twice; only the newest run survives.
	want := []string{"git status", "git push"}
	if !slices.Equal(got, want) {
		t.Errorf("searchRows(git) = %q, want %q", got, want)
	}
}

func TestSearchRowsDirFilter(t *testing.T) {
	got := searchRows(searchFixture(), "git", "/a", 0)
	want := []string{"git status"}
	if !slices.Equal(got, want) {
		t.Errorf("searchRows(git, /a) = %q, want %q", got, want)
	}
}

func TestSearchRowsLimit(t *testing.T) {
	got := searchRows(searchFixture(), "g", "", 1)
	want := []string{"go test ./..."}
	if !slices.Equal(got, want) {
		t.Errorf("searchRows(g, limit 1) = %q, want %q", got, want)
	}
}

func TestSearchRowsEmptyPrefixMatchesAll(t *testing.T) {
	got := searchRows(searchFixture(), "", "", 0)
	want := []string{"go test ./...", "git status", "ls -la", "git push"}
	if !slices.Equal(got, want) {
		t.Errorf("searchRows(\"\") = %q, want %q", got, want)
	}
}

func TestSearchRowsNoMatch(t *testing.T) {
	if got := searchRows(searchFixture(), "cargo", "", 0); len(got) != 0 {
		t.Errorf("searchRows(cargo) = %q, want empty", got)
	}
}

func TestSearchRowsUnsortedInput(t *testing.T) {
	rows := searchFixture()
	slices.Reverse(rows)
	got := searchRows(rows, "git", "", 0)
	want := []string{"git status", "git push"}
	if !slices.Equal(got, want) {
		t.Errorf("searchRows(git, reversed input) = %q, want %q", got, want)
	}
}

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
