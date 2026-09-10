# Configuration

ash reads its configuration only in interactive shells, so scripts and
`ash -c` start fast and don't depend on your setup.

```
~/.config/ash/
├── config.ash          read at startup
├── conf.d/*.ash        read before config.ash, in alphabetical order
├── functions/NAME.ash  loaded the first time NAME is called
├── completions/CMD.ash loaded the first time you complete CMD
├── themes/NAME.theme   highlighting themes for `theme choose`
└── variables           universal variables (written by `set -U`)
```

`$XDG_CONFIG_HOME` moves all of this. History goes in
`~/.local/share/ash/history` and caches in `~/.cache/ash`.

`ash -N` skips the config, which is handy when you broke it.

## A starting point

```sh
# ~/.config/ash/config.ash

add_path ~/bin ~/go/bin ~/.cargo/bin
export EDITOR=nvim

if status is-interactive
    integrate starship zoxide fzf

    alias ll 'ls -l'
    abbr -a gs git status
    abbr -a gco git checkout

    bind ctrl-g 'lazygit; commandline -f repaint'
end
```

The `status is-interactive` guard isn't strictly needed (config is only read
by interactive shells), but it keeps the file valid if you source it from a
script, and it's what fish configs look like, so pasted snippets fit.

Since ash reads fish syntax, most of a fish `config.fish` works as is.
Lines of the form `something init fish | source` are better replaced with
`integrate something` (see [integrations.md](integrations.md)), which is
faster and doesn't need fish's internals. [from-fish.md](from-fish.md) has
the details.

## The prompt

ash picks the prompt like this:

1. If you defined an `ash_prompt` function (or `fish_prompt`), its output is
   the prompt. `ash_right_prompt` / `fish_right_prompt` go on the right.
2. Otherwise, if you ran `integrate starship`, starship draws it.
3. Otherwise you get the built-in prompt: the directory, the git branch,
   how long the last command took if it was slow, and a `❯` that turns red
   when the last command failed.

Prompts are rendered in the background while you type, and the left and
right starship prompts are rendered in parallel.

### Transient prompt

`enable_transience` replaces the full prompt with a short one once you
press enter, which keeps scrollback readable with a multi-line prompt. The
short version is the output of `ash_transient_prompt` (or starship's
`starship_transient_prompt_func`, so an existing fish config just works),
and `ash_transient_rprompt` / `starship_transient_rprompt_func` on the
right:

```sh
integrate starship
function starship_transient_prompt_func
    starship module character
end
function starship_transient_rprompt_func
    starship module time
end
enable_transience
```

### Other things the prompt loop does

- Sets the window title to the directory, or the running command. Define
  `ash_title` (or `fish_title`) to change it.
- Reports the directory to the terminal (OSC 7) so new tabs open in the
  same place.
- Marks prompts and command output (OSC 133), which lets terminals like
  Ghostty, kitty, WezTerm and iTerm2 jump between prompts and select a
  command's output. In kitty you can also click anywhere in the command
  you're typing to move the cursor there.

## Highlighting colors

Colors come from a theme. `theme choose NAME` loads
`~/.config/ash/themes/NAME.theme`, or the same file from
`~/.config/fish/themes`, so fish themes work unchanged. `theme list` shows
what's available and `theme choose default` goes back to the built-in one.

A theme file is lines of `fish_color_ROLE value`:

```
fish_color_command ffffff --bold
fish_color_param c8c8c8
fish_color_error red --underline
fish_color_autosuggestion 5e5e5e
fish_pager_color_description 6b6b6b --italics
```

You can also set a single color directly with a variable, which wins over
the theme: `set -g ash_color_command blue --bold` (`fish_color_command`
works too).

Roles: `normal command keyword param option quote escape variable operator
end redirection comment error autosuggestion valid_path selection
search_match cancel`, plus the pager's `prefix completion description
progress selected_background selected_completion selected_description`.

Values are a color (`red`, `brblue`, `ff8800`, `f80`) and any of `--bold`,
`--dim`, `--italics`, `--underline`, `--reverse`, `--background=COLOR`.

## Abbreviations

```sh
abbr -a gs git status
abbr -a --position anywhere -- -h --help
abbr -a --set-cursor gcm 'git commit -m "%"'
abbr -a --regex '^\.\.+$' --function multicd dotdot
abbr                 # list them
abbr -e gs           # erase one
```

They expand when you press space or enter in command position (or
anywhere, with `--position anywhere`). `--function` makes the expansion the
output of a function, which receives the typed word as `$argv[1]`.

## Universal variables

`set -U name value` saves a variable for every future session. They're kept
in `~/.config/ash/variables`, and `set -Ue name` removes one. Unlike fish,
a change isn't pushed into shells that are already running.

## Login shell

To make ash your login shell:

```sh
command -v ash | sudo tee -a /etc/shells
chsh -s "$(command -v ash)"
```

As a login shell on macOS, ash builds `PATH` from `/etc/paths` and
`/etc/paths.d` the way `path_helper` does, since nothing else will. It
doesn't read `~/.profile` or `/etc/profile`.

If you'd rather not change your login shell, point your terminal at ash
instead. In Ghostty that's `command = /path/to/ash` in its config, in kitty
`shell /path/to/ash`.

## Startup options

```
ash                   interactive shell (or reads commands from stdin when it isn't a terminal)
ash script.ash args   run a script
ash -c 'cmd' [name args...]
ash -l                login shell
ash -N                skip the config
ash -C 'cmd'          run cmd before the config
ash -n script.ash     only check the syntax
```

`ash` without arguments and a non-terminal stdin reads the whole of stdin
and runs it, which is what starship's custom modules expect when
`STARSHIP_SHELL` points at ash.
