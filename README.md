# ash

A shell for people who like fish but keep pasting bash.

ash has the interactive side of fish: highlighting as you type,
autosuggestions from your history, a completion menu, abbreviations, a
`ctrl-r` that actually helps. Underneath, it runs both fish syntax and
POSIX sh with the bash extensions people use, so your fish config works
and so does the snippet from somebody's README:

```sh
# fish
for f in *.jpg; magick $f (path change-extension png $f); end

# bash, same shell, same file
for f in *.jpg; do magick "$f" "${f%.jpg}.png"; done
```

## Install

You need Go 1.27 or newer.

```sh
go install github.com/andrinoff/ash@latest
```

or from a checkout:

```sh
go build -o ash .
```

Then run `ash`. To try it without touching your setup, start it from your
current shell; `exit` takes you back.

## What's in it

- **Both languages.** fish's `set`, `function ... end`, `and`/`or`,
  `string`, `math`, `$argv[2..-1]`, alongside `export`, `$(...)`, `&&`,
  `[[ ]]`, `${var:-default}`, heredocs, `case`, `name() { ... }`. Variables
  are lists, so `$file` never splits on spaces. The details are in
  [docs/language.md](docs/language.md).
- **fish's editor**, plus a few things fish doesn't do: vi mode with text
  objects, `!!` and `!$`, a fuzzy `ctrl-r`, `alt-.` for the last argument,
  copy and paste with `ctrl-x` / `ctrl-v`, and `alt-w`, which explains
  the line you're typing flag by flag before you run it. See
  [docs/editing.md](docs/editing.md).
- **Your tools, built in.** `integrate starship zoxide fzf mise direnv
  atuin` replaces the `init | source` lines. Starship gets a transient
  prompt and renders its left and right prompts in parallel while you're
  already typing. There's a native `z` that shares its data file with
  fish's z plugin. See [docs/integrations.md](docs/integrations.md).
- **Programs can talk back.** Every command gets `$ASH_ACTION_FILE`. A
  program that writes `cd /some/dir` or `export NAME=value` there changes
  the shell when it exits, which is how `integrate yazi lazygit` leaves
  you in the directory you quit them in. Your own scripts can do it too,
  in any language. See
  [docs/integrations.md](docs/integrations.md#talking-back-to-the-shell).
- **fish's completions.** If fish is installed, ash reads its completion
  files, so `git <tab>`, `brew <tab>` and a thousand others work out of
  the box. Go programs built with cobra (gh, kubectl, helm...) complete
  themselves, with nothing to set up.
- **Plugins in any language.** A plugin is a program that runs next to the
  shell and talks to it in JSON lines: it hears about prompts, commands
  and `cd`, and can set prompt segments and variables, change directory,
  fill in the command line, and add commands and completions. The shell
  never waits for it. One of the examples lets file managers and editors
  drive the shell through a socket. See [docs/plugins.md](docs/plugins.md).
- **A terminal citizen.** Proper job control (`ctrl-z`, `fg`, `bg`), window
  titles, OSC 7 so new tabs open in the same directory, OSC 133 so your
  terminal can jump between prompts, a `⏎` marker when output didn't end
  in a newline.
- **Pickers where they make sense.** `choose`, the directory history and
  the history browser are [Bubble Tea](https://github.com/charmbracelet/bubbletea)
  programs. The line editor itself is hand-written, because it has to
  start instantly and stay out of the way of job control.

## Configuration

`~/.config/ash/config.ash` is read by interactive shells. A small one:

```sh
add_path ~/bin ~/go/bin
export EDITOR=nvim

if status is-interactive
    integrate starship zoxide fzf
    alias ll 'eza -l'
    abbr -a gs git status
    bind ctrl-g 'lazygit; commandline -f repaint'
end
```

There's more in [docs/config.md](docs/config.md), and if you're moving from
fish, [docs/from-fish.md](docs/from-fish.md) has a table of what to
change (not much).

## Making it your shell

```sh
command -v ash | sudo tee -a /etc/shells
chsh -s "$(command -v ash)"
```

Or leave your login shell alone and point your terminal at ash: in
Ghostty, `command = /path/to/ash`.

## How ready is it

It's young. The test suite covers the parser, the interpreter and the
editor, and it runs real fish and bash configs, but expect rough edges.
Known gaps:

- No associative arrays (`declare -A`), `select`, `coproc` or extended
  globs (`!(...)`).
- No `funced` or fish's web configuration.
- Universal variables aren't synced live between running shells.
- Windows isn't supported. Linux and macOS are.

Bug reports with the smallest input that shows the problem are the most
useful kind.

## Docs

- [docs/language.md](docs/language.md): syntax, variables, quoting, expansion
- [docs/editing.md](docs/editing.md): keys, completion, history, vi mode
- [docs/config.md](docs/config.md): config files, prompt, themes, abbreviations
- [docs/integrations.md](docs/integrations.md): starship, zoxide, fzf, mise, direnv, atuin, file managers, `$ASH_ACTION_FILE`
- [docs/plugins.md](docs/plugins.md): plugins in any language, and the protocol they speak
- [docs/builtins.md](docs/builtins.md): every builtin
- [docs/from-fish.md](docs/from-fish.md): moving a fish setup over

## License

MIT
