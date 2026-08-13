package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testStore(t *testing.T) Store {
	t.Helper()
	return newStore(filepath.Join(t.TempDir(), "history.jsonl"))
}

func TestStoreAppendListRoundTrip(t *testing.T) {
	store := testStore(t)
	entries := []Entry{
		{T: 10, D: "/tmp/one", X: 0, C: "printf hello", M: 1250},
		{T: 20, D: "/tmp/two", X: 1, C: "printf 'one\\ntwo'\nprintf done"},
	}
	if err := store.Append(entries); err != nil {
		t.Fatal(err)
	}

	rows, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(entries) {
		t.Fatalf("List returned %d rows, want %d", len(rows), len(entries))
	}
	for i, row := range rows {
		if row.Entry != entries[i] {
			t.Errorf("row %d entry = %#v, want %#v", i, row.Entry, entries[i])
		}
		if row.ID == "" || strings.ContainsAny(row.ID, " \t\n") {
			t.Errorf("row %d has invalid id %q", i, row.ID)
		}
	}
}

func TestStoreAppendAddsMissingNewline(t *testing.T) {
	store := testStore(t)
	first := Entry{T: 10, D: "/tmp", X: 0, C: "first"}
	encoded, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(store.path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}

	second := Entry{T: 20, D: "/tmp", X: 0, C: "second"}
	if err := store.Append([]Entry{second}); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(store.path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte("}\n{")) {
		t.Fatalf("history does not separate entries with a newline: %q", data)
	}
	rows, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Entry != first || rows[1].Entry != second {
		t.Fatalf("List returned %#v", rows)
	}
}

func TestStoreRejectsNewlineThatWouldOversizeTrailingLine(t *testing.T) {
	store := testStore(t)
	base, err := json.Marshal(Entry{T: 10, D: "/tmp", X: 0, C: ""})
	if err != nil {
		t.Fatal(err)
	}
	entry := Entry{T: 10, D: "/tmp", X: 0, C: strings.Repeat("x", maxJSONLineSize-len(base))}
	encoded, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) != maxJSONLineSize {
		t.Fatalf("encoded entry is %d bytes, want %d", len(encoded), maxJSONLineSize)
	}
	if err := os.MkdirAll(filepath.Dir(store.path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := store.Append([]Entry{{T: 20, C: "next"}}); err == nil {
		t.Fatal("Append accepted a newline that made the trailing line oversized")
	}
	after, err := os.ReadFile(store.path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, encoded) {
		t.Fatal("Append changed the file after rejecting the trailing line")
	}
}

func TestStoreRejectsOversizedEntryWithoutChangingFile(t *testing.T) {
	store := testStore(t)
	seed := Entry{T: 10, D: "/tmp", X: 0, C: "seed"}
	if err := store.Append([]Entry{seed}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(store.path)
	if err != nil {
		t.Fatal(err)
	}

	oversized := Entry{T: 20, D: "/tmp", X: 0, C: strings.Repeat("x", maxJSONLineSize)}
	if err := store.Append([]Entry{oversized}); err == nil {
		t.Fatal("Append accepted an oversized entry")
	}
	after, err := os.ReadFile(store.path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("Append changed the file after rejecting an oversized entry")
	}
}

func TestStoreGetDirectDoesNotScanLaterRows(t *testing.T) {
	store := testStore(t)
	entry := Entry{T: 10, D: "/tmp", X: 0, C: "target"}
	if err := store.Append([]Entry{entry}); err != nil {
		t.Fatal(err)
	}
	rows, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(store.path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("not json\n"); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	got, err := store.Get(rows[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if got != entry {
		t.Fatalf("Get returned %#v, want %#v", got, entry)
	}
}

func TestStoreGetDirectAndFallback(t *testing.T) {
	store := testStore(t)
	entries := []Entry{
		{T: 10, D: "/tmp", X: 0, C: "earlier"},
		{T: 20, D: "/tmp", X: 7, C: "target\ncontinued"},
		{T: 30, D: "/tmp", X: 0, C: "later"},
	}
	if err := store.Append(entries); err != nil {
		t.Fatal(err)
	}
	rows, err := store.List()
	if err != nil {
		t.Fatal(err)
	}

	got, err := store.Get(rows[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	if got != entries[1] {
		t.Fatalf("direct Get returned %#v, want %#v", got, entries[1])
	}

	if err := store.Delete(rows[0].ID, false); err != nil {
		t.Fatal(err)
	}
	got, err = store.Get(rows[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	if got != entries[1] {
		t.Fatalf("fallback Get returned %#v, want %#v", got, entries[1])
	}
}

func TestStoreExitStatusDistinguishesIDsAndFallbackGet(t *testing.T) {
	store := testStore(t)
	entries := []Entry{
		{T: 5, D: "/tmp", X: 0, C: "shift offsets"},
		{T: 10, D: "/tmp", X: 0, C: "same"},
		{T: 10, D: "/tmp", X: 7, C: "same"},
	}
	if err := store.Append(entries); err != nil {
		t.Fatal(err)
	}
	rows, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if rows[1].ID == rows[2].ID {
		t.Fatalf("entries with different exit statuses have the same id %q", rows[1].ID)
	}

	ids := []string{rows[1].ID, rows[2].ID}
	if err := store.Delete(rows[0].ID, false); err != nil {
		t.Fatal(err)
	}
	for i, id := range ids {
		got, err := store.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if got.X != entries[i+1].X {
			t.Errorf("Get(%q) exit status = %d, want %d", id, got.X, entries[i+1].X)
		}
	}
}

func TestStoreDuplicateIDsAndDelete(t *testing.T) {
	store := testStore(t)
	duplicate := Entry{T: 10, D: "/tmp", X: 0, C: "same"}
	marker := Entry{T: 15, D: "/tmp", X: 0, C: "marker"}
	other := Entry{T: 20, D: "/tmp", X: 0, C: "other"}
	if err := store.Append([]Entry{duplicate, marker, duplicate, other}); err != nil {
		t.Fatal(err)
	}
	rows, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].ID == rows[2].ID {
		t.Fatalf("duplicate entries have the same id %q", rows[0].ID)
	}

	if err := store.Delete(rows[0].ID, false); err != nil {
		t.Fatal(err)
	}
	rows, err = store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0].Entry != marker || rows[1].Entry != duplicate || rows[2].Entry != other {
		t.Fatalf("Delete removed the wrong rows: %#v", rows)
	}

	sameCommand := []Entry{
		{T: 30, D: "/one", X: 1, C: "same"},
		{T: 40, D: "/two", X: 2, C: "same"},
	}
	if err := store.Append(sameCommand); err != nil {
		t.Fatal(err)
	}
	rows, err = store.List()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(rows[1].ID, true); err != nil {
		t.Fatal(err)
	}
	rows, err = store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Entry != marker || rows[1].Entry != other {
		t.Fatalf("Delete all left %#v", rows)
	}
}

func TestStoreDeleteFallsBackToNewestHashMatch(t *testing.T) {
	store := testStore(t)
	earlier := Entry{T: 5, C: "earlier"}
	duplicate := Entry{T: 10, D: "/tmp", X: 0, C: "same"}
	marker := Entry{T: 15, C: "marker"}
	if err := store.Append([]Entry{earlier, duplicate, marker, duplicate}); err != nil {
		t.Fatal(err)
	}
	rows, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	staleID := rows[1].ID
	if err := store.Delete(rows[0].ID, false); err != nil {
		t.Fatal(err)
	}

	if err := store.Delete(staleID, false); err != nil {
		t.Fatal(err)
	}
	rows, err = store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Entry != duplicate || rows[1].Entry != marker {
		t.Fatalf("fallback Delete left %#v", rows)
	}
}

func TestStoreDeleteNonexistentID(t *testing.T) {
	store := testStore(t)
	entry := Entry{T: 10, D: "/tmp", X: 0, C: "keep"}
	if err := store.Append([]Entry{entry}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(store.path)
	if err != nil {
		t.Fatal(err)
	}

	if err := store.Delete("0-000000000000", false); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(store.path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("Delete changed the file for a nonexistent id")
	}
}
