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
end redirection comment error autosuggestion preview valid_path selection
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
ash --profile         time each line of the config (see below)
```

`ash` without arguments and a non-terminal stdin reads the whole of stdin
and runs it, which is what starship's custom modules expect when
`STARSHIP_SHELL` points at ash.

## Keeping startup fast

With an empty config, ash shows its first prompt about 3ms after it
starts (measured on an M4 Max). What a config adds to that usually comes
from a line or two that run another program.

### Finding the slow lines

`ash --profile` times each statement of the config and prints a report
once the first prompt is on the screen. Here it is for a config that sets
up Homebrew and Go:

```
$ ash --profile
ash: profile, slowest first
  where             self     total  statement
  config.ash:1   15.45ms   15.45ms  eval "$(brew shellenv)"
  config.ash:2    6.57ms    6.57ms  export GOPATH="$(go env GOPATH)"
  config.ash:6    0.02ms    0.02ms  abbr -a gs git status
  config.ash:5    0.02ms    0.02ms  integrate zoxide
  config.ash:3    0.02ms    0.02ms  add_path $GOPATH/bin
  config.ash:4    0.01ms    0.05ms  if status is-interactive …
  config.ash      0.03ms   22.11ms  (whole file)
ash: config took 22.15ms, first prompt at 24.79ms
```

Two lines take 22ms. Everything else takes a tenth of a millisecond.

`self` is the time of the statement itself, and `total` adds what it ran:
the statements in an `if` block, or a file it sourced. Rows are sorted by
`self`, so the line to fix is at the top, and each file comes after with
its total. Statements inside a top-level `if`, `switch` or `begin` block
get rows of their own; a loop or a function call is one row. A file
sourced more than once keeps one set of rows, with the times added up and
`(3 runs)` after the statement. The report lists the ten slowest
statements and adds up the rest.

Times count from when the process started. On Linux they count from when
ash's own code starts, which leaves out a millisecond or two.

If a terminal starts the shell for you and you can't add a flag, set
`ASH_PROFILE=1` in its environment. ash takes the variable out of its own
environment, so the programs and shells it starts don't print reports
too.

Scripts can be profiled the same way: `ash --profile build.ash` and
`ash --profile -c '...'` print the report when they exit.

### Caching slow commands

`brew shellenv` and `go env GOPATH` print the same thing every time.
`cached` runs a command once, keeps what it printed, and prints that on
later calls:

```sh
eval "$(cached brew shellenv)"
export GOPATH="$(cached go env GOPATH)"
```

With that, the same config takes 0.21ms and the first prompt is up at
2.78ms instead of 24.79ms. A hit costs about 0.05ms, where running the
commands took 15ms and 6ms.

`cached` runs the command again when

- the entry is older than its TTL: 24 hours unless you pass `--ttl`, as
  in `--ttl 1h` or `--ttl 7d`,
- a file given with `--watch` changed, as in
  `cached --dir --watch go.mod go list -m`,
- the program changed, say after `brew upgrade` (ash compares its
  modification time and size), or the function was redefined,
- `$PATH` is different, or with `--dir`, the directory is.

`cached --list` shows what is cached, `cached --clear` forgets all of it,
and `cached --clear brew shellenv` forgets one command. The entries are
files in `~/.cache/ash/cached`.

Only the output and the exit status are kept, so a hit doesn't repeat
anything else the command did. Cache commands that print code for you to
`eval` or `source`. Don't cache ones that start something, like
`ssh-agent -s`, or functions that set variables themselves.

A failure is kept too. If `brew shellenv` fails once, every start gets
that failure back until the TTL runs out or you run
`cached --clear brew shellenv`. Exit statuses from 126 up are not kept:
those come from commands that couldn't run, or that ctrl-c or a signal
ended. Variables other than `$PATH` are not part of the entry either:
`GOOS=linux cached go env GOOS` prints whatever was cached first.

Two more things keep startup short:

- `integrate starship` instead of `starship init fish | source`, and the
  same for the other tools in [integrations.md](integrations.md). They're
  built in, so nothing runs at startup.
- A function you only need now and then can go in
  `~/.config/ash/functions/NAME.ash`, which is read the first time you
  call it.
