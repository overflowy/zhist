package main

import (
	"bufio"
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

const maxJSONLineSize = 4 * 1024 * 1024

var errEntryNotFound = errors.New("entry not found")

type Entry struct {
	T int64  `json:"t"`           // unix timestamp
	D string `json:"d"`           // working directory ("" if unknown)
	X int    `json:"x"`           // exit status (-1 if unknown)
	C string `json:"c"`           // full command, may contain newlines
	M int64  `json:"m,omitempty"` // duration in milliseconds (0 if unknown)
}

type Row struct {
	Entry
	ID string
}

type Store struct {
	path string
}

type storedRow struct {
	Entry
	id     string
	offset int64
}

func newStore(path string) Store {
	return Store{path: path}
}

func (s Store) Append(entries []Entry) error {
	encoded := make([][]byte, len(entries))
	for i, entry := range entries {
		line, err := encodeEntry(entry)
		if err != nil {
			return err
		}
		encoded[i] = line
	}
	if len(encoded) == 0 {
		return nil
	}

	return s.withLock(func() error {
		f, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			return err
		}
		info, err := f.Stat()
		if err != nil {
			f.Close()
			return err
		}
		originalSize := info.Size()
		rollback := func() {
			if err := f.Truncate(originalSize); err != nil {
				_ = os.Truncate(s.path, originalSize)
			}
		}
		fail := func(err error) error {
			rollback()
			_ = f.Close()
			return err
		}

		if originalSize > 0 {
			var last [1]byte
			if _, err := f.ReadAt(last[:], originalSize-1); err != nil {
				f.Close()
				return err
			}
			if last[0] != '\n' {
				trailingSize, err := trailingLineSize(f, originalSize)
				if err != nil {
					f.Close()
					return err
				}
				if trailingSize >= maxJSONLineSize {
					f.Close()
					return fmt.Errorf("cannot append newline to trailing line of at least %d bytes; limit is %d", trailingSize, maxJSONLineSize)
				}
				if _, err := f.Write([]byte{'\n'}); err != nil {
					return fail(err)
				}
			}
		}
		for _, line := range encoded {
			if _, err := f.Write(line); err != nil {
				return fail(err)
			}
		}
		if err := f.Close(); err != nil {
			rollback()
			return err
		}
		return nil
	})
}

func trailingLineSize(f *os.File, size int64) (int64, error) {
	const chunkSize = 32 * 1024
	buf := make([]byte, chunkSize)
	var total int64
	for end := size; end > 0; {
		start := max(end-chunkSize, 0)
		n := int(end - start)
		if _, err := f.ReadAt(buf[:n], start); err != nil {
			return 0, err
		}
		if i := bytes.LastIndexByte(buf[:n], '\n'); i >= 0 {
			return total + int64(n-i-1), nil
		}
		total += int64(n)
		if total >= maxJSONLineSize {
			return total, nil
		}
		end = start
	}
	return total, nil
}

func (s Store) List() ([]Row, error) {
	var rows []storedRow
	err := s.withReadLock(func() error {
		var err error
		rows, err = s.readAll()
		return err
	})
	if err != nil {
		return nil, err
	}
	out := make([]Row, len(rows))
	for i, row := range rows {
		out[i] = Row{Entry: row.Entry, ID: row.id}
	}
	return out, nil
}

func (s Store) Get(id string) (Entry, error) {
	offset, hash, ok := parseID(id)
	if !ok {
		return Entry{}, errEntryNotFound
	}
	// Keep the direct seek lockless. Verification rejects torn reads before fallback.
	entry, matched, err := s.getAt(offset, hash)
	if err != nil {
		return Entry{}, err
	}
	if matched {
		return entry, nil
	}

	var rows []storedRow
	err = s.withReadLock(func() error {
		var err error
		rows, err = s.readAll()
		return err
	})
	if err != nil {
		return Entry{}, err
	}
	for i := len(rows) - 1; i >= 0; i-- {
		if contentHash(rows[i].Entry) == hash {
			return rows[i].Entry, nil
		}
	}
	return Entry{}, errEntryNotFound
}

func (s Store) Delete(id string, all bool) error {
	offset, hash, ok := parseID(id)
	if !ok {
		return nil
	}

	return s.withLock(func() error {
		rows, err := s.readAll()
		if err != nil {
			return err
		}
		target := -1
		for i := range rows {
			if rows[i].offset == offset && contentHash(rows[i].Entry) == hash {
				target = i
				break
			}
		}
		if target < 0 {
			for i := len(rows) - 1; i >= 0; i-- {
				if contentHash(rows[i].Entry) == hash {
					target = i
					break
				}
			}
		}
		if target < 0 {
			return nil
		}

		kept := make([]Entry, 0, len(rows)-1)
		for i, row := range rows {
			if all {
				if row.C != rows[target].C {
					kept = append(kept, row.Entry)
				}
				continue
			}
			if i != target {
				kept = append(kept, row.Entry)
			}
		}
		return s.writeAll(kept)
	})
}

func encodeEntry(entry Entry) ([]byte, error) {
	if entry.C == "" {
		return nil, fmt.Errorf("empty command")
	}
	encoded, err := json.Marshal(entry)
	if err != nil {
		return nil, err
	}
	encoded = append(encoded, '\n')
	if len(encoded) > maxJSONLineSize {
		return nil, fmt.Errorf("encoded JSON line for entry %s is %d bytes; limit is %d", contentHash(entry), len(encoded), maxJSONLineSize)
	}
	return encoded, nil
}

func contentHash(entry Entry) string {
	h := sha1.Sum(fmt.Appendf(nil, "%d\x00%s\x00%d\x00%s\x00%d", entry.T, entry.D, entry.X, entry.C, entry.M))
	return hex.EncodeToString(h[:6])
}

func makeID(offset int64, entry Entry) string {
	return strconv.FormatInt(offset, 36) + "-" + contentHash(entry)
}

func parseID(id string) (int64, string, bool) {
	offsetText, hash, ok := strings.Cut(id, "-")
	if !ok || offsetText == "" || len(hash) != 12 {
		return 0, "", false
	}
	offset, err := strconv.ParseInt(offsetText, 36, 64)
	if err != nil || offset < 0 {
		return 0, "", false
	}
	decoded, err := hex.DecodeString(hash)
	if err != nil || len(decoded) != 6 {
		return 0, "", false
	}
	return offset, hash, true
}

func (s Store) getAt(offset int64, hash string) (Entry, bool, error) {
	f, err := os.Open(s.path)
	if os.IsNotExist(err) {
		return Entry{}, false, nil
	}
	if err != nil {
		return Entry{}, false, err
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return Entry{}, false, err
	}

	r := bufio.NewReader(io.LimitReader(f, maxJSONLineSize+1))
	line, err := r.ReadBytes('\n')
	if err != nil && err != io.EOF {
		return Entry{}, false, err
	}
	if len(line) == 0 || len(line) > maxJSONLineSize {
		return Entry{}, false, nil
	}
	var entry Entry
	if err := json.Unmarshal(line, &entry); err != nil || entry.C == "" {
		return Entry{}, false, nil
	}
	if contentHash(entry) != hash {
		return Entry{}, false, nil
	}
	return entry, true, nil
}

// Callers lock readAll because Delete already holds the exclusive lock; relocking can deadlock.
func (s Store) readAll() ([]storedRow, error) {
	f, err := os.Open(s.path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var rows []storedRow
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), maxJSONLineSize+1)
	sc.Split(scanJSONLines)
	var offset int64
	lineNumber := 0
	for sc.Scan() {
		lineNumber++
		line := sc.Bytes()
		if len(line) > maxJSONLineSize {
			return nil, fmt.Errorf("read %s line %d: encoded JSON line is %d bytes; limit is %d", s.path, lineNumber, len(line), maxJSONLineSize)
		}
		var entry Entry
		if err := json.Unmarshal(line, &entry); err != nil {
			return nil, fmt.Errorf("read %s line %d: %w", s.path, lineNumber, err)
		}
		if entry.C == "" {
			return nil, fmt.Errorf("read %s line %d: empty command", s.path, lineNumber)
		}
		rows = append(rows, storedRow{Entry: entry, id: makeID(offset, entry), offset: offset})
		offset += int64(len(line))
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", s.path, err)
	}
	return rows, nil
}

func scanJSONLines(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		return i + 1, data[:i+1], nil
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

func (s Store) writeAll(entries []Entry) error {
	tmp := s.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = os.Remove(tmp)
		}
	}()
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	w := bufio.NewWriter(f)
	for _, entry := range entries {
		line, err := encodeEntry(entry)
		if err != nil {
			f.Close()
			return err
		}
		if _, err := w.Write(line); err != nil {
			f.Close()
			return err
		}
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	// Renaming without syncing the file and directory can expose empty or stale history after a crash.
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}
	removeTemp = false

	dir, err := os.Open(filepath.Dir(s.path))
	if err != nil {
		return err
	}
	syncErr := dir.Sync()
	closeErr := dir.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

func (s Store) withLock(fn func() error) error {
	return s.withFlock(syscall.LOCK_EX, fn)
}

func (s Store) withReadLock(fn func() error) error {
	return s.withFlock(syscall.LOCK_SH, fn)
}

func (s Store) withFlock(mode int, fn func() error) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	lock, err := os.OpenFile(s.path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	if err := syscall.Flock(int(lock.Fd()), mode); err != nil {
		lock.Close()
		return err
	}
	defer func() {
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		_ = lock.Close()
	}()
	return fn()
}
