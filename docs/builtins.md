# Builtins

`help` lists everything with a one-line description, and `help NAME` shows
one. Options follow bash for the bash builtins and fish for the fish ones;
where a builtin exists in both (`set`, `read`, `echo`, `type`, `command`),
it accepts both sets of options.

## Running things

| | |
|---|---|
| `source FILE [ARGS]`, `. FILE` | run a file in the current shell. Reads stdin with no file or `-`, so `cmd \| source` works |
| `eval ARGS` | run the arguments as code |
| `exec CMD` | replace the shell with CMD. With only redirections, applies them to the shell itself |
| `command [-v\|-q] CMD` | run an external command, skipping functions and builtins; `-v` prints where it is |
| `builtin NAME` | run a builtin, skipping functions |
| `type [-t\|-p\|-q\|-a] NAME` | say what NAME is: alias, function, builtin, keyword or file |
| `time CMD` | (a keyword) how long CMD took |
| `exit [N]`, `return [N]`, `break [N]`, `continue [N]` | the usual |
| `true`, `false`, `:` | |

## Variables

| | |
|---|---|
| `set [-lgUfx] NAME VALUES...` | fish's set: `-e` erase, `-q` query, `-a`/`-p` append/prepend, `-S` show, `NAME[2]` index assignment |
| `set -euo pipefail`, `set -- ARGS` | sh's set: options and positional arguments |
| `export NAME=VALUE`, `local`, `readonly`, `declare`, `typeset`, `unset` | bash's |
| `read [-r] [-p PROMPT] [-s] [-n N] [-t SECS] [-a] NAMES...` | read a line; fish's `-l`, `-g`, `-x`, `-P`, `-L`, `-z` work too. At an interactive terminal it uses the line editor |
| `shift [N]`, `getopts` | positional arguments |
| `let EXPR`, `(( EXPR ))` | integer arithmetic |
| `math [-s N] EXPR` | floating point arithmetic |
| `count ITEMS...` | number of arguments (or lines on stdin) |
| `contains [-i] KEY VALUES...` | is KEY one of VALUES |
| `random [START END \| choice ITEMS...]` | random numbers, or a random item |

## Text

| | |
|---|---|
| `echo [-n] [-e] [-s]` | |
| `printf FORMAT ARGS...` | bash's, including `%q` and `-v VAR` |
| `string SUBCOMMAND` | fish's string toolkit: `length sub split join trim replace match upper lower escape unescape repeat pad collect shorten` |
| `path SUBCOMMAND` | fish's path toolkit: `basename dirname extension change-extension filter is mtime normalize resolve sort` |
| `set_color COLOR [--bold ...]` | print color escapes |
| `test`, `[`, `[[ ]]` | conditions |

## Directories

| | |
|---|---|
| `cd [DIR\|-]` | with `CDPATH`, and a "did you mean" for typos |
| `pushd`, `popd`, `dirs` | the directory stack |
| `prevd`, `nextd`, `cdh` | fish's directory history; `cdh` on its own lets you pick from it |
| `pwd [-P]` | |
| `realpath`, `add_path` / `fish_add_path` | |
| `z`, `zi` | jump to frecent directories (after `integrate z` or `integrate zoxide`) |

Typing a directory as a command (`..`, `~/src`, `./build/`) changes to it.

## Functions, aliases, abbreviations

| | |
|---|---|
| `function NAME ...; end`, `NAME() { ...; }` | define a function |
| `functions [-e\|-q\|-c\|-D] NAME` | list, erase, query, copy, or show a function |
| `funcsave NAME` | save a function to `~/.config/ash/functions` |
| `alias NAME=VALUE`, `alias NAME VALUE`, `unalias` | |
| `abbr` | abbreviations, see [config.md](config.md) |
| `emit EVENT ARGS...` | fire an event for `--on-event` handlers |

## Jobs

| | |
|---|---|
| `jobs [-p\|-g\|-c\|-q]` | list jobs |
| `fg [%N]`, `bg [%N]` | resume a stopped job |
| `wait [%N\|PID]` | wait for jobs |
| `kill [-SIG] PID\|%N` | signal processes or jobs; `kill -l` lists signals |
| `disown [%N]` | forget a job so it isn't hung up on exit |
| `trap 'CODE' SIGNALS`, `trap -p` | run code on a signal or `EXIT` |
| `suspend` | stop the shell itself |

## The shell itself

| | |
|---|---|
| `status SUBCOMMAND` | `is-interactive is-login is-command-substitution current-filename current-function current-line-number stack-trace...` |
| `history [search\|delete\|clear\|import\|browse]` | see [editing.md](editing.md) |
| `bind`, `commandline` | key bindings and the command line, see [editing.md](editing.md) |
| `integrate TOOL...` | hook up starship, zoxide, z, fzf, mise, direnv, atuin, or a file manager (see [integrations.md](integrations.md)) |
| `theme [list\|choose NAME]` | highlighting colors |
| `enable_transience`, `disable_transience` | the transient prompt |
| `choose [--multi] ITEMS...` | an interactive picker for scripts: `git branch \| choose` |
| `umask`, `ulimit`, `times`, `hash` | |
| `help [NAME]` | |
