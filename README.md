# zhist

A shell history tool for zsh, written in Go. It records every command with its
working directory, exit status, and duration. It replaces the zsh history file
as the persistent store.

https://github.com/user-attachments/assets/a7e1226b-e695-4505-ad96-a5da6dfb3afd

## Purpose

Native zsh history stores a command and a timestamp, nothing else. zhist stores
context: where you ran a command, whether it failed, and how long it took. The
fzf picker uses that context. Failed commands show in red, and each entry shows
its duration. One key toggles between global history and the current
directory's history.

## Install

```sh
go install github.com/overflowy/zhist@latest
```

Requires [fzf](https://github.com/junegunn/fzf) 0.45 or newer for the picker.

## Setup

Add to `.zshrc`:

```zsh
eval "$(zhist init)"
```

Import existing history once:

```sh
zhist import ~/.zsh_history
```

Imported entries have no directory, exit status, or duration. They show a
blank directory and never render red.

## Keys

| Key                | Action                                            |
| ------------------ | ------------------------------------------------- |
| `ctrl-r`           | Open the history picker                           |
| `up` / `down`      | Open the picker on an empty line; otherwise step line history |
| `ctrl-g`           | Toggle global / current-directory history         |
| `ctrl-d`           | Delete the selected entry                         |
| `ctrl-x`           | Delete all entries with the same command          |
| `tab`              | Accept and leave the command on the line          |

Pass `-no-arrow-binds` to `zhist init` to keep the default up/down behavior.
Only `ctrl-r` opens the picker then.

## Ignoring commands

Define a `HIST_EXCLUDE` array in `.zshrc` to keep commands out of the store.
zhist skips a command when its first word is in the array.

```zsh
HIST_EXCLUDE=(cd ls clear pwd exit)
```

- Matching is exact and case-sensitive, on the first word only. `ls` skips
  `ls -la` but not `lsd`.
- For multiline commands the first word of the first line decides.
- The hook reads the array at record time. Change it in a running shell and
  the change applies to the next command.
- A leading space also skips recording, for one-off exclusions.

## Recommended zsh history settings

zhist owns persistence. Keep native history in memory only, for line stepping
and `!` expansion within a session.

```zsh
unset HISTFILE  # macOS /etc/zshrc sets it; unset stops zsh reading or writing the file
HISTSIZE=100000 # In-memory events for up-arrow and ! expansion
SAVEHIST=0      # Never write a history file

setopt BANG_HIST            # Treat the '!' character specially during expansion
setopt HIST_IGNORE_DUPS     # Don't record an entry that was just recorded again
setopt HIST_IGNORE_ALL_DUPS # Delete old recorded entry if new entry is a duplicate
setopt HIST_FIND_NO_DUPS    # Do not display a line previously found
setopt HIST_IGNORE_SPACE    # Don't record an entry starting with a space
setopt HIST_REDUCE_BLANKS   # Remove superfluous blanks before recording entry
setopt HIST_VERIFY          # Do not execute immediately upon history expansion
```

Do not set `SHARE_HISTORY`, `INC_APPEND_HISTORY`, or `EXTENDED_HISTORY`. They
only affect the history file, which zhist replaces.

Compatibility notes:

- Commands with a leading space are not recorded. This matches `HIST_IGNORE_SPACE`.
- `eval "$(zhist init)"` must run after plugins that bind `ctrl-r` or the
  arrow keys (atuin, zsh-history-substring-search, prompt pickers). The last
  bind wins.
- The record hook prepends itself to `precmd_functions` and passes `$?`
  through. Prompts that read the exit status in their own precmd keep working.

## CLI

```
zhist init [-no-arrow-binds]  Print the zsh integration script
zhist add -dir D -exit N [-ms N]  Append an entry; command read from stdin
zhist list [-dir D]        Print entries for fzf, newest first, deduplicated
zhist get -id ID           Print the full command for an entry
zhist delete -id ID [-all] Delete an entry, or all entries with its command
zhist import FILE          Import a zsh EXTENDED_HISTORY file
```
