// zhist is a shell history store with per-entry directory and exit status.
// Entries are appended as JSON lines. fzf provides the UI, wired up by the
// zsh integration that `zhist init` emits.
package main

import (
	"bufio"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Entry struct {
	T int64  `json:"t"` // unix timestamp
	D string `json:"d"` // working directory ("" if unknown)
	X int    `json:"x"` // exit status (-1 if unknown)
	C string `json:"c"` // full command, may contain newlines
}

func (e Entry) id() string {
	h := sha1.Sum(fmt.Appendf(nil, "%d\x00%s\x00%s", e.T, e.D, e.C))
	return hex.EncodeToString(h[:6])
}

func dataPath() string {
	if p := os.Getenv("ZHIST_FILE"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "zhist", "history.jsonl")
}

func readAll(path string) ([]Entry, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	line := 0
	for sc.Scan() {
		line++
		var e Entry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			return nil, fmt.Errorf("read %s line %d: %w", path, line, err)
		}
		if e.C == "" {
			return nil, fmt.Errorf("read %s line %d: empty command", path, line)
		}
		out = append(out, e)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return out, nil
}

func writeAll(path string, entries []Entry) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)
	for _, e := range entries {
		if err := enc.Encode(e); err != nil {
			f.Close()
			return err
		}
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func appendEntry(path string, e Entry) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewEncoder(f).Encode(e)
}

func relTime(t int64, now int64) string {
	ago := now - t
	switch {
	case ago < 60:
		return fmt.Sprintf("%2ds ago", ago)
	case ago < 3600:
		return fmt.Sprintf("%2dm ago", ago/60)
	case ago < 86400:
		return fmt.Sprintf("%2dh ago", ago/3600)
	case ago < 604800:
		return fmt.Sprintf("%2dd ago", ago/86400)
	default:
		return fmt.Sprintf("%2dw ago", ago/604800)
	}
}

func cmdAdd(args []string) {
	fs := flag.NewFlagSet("add", flag.ExitOnError)
	dir := fs.String("dir", "", "working directory")
	exit := fs.Int("exit", -1, "exit status")
	ts := fs.Int64("ts", 0, "unix timestamp (default now)")
	fs.Parse(args)
	raw, _ := io.ReadAll(os.Stdin)
	cmd := strings.TrimRight(string(raw), "\n")
	if strings.TrimSpace(cmd) == "" {
		return
	}
	t := *ts
	if t == 0 {
		t = time.Now().Unix()
	}
	if err := appendEntry(dataPath(), Entry{T: t, D: *dir, X: *exit, C: cmd}); err != nil {
		fmt.Fprintln(os.Stderr, "zhist:", err)
		os.Exit(1)
	}
}

const (
	cBlue  = "\033[34m"
	cDim   = "\033[2m"
	cRed   = "\033[31m"
	cReset = "\033[0m"
)

func cmdList(args []string) {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	dir := fs.String("dir", "", "only entries recorded in this directory")
	fs.Parse(args)
	entries, err := readAll(dataPath())
	if err != nil {
		fmt.Fprintln(os.Stderr, "zhist:", err)
		os.Exit(1)
	}
	// SHARE_HISTORY-imported files interleave sessions, so file order is not
	// time order.
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].T < entries[j].T })
	now := time.Now().Unix()
	w := bufio.NewWriter(os.Stdout)
	defer w.Flush()
	seen := make(map[string]bool, len(entries))
	// Newest first, dedup by command keeping the most recent occurrence.
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if *dir != "" && e.D != *dir {
			continue
		}
		if seen[e.C] {
			continue
		}
		seen[e.C] = true
		disp := e.C
		if i := strings.IndexByte(disp, '\n'); i >= 0 {
			disp = disp[:i] + " ⏎"
		}
		col := ""
		if e.X > 0 {
			col = cRed
		}
		fmt.Fprintf(w, "%s\t%s%8s%s\t%s%s%s\n",
			e.id(),
			cBlue, relTime(e.T, now), cReset,
			col, disp, cReset)
	}
}

func cmdGet(args []string) {
	fs := flag.NewFlagSet("get", flag.ExitOnError)
	id := fs.String("id", "", "entry id")
	fs.Parse(args)
	entries, err := readAll(dataPath())
	if err != nil {
		fmt.Fprintln(os.Stderr, "zhist:", err)
		os.Exit(1)
	}
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].id() == *id {
			fmt.Println(entries[i].C)
			return
		}
	}
	os.Exit(1)
}

func cmdDelete(args []string) {
	fs := flag.NewFlagSet("delete", flag.ExitOnError)
	id := fs.String("id", "", "entry id")
	all := fs.Bool("all", false, "delete every entry with the same command")
	fs.Parse(args)
	path := dataPath()
	entries, err := readAll(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "zhist:", err)
		os.Exit(1)
	}
	targetCommand := ""
	for i := range entries {
		if entries[i].id() == *id {
			targetCommand = entries[i].C
			break
		}
	}
	if targetCommand == "" {
		return
	}
	kept := entries[:0]
	for _, e := range entries {
		if *all && e.C == targetCommand {
			continue
		}
		if !*all && e.id() == *id {
			continue
		}
		kept = append(kept, e)
	}
	if err := writeAll(path, kept); err != nil {
		fmt.Fprintln(os.Stderr, "zhist:", err)
		os.Exit(1)
	}
}

var zshHistLine = regexp.MustCompile(`^: (\d+):(\d+);(.*)$`)

// cmdImport reads a zsh EXTENDED_HISTORY file. Directory and exit status are
// unknown for imported entries.
func cmdImport(args []string) {
	fs := flag.NewFlagSet("import", flag.ExitOnError)
	fs.Parse(args)
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: zhist import <zsh_history_file>")
		os.Exit(2)
	}
	f, err := os.Open(fs.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, "zhist:", err)
		os.Exit(1)
	}
	defer f.Close()
	path := dataPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "zhist:", err)
		os.Exit(1)
	}
	out, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		fmt.Fprintln(os.Stderr, "zhist:", err)
		os.Exit(1)
	}
	defer out.Close()
	enc := json.NewEncoder(out)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	n := 0
	var cur *Entry
	flush := func() {
		if cur != nil {
			enc.Encode(*cur)
			n++
			cur = nil
		}
	}
	for sc.Scan() {
		line := sc.Text()
		if cur != nil {
			// Continuation of a multiline entry.
			cur.C += "\n" + strings.TrimSuffix(line, "\\")
			if !strings.HasSuffix(line, "\\") {
				flush()
			}
			continue
		}
		m := zshHistLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		t, _ := strconv.ParseInt(m[1], 10, 64)
		cmd := m[3]
		cur = &Entry{T: t, X: -1, C: strings.TrimSuffix(cmd, "\\")}
		if !strings.HasSuffix(cmd, "\\") {
			flush()
		}
	}
	flush()
	fmt.Printf("imported %d entries\n", n)
}

// zshInit is the shell integration loaded via eval "$(zhist init)".
// It records every command with cwd and exit status, and binds the fzf
// search UI (ctrl-r, bare arrows; ctrl-g toggles global/directory mode).
const zshInit = `
_zhist_cmd=""
_zhist_dir=""

_zhist_preexec() {
	_zhist_cmd="$1"
	_zhist_dir="$PWD"
}

# Returns $ret so later precmd hooks (e.g. the prompt) still see the real
# exit status.
_zhist_precmd() {
	local ret=$?
	if [[ -n "$_zhist_cmd" ]]; then
		local cmd="$_zhist_cmd" dir="$_zhist_dir"
		_zhist_cmd=""
		if [[ "$cmd" != \ * ]]; then
			local first="${cmd%%$'\n'*}"
			first="${first%% *}"
			if (( ! ${+HIST_EXCLUDE} )) || [[ ${HIST_EXCLUDE[(ie)$first]} -gt ${#HIST_EXCLUDE} ]]; then
				print -r -- "$cmd" | zhist add -dir "$dir" -exit $ret
			fi
		fi
	fi
	return $ret
}

autoload -Uz add-zsh-hook
add-zsh-hook preexec _zhist_preexec
# Prepend so we read $? before other precmd hooks (prompt, atuin) clobber it.
precmd_functions=(_zhist_precmd $precmd_functions)

(( ${+FUNC_DESC} )) && FUNC_DESC[fhistory]='Search and edit history with fzf'

_fhistory_select() {
	local qpwd=${(q)PWD}
	# Branches on $FZF_PROMPT so reloads keep whatever mode ctrl-g selected.
	local reload="if [ \"\$FZF_PROMPT\" = \"Dir> \" ]; then zhist list -dir $qpwd; else zhist list; fi"
	local toggle="if [ \"\$FZF_PROMPT\" = \"Dir> \" ]; then echo \"change-prompt(Global> )+reload(zhist list)\"; else echo \"change-prompt(Dir> )+reload(zhist list -dir $qpwd)\"; fi"
	local id
	id=$(zhist list |
		fzf --ansi --height=80% --reverse --prompt="Global> " --no-sort \
			--tabstop=1 --delimiter='\t' --with-nth=2.. \
			--header="ctrl-g: dir/global · ctrl-d: delete entry · ctrl-x: delete all" \
			--bind "tab:accept" \
			--bind "ctrl-g:transform:$toggle" \
			--bind "ctrl-d:execute-silent(zhist delete -id {1})+reload($reload)" \
			--bind "ctrl-x:execute-silent(zhist delete -id {1} -all)+reload($reload)" |
		cut -f1)
	[[ -n "$id" ]] && zhist get -id "$id"
}

fhistory() {
	local selected
	selected=$(_fhistory_select)
	[[ -n "$selected" ]] && print -z "$selected"
}

_fhistory_widget() {
	if [[ -n "$BUFFER" && ("$KEYS" == $'\e[A' || "$KEYS" == $'\eOA') ]]; then
		zle up-line-or-history
	elif [[ -n "$BUFFER" && ("$KEYS" == $'\e[B' || "$KEYS" == $'\eOB') ]]; then
		zle down-line-or-history
	else
		local selected
		selected=$(_fhistory_select)
		if [[ -n "$selected" ]]; then
			BUFFER="$selected"
			CURSOR=${#BUFFER}
		fi
		zle reset-prompt
	fi
}
zle -N _fhistory_widget
bindkey '^R' _fhistory_widget
# Both normal and application cursor sequences; which one the terminal sends
# depends on keypad mode.
bindkey '^[[A' _fhistory_widget
bindkey '^[OA' _fhistory_widget
bindkey '^[[B' _fhistory_widget
bindkey '^[OB' _fhistory_widget
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: zhist <init|add|list|get|delete|import> [flags]")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "init":
		fmt.Print(zshInit)
	case "add":
		cmdAdd(os.Args[2:])
	case "list":
		cmdList(os.Args[2:])
	case "get":
		cmdGet(os.Args[2:])
	case "delete":
		cmdDelete(os.Args[2:])
	case "import":
		cmdImport(os.Args[2:])
	default:
		fmt.Fprintln(os.Stderr, "zhist: unknown command", os.Args[1])
		os.Exit(2)
	}
}
