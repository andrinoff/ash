# The command line

ash's line editor works like fish's, with a few things borrowed from zsh
and bash. Everything below is the default; all of it can be rebound with
`bind`.

## As you type

The line is highlighted while you type. Commands that don't exist show up
as errors before you press enter, and arguments that are existing files
are underlined.

The grey text after the cursor is an autosuggestion: the most recent
command from your history that starts with what you typed. ash prefers
commands you ran in the current directory, and skips suggestions that
would `cd` into a directory that's gone.

| key | |
|---|---|
| `→`, `ctrl-f`, `end`, `ctrl-e` | accept the whole suggestion |
| `alt-→`, `alt-f` | accept the next word of it |
| `escape` | hide it for now |

## What a word expands to

When the cursor is on a word the shell would expand, a dim line under the
command line shows what it turns into. You see it before you press enter,
not after:

```
❯ rm build/*.o
12 matches: build/a.o build/b.o build/c.o build/d.o …
```

| you type | the line shows |
|---|---|
| `ls *.go` | `3 matches: lexer.go main.go parser.go` |
| `ls *.rs` | `no matches` |
| `echo $PATH` | `5 values: /opt/homebrew/bin /usr/local/bin /usr/bin …` |
| `cd ~/src` | `/Users/you/src` |
| `cp app.conf{,.bak}` | `2 values: app.conf app.conf.bak` |
| `echo $((60 * 60 * 24))` | `86400` |
| `echo $nope` | `(unset)` |
| `sudo !!` | the previous command |

That covers globs (`**` too), variables in all their forms (`$x`,
`${x:-default}`, `${x%.*}`, `$list[2]`), `~` and `~user`, braces,
arithmetic, and `!!` / `!$`. Plain words show nothing, and neither does
quoted text like `'*.go'`. You don't have to finish a word first: `"$HOME/`
already shows your home directory with a slash after it.

The preview never runs or changes anything:

- Command substitution (`$(...)`, `(...)`, backticks) and process
  substitution (`<(...)`) show nothing. Words inside them do: with the
  cursor on `*.go` in `echo $(ls *.go)` you see the matches.
- `${x:=default}` shows the default without setting `x`, and `$((i++))`
  shows the value without changing `i`.

It stays out of the way. It's worked out in the background once you stop
typing for a moment, so typing never waits for it. A glob stops after 200
matches or a few milliseconds and says `200+ matches`, braces stop after 50
values, and the line is hidden while the completion menu or the history
search is open.

To turn it off, put `set -g ash_preview 0` in `config.ash`. Its color is
the `preview` role (`set -g ash_color_preview brblack --italics`); without
one it uses the autosuggestion color.

## Completion

`tab` completes the word under the cursor. If there's one match it's
inserted; if there are several, the common part is inserted and a second
`tab` opens the menu. In the menu, `tab` and the arrow keys move, `enter`
keeps the selection, and `escape` puts back what you had.

Matching is forgiving, in this order: exact prefix, prefix ignoring case,
substring, then the letters in order (`gco` finds `git-checkout-index`).
Only the best kind of match is shown.

Completions for options and subcommands come from `complete` definitions.
ash reads fish's completion files (fish ships around a thousand of them),
so if fish is installed you get completions for git, brew, docker, cargo
and friends without doing anything. Your own go in
`~/.config/ash/completions/COMMAND.ash`.

Programs written in Go with [cobra](https://github.com/spf13/cobra) (gh,
kubectl, helm, hugo, and a long tail of smaller tools) know how to
complete their own arguments, and ash asks them directly. There's no
`prog completion fish | source` step: when a command has no completion
file, ash peeks at the module list Go stores in the binary, and if cobra
is in it, Tab works. Nothing gets run just to find out.

## History

| key | |
|---|---|
| `↑`, `ctrl-p` | previous command containing what you typed |
| `↓`, `ctrl-n` | next one |
| `alt-↑`, `alt-↓` | cycle the word under the cursor through words from history |
| `alt-.` | insert the last argument of the previous command (repeat to go further back) |
| `ctrl-r` | search history interactively |

In the `ctrl-r` search, type to narrow the list (it matches letters in
order, so `gpu` finds `git push -u origin main`), use the arrows or
`ctrl-p`/`ctrl-n` to move, `enter` or `tab` to put the command on the line
for editing, and `escape` to cancel.

ash saves every command with the directory it ran in and its exit status,
so the search can narrow to what you ran here. Press `ctrl-r` again to go
to the next scope. The header shows which one you're in:

| header | lists |
|---|---|
| `history:` | everything |
| `history [this directory]:` | commands run in the current directory |
| `history [this repository]:` | commands run anywhere in the current git repository |
| `history [failed]:` | commands that failed |

On a narrow screen the header shows just the scope's name, like
`[repository]:`.

After `failed` it starts over. Outside a repository the repository scope is
skipped. ash finds the repository by looking for `.git` in the directory
and its parents, without running git. What you typed stays when the scope
changes. Only the last run of each command is kept, so a command belongs to
the directory you last ran it in, and counts as failed if it failed that
time. Commands imported from another shell have no directory or status, so
they only show up under everything.

To start the search in another scope, set `ash_history_scope`:

```sh
set -U ash_history_scope directory    # or repository, failed
```

Outside the search, `history --here` lists the commands run in the current
directory and `history --failed` the ones that failed. They combine with
each other and with a search: `history --here --failed make` shows the make
commands that failed here.

`history browse` is the same search with more room: every command comes
with the directory it ran in and how long ago, and the ones that failed
are marked with `✗`. Whatever you pick is waiting on your next command
line. It's not bound to anything by default; if you like it,

```sh
bind alt-r 'history browse'
```

and from the key it starts out searching for what you'd typed.

History lives in `~/.local/share/ash/history`. The first time ash starts it
imports your fish history, or zsh's, or bash's, whichever it finds first.
`history import fish` (or `zsh`, `bash`) does it again later. Commands that
start with a space are not saved.

`!!` and `!$` expand to the previous command and its last argument when you
press space or enter, so you see what's about to run.

Commands that look like they contain a credential (an API key or token in
a known format, a `password=...`, a URL with a password in it) are not
saved. `set -g ash_history_ignore 'regex' ...` adds your own patterns, and
`set -g ash_history_secrets 0` turns the built-in ones off.

## Typos

When a command isn't found and something close is, ash says so:

```
❯ gti status
ash: unknown command: gti (did you mean git?)
❯ git status        ← offered on the empty line; press → to take it
```

The corrected line shows up as the autosuggestion on the next prompt, so
fixing it is one key. The same happens for `cd` into a directory that
doesn't exist when a close one does.

## Moving and editing

| key | |
|---|---|
| `ctrl-a`, `home` / `ctrl-e`, `end` | start / end of line |
| `ctrl-b`, `←` / `ctrl-f`, `→` | a character back / forward |
| `alt-b`, `ctrl-←` / `alt-f`, `ctrl-→` | a word back / forward |
| `alt-←` / `alt-→` | on an empty line: previous / next directory (`cdh` shows them all) |
| `ctrl-w` | delete back to the previous `/`, `=` or space (path component) |
| `alt-backspace` | delete the previous word |
| `alt-d` | delete the next word |
| `ctrl-u` / `ctrl-k` | delete to the start / end of the line |
| `ctrl-y` / `alt-y` | paste what was deleted / cycle through older deletions |
| `ctrl-t` / `alt-t` | swap characters / words |
| `alt-u` / `alt-c` | uppercase / capitalize the word |
| `ctrl-z`, `ctrl-_` / `alt-/` | undo / redo (`ctrl-z` on an empty line brings back the job you last stopped, so it flips between vim and the shell) |
| `alt-enter` | insert a newline |
| `ctrl-x` / `ctrl-v` | copy the line to the clipboard / paste from it (over SSH the copy goes through the terminal, OSC 52, so it lands on your local clipboard; `clip` does the same for a pipe: `git diff \| clip`) |
| `alt-s` | add or remove `sudo` at the front (on an empty line, the last command with sudo) |
| `alt-e`, `alt-v`, `ctrl-x ctrl-e` | edit the line in `$VISUAL` / `$EDITOR` |
| `alt-h` | open the man page for the command under the cursor |
| `alt-w` | explain the line: what the command is and what each option does (see below) |
| `alt-l` | list the directory under the cursor |
| `alt-p` | append `\| less` (or `$PAGER`) |
| `alt-#` | comment the line out and keep it in history |
| `ctrl-l` | clear the screen |
| `ctrl-c` | cancel the line |
| `ctrl-d` | delete a character, or exit on an empty line |
| `ctrl-q` | insert the next key literally |

`alt-w` is for the command you found in a README and don't quite trust.
It shows, under the line, what each part does:

```
❯ tar -czf backup.tgz --exclude .git src
tar           manipulate tape archives
  -c          Create archive
  -z          Filter through gzip
  -f          Archive file
  backup.tgz  argument of -f
  --exclude   Exclude file
  .git        argument of --exclude
```

The descriptions come from the man page's summary line and from the
completion definitions, so anything that completes well explains well,
including git subcommands and cobra programs. Nothing is run. `explain`
does the same from a script or the prompt: `explain git rebase -i
HEAD~3`.

Pressing enter on an unfinished line (an open quote, a `for` without its
`end`, a trailing `|`) starts a new line instead of running it. Pasted text
is inserted as is, newlines included, and never runs by itself.

## vi mode

`set -o vi` (or `fish_vi_key_bindings`) switches to vi bindings; `set -o
emacs` switches back. You start in insert mode. `escape` goes to normal
mode, where the usual motions (`h l w b e W B E 0 ^ $ f F t T ; ,`, counts),
operators (`d c y` with motions and text objects like `iw`, `a"`, `i(`),
`x X s S D C p P r R u ~ ctrl-r`, `v` for visual mode, and `j`/`k` for
history work. The cursor is a bar in insert mode and a block in normal
mode. If you use starship, the `vimcmd_symbol` from your config shows up
in normal mode.

## Binding keys

```sh
bind ctrl-o 'tmux-sessionizer; commandline -f repaint'
bind alt-g 'git status'
bind ctrl-x,ctrl-g 'commandline -i (git branch --show-current)'
bind -M insert ctrl-p history-search-backward     # vi insert mode
bind -e ctrl-o                                    # remove it again
```

Key names follow fish 4: `ctrl-x`, `alt-x`, `shift-tab`, `up`, `f5`,
`ctrl-alt-left`, with `,` between the keys of a sequence. The older `\cx` and
`\e[A` spellings are accepted too. The command is either the name of an
editor function (`bind -f` lists them) or any shell code. Shell code runs
with the terminal in normal mode, so full screen programs like fzf or tmux
work from a binding.

`commandline` reads and changes the line from inside a binding:
`commandline` prints it, `commandline -r text` replaces it, `-i` inserts at
the cursor, `-t` works on the current token, `-C` gets or sets the cursor,
and `-f name` runs editor functions after the binding returns.

## The prompt

ash draws the prompt while you're already typing. If the prompt takes a
while (starship in a huge git repository, say) you can type immediately and
the prompt fills in when it's ready. See [config.md](config.md) for how to
pick a prompt and [integrations.md](integrations.md) for starship.

When the previous command's output didn't end with a newline, ash marks the
spot with `⏎` and starts the prompt on a fresh line instead of gluing it to
the output.
