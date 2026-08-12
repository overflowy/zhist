// zhist is a shell history store with per-entry directory and exit status.
// Entries are appended as JSON lines. fzf provides the UI, wired up by the
// zsh integration that `zhist init` emits.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

func dataPath() string {
	if p := os.Getenv("ZHIST_FILE"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "zhist", "history.jsonl")
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
	if err := newStore(dataPath()).Append([]Entry{{T: t, D: *dir, X: *exit, C: cmd}}); err != nil {
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
	rows, err := newStore(dataPath()).List()
	if err != nil {
		fmt.Fprintln(os.Stderr, "zhist:", err)
		os.Exit(1)
	}
	// SHARE_HISTORY-imported files interleave sessions, so file order is not
	// time order.
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].T < rows[j].T })
	now := time.Now().Unix()
	w := bufio.NewWriter(os.Stdout)
	defer w.Flush()
	seen := make(map[string]bool, len(rows))
	// Newest first, dedup by command keeping the most recent occurrence.
	for _, row := range slices.Backward(rows) {
		e := row.Entry
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
			row.ID,
			cBlue, relTime(e.T, now), cReset,
			col, disp, cReset)
	}
}

func cmdGet(args []string) {
	fs := flag.NewFlagSet("get", flag.ExitOnError)
	id := fs.String("id", "", "entry id")
	fs.Parse(args)
	entry, err := newStore(dataPath()).Get(*id)
	if err == errEntryNotFound {
		os.Exit(1)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "zhist:", err)
		os.Exit(1)
	}
	fmt.Println(entry.C)
}

func cmdDelete(args []string) {
	fs := flag.NewFlagSet("delete", flag.ExitOnError)
	id := fs.String("id", "", "entry id")
	all := fs.Bool("all", false, "delete every entry with the same command")
	fs.Parse(args)
	err := newStore(dataPath()).Delete(*id, *all)
	if err != nil {
		fmt.Fprintln(os.Stderr, "zhist:", err)
		os.Exit(1)
	}
}

var zshHistLine = regexp.MustCompile(`^: (\d+):(\d+);(.*)$`)

// importHistory reads a zsh EXTENDED_HISTORY file. Directory and exit status
// are unknown for imported entries.
func importHistory(source string, store Store) (int, error) {
	f, err := os.Open(source)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	var entries []Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), maxJSONLineSize)
	var cur *Entry
	flush := func() error {
		if cur == nil {
			return nil
		}
		entries = append(entries, *cur)
		cur = nil
		return nil
	}
	for sc.Scan() {
		line := sc.Text()
		if cur != nil {
			// Continuation of a multiline entry.
			cur.C += "\n" + strings.TrimSuffix(line, "\\")
			if !strings.HasSuffix(line, "\\") {
				if err := flush(); err != nil {
					return 0, err
				}
			}
			continue
		}
		m := zshHistLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		t, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil {
			return 0, err
		}
		cmd := m[3]
		if cmd == "" {
			continue
		}
		cur = &Entry{T: t, X: -1, C: strings.TrimSuffix(cmd, "\\")}
		if !strings.HasSuffix(cmd, "\\") {
			if err := flush(); err != nil {
				return 0, err
			}
		}
	}
	if err := sc.Err(); err != nil {
		return 0, err
	}
	if cur != nil {
		return 0, fmt.Errorf("unexpected end of file in multiline entry")
	}
	if err := store.Append(entries); err != nil {
		return 0, err
	}
	return len(entries), nil
}

func cmdImport(args []string) {
	fs := flag.NewFlagSet("import", flag.ExitOnError)
	fs.Parse(args)
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: zhist import <zsh_history_file>")
		os.Exit(2)
	}
	n, err := importHistory(fs.Arg(0), newStore(dataPath()))
	if err != nil {
		fmt.Fprintln(os.Stderr, "zhist:", err)
		os.Exit(1)
	}
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

_fhistory_select() {
	local qpwd=${(q)PWD}
	# Branches on $FZF_PROMPT so reloads keep whatever mode ctrl-g selected.
	local reload="if [ \"\$FZF_PROMPT\" = \"Dir> \" ]; then zhist list -dir $qpwd; else zhist list; fi"
	local toggle="if [ \"\$FZF_PROMPT\" = \"Dir> \" ]; then echo \"change-prompt(Global> )+reload(zhist list)\"; else echo \"change-prompt(Dir> )+reload(zhist list -dir $qpwd)\"; fi"
	# Preview visibility persists across sessions via a flag file.
	local pstate="${XDG_STATE_HOME:-$HOME/.local/state}/zhist/preview-hidden"
	mkdir -p "${pstate:h}"
	local qstate=${(q)pstate}
	local pwin="down,6,wrap"
	[[ -f "$pstate" ]] && pwin="down,6,wrap,hidden"
	local id
	# Clear the user's fzf defaults so zhist renders the same on every machine.
	id=$(zhist list |
		FZF_DEFAULT_OPTS= FZF_DEFAULT_OPTS_FILE= \
		fzf --ansi --reverse --prompt="Global> " --tiebreak=index \
			--tabstop=1 --delimiter='\t' --with-nth=2.. \
			--preview="zhist get -id {1}" --preview-window=$pwin \
			--header="ctrl-g: dir/global · ctrl-d: delete entry · ctrl-x: delete all · ctrl-/: preview" \
			--bind "tab:accept" \
			--bind "ctrl-/:toggle-preview+execute-silent(if [ -f $qstate ]; then rm -f $qstate; else touch $qstate; fi)" \
			--bind "ctrl-g:transform:$toggle" \
			--bind "ctrl-d:execute-silent(zhist delete -id {1})+reload($reload)" \
			--bind "ctrl-x:execute-silent(zhist delete -id {1} -all)+reload($reload)" |
		cut -f1)
	[[ -n "$id" ]] && zhist get -id "$id"
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
