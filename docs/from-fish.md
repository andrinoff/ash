# Coming from fish

Most of what you know carries over. ash runs fish syntax, reads fish
completion files and themes, and understands the fish builtins that configs
lean on (`set`, `string`, `math`, `contains`, `count`, `status`, `argparse`,
`path`, `abbr`, `bind`, `commandline`, `functions`, `set_color`, `random`,
`fish_add_path`...).

## Moving a config over

1. Copy `~/.config/fish/config.fish` to `~/.config/ash/config.ash`, and the
   functions you wrote yourself from `~/.config/fish/functions/` to
   `~/.config/ash/functions/` (rename `.fish` to `.ash`, or don't: both
   load).
2. Replace the `init | source` lines:

   | fish | ash |
   |---|---|
   | `starship init fish \| source` | `integrate starship` |
   | `zoxide init fish \| source` | `integrate zoxide` |
   | `fzf --fish \| source` | `integrate fzf` |
   | `mise activate fish \| source` | `integrate mise` |
   | `direnv hook fish \| source` | `integrate direnv` |
   | `atuin init fish \| source` | `integrate atuin` |
   | the `z` plugin (jethrokuan/z) | `integrate z` (reads and writes the same data file) |
   | `fish_config theme choose X` | `theme choose X` (fish's theme files work) |

   The `init` scripts would mostly work too, but they're written against
   fish internals and run a process at every startup. `integrate` is
   built in.
3. Drop plugin managers (fisher, oh-my-fish). Plugins are usually a few
   functions; copy the ones you use into `functions/`.
4. Start `ash` from your fish session and see what complains. `ash -N`
   starts without the config if something goes wrong.

Your history comes along: the first time ash starts it imports
`~/.local/share/fish/fish_history`.

## Things that behave differently

- `$(cmd)` splits its output on whitespace, like bash. Use `(cmd)` for
  fish's split-on-newlines.
- Command substitution runs in a subshell, so `(set -g x 1)` doesn't set
  `x` in your shell. Nobody does this on purpose, but now you know.
- `set -e` alone turns on exit-on-error, and `set -x` alone turns on
  tracing, as in sh. With a variable name they mean what fish means.
- A glob that matches nothing is passed through unchanged instead of being
  an error (except in `for` and `set`, where it produces nothing).
- In the interactive shell and in `.ash` files, single quotes are POSIX:
  `\'` doesn't escape a quote inside them. Files ending in `.fish` use
  fish's rules.
- Universal variables are stored in `~/.config/ash/variables` and are not
  shared live with running shells.
- `funced` doesn't exist yet. `funcsave` does.
- There's no `fish_config` web UI.

## Things that are the same, on purpose

The key bindings (two of them do a little more, see below), the
autosuggestions, the way the up arrow searches history, `alt-←`/`alt-→` for directory history, `prevd`/`nextd`/`cdh`,
abbreviations, `--on-variable` and `--on-event` handlers, `$argv`,
`$status` and `$pipestatus`, `$CMD_DURATION`, the `fish_greeting`,
`fish_prompt` and `fish_title` functions, and the `fish_color_*` and
`fish_pager_color_*` variables.

## What you get on top

Everything sh: `&&`, `||`, `$(( ))`, `[[ ]]`, `${var:-default}` and the
rest of parameter expansion, here-documents, `<(...)`, `case`, `name() {
}`, `local`, `export X=1`, `trap`, `getopts`, arrays with `${arr[@]}`.
So the one-liner from a README works without translation.

And a few things fish doesn't have:

- `alt-w` explains the command line, option by option, instead of only
  showing `whatis` for the command.
- `ctrl-z` on an empty line brings back the job you just stopped. With
  text on the line it's still undo.
- Programs built with cobra (gh, kubectl, helm) complete without a
  completion script, and quitting yazi or lazygit leaves you where they
  were (`integrate yazi lazygit`).
- `history browse` shows where and when each command ran and whether it
  failed, and `cdh` opens a picker.
- `clip` copies a pipe to the clipboard, through the terminal when you're
  on SSH.
