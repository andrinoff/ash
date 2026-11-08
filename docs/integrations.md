# Integrations

Most tools that hook into a shell ship an `init` script for bash, zsh and
fish. Those scripts are written against each shell's internals and run a
process at every startup. ash has the popular ones built in instead:

```sh
integrate starship zoxide fzf mise direnv atuin
```

Each name is checked separately. If a tool isn't installed, `integrate`
skips it quietly (status 1), so the same config works on a machine that
doesn't have everything. `integrate -v ...` tells you what it skipped.

## starship

```sh
integrate starship
```

ash calls `starship prompt` itself, with the exit status, pipe statuses,
command duration, job count, terminal width and vi mode, and sets
`STARSHIP_SHELL` to ash's own path so starship's `custom` modules that
don't name a shell run their commands with ash. The left and right prompts
render in parallel, in the background, so you can start typing before
they're done. Your `continuation_prompt` is used for the second and later
lines of a multi-line command.

For the transient prompt (the full prompt collapses once a command runs),
define the same functions starship's fish integration uses and turn it on:

```sh
function starship_transient_prompt_func
    starship module character
end
function starship_transient_rprompt_func
    starship module time
end
enable_transience
```

In vi mode, `vimcmd_symbol` and friends from your starship config work.

## z (built in)

```sh
integrate z
```

A native port of [z](https://github.com/rupa/z), compatible with the data
file of rupa/z and of the fish plugin jethrokuan/z (`~/.local/share/z/data`
by default, or `$Z_DATA`). If you used z in fish, ash picks up where it left
off, and both shells keep updating the same file.

```sh
z proj          # cd to the best match for "proj"
z gh ash        # matches /home/me/github/ash
z -l proj       # list matches with their scores
z -t proj       # most recent rather than most frequent
z -c foo        # only below the current directory
z -x            # forget the current directory
```

`$Z_EXCLUDE` (a list of regular expressions) keeps directories out.

## zoxide

```sh
integrate zoxide
```

Defines `z` and `zi` the way `zoxide init` does: `z foo` jumps, `z -` goes
back, `zi foo` picks interactively. Directories are added in the
background, so `cd` never waits for zoxide. Use either `z` or `zoxide`, not
both.

## fzf

```sh
integrate fzf
```

| key | |
|---|---|
| `ctrl-r` | search history with fzf (instead of ash's own search) |
| `ctrl-t` | insert files picked with fzf |
| `alt-c` | cd into a directory picked with fzf |

`FZF_DEFAULT_OPTS`, `FZF_CTRL_R_OPTS`, `FZF_CTRL_T_COMMAND`,
`FZF_CTRL_T_OPTS`, `FZF_ALT_C_COMMAND` and `FZF_ALT_C_OPTS` work as with
fzf's own shell integration. To keep ash's own `ctrl-r`, with its scopes,
set `FZF_CTRL_R_COMMAND` to nothing first:

```sh
set -g FZF_CTRL_R_COMMAND ''
integrate fzf
```

## mise

```sh
integrate mise
```

Runs `mise hook-env` before each prompt and after `cd`, so tool versions
and environment variables follow the directory you're in. `mise shell`
works too.

## direnv

```sh
integrate direnv
```

Loads and unloads `.envrc` environments before each prompt, through
`direnv export json`.

## atuin

```sh
integrate atuin
```

Records every command in atuin (with its exit status and duration) and
binds `ctrl-r` to atuin's search.

## File managers and git clients

A program can't change its parent shell's directory, which is why every
TUI file manager ships a shell function to make "quit and stay where you
were" work. ash solves it once for all of them (see "Talking back to the
shell" below), and `integrate` wires up the ones it knows:

| | |
|---|---|
| `integrate yazi` | `y` runs yazi; quit with `q` and you're in yazi's directory |
| `integrate lazygit` | switch repositories inside lazygit, and the shell follows when you quit |
| `integrate nnn` | quitting nnn with `^G` changes to its directory |
| `integrate lf` | `lfcd` runs lf and stays in the directory you quit in |
| `integrate ranger` | `ranger-cd`, the same for ranger |
| `integrate broot` | `br` runs broot; `alt-enter` on a directory changes to it |

If you already have a function with one of those names, ash leaves yours
alone.

## Talking back to the shell

Every command run from an interactive ash gets `$ASH_ACTION_FILE` in its
environment: a path to a file that doesn't exist yet. A program, or a
script, or one of your functions, can write requests into it, and when the
command finishes ash carries them out:

```
/some/directory          a bare directory: cd there
cd DIR                   the same, with shell quoting
export NAME=VALUE        set and export a variable
set NAME VALUE...        set a variable (fish style)
unset NAME               remove a variable
commandline TEXT         put TEXT on your next command line
message TEXT             print TEXT before the next prompt
```

That's the whole protocol. Nothing in the file is run as code: words with
`$`, backticks or `(...)` are ignored, and so is any other command. It
works from key bindings too, so `bind alt-y y` opens yazi on a key and
leaves you where you quit.

A small example, a script that finds a project and drops you in it:

```sh
#!/bin/sh
dir=$(find ~/src -maxdepth 2 -name .git -prune -exec dirname {} \; | fzf) || exit
[ -n "$ASH_ACTION_FILE" ] && echo "$dir" > "$ASH_ACTION_FILE"
```

In other shells that has to be a shell function. With ash it can be a
program in any language, and it still works when it's started from
another program.

## Anything else

Tools that print shell code for you to `eval` usually work, because ash
runs sh syntax:

```sh
eval "$(brew shellenv)"
eval "$(ssh-agent -s)"
source ~/.cargo/env
```

These run at every startup. When one is slow and prints the same thing
each time, `eval "$(cached brew shellenv)"` runs it once and reuses the
output (see [config.md](config.md#keeping-startup-fast)). `ssh-agent`
starts an agent, so leave that one alone.

And fish-flavoured ones often work too, with `| source`, since ash reads
fish syntax. When a tool's `init` script leans on internals ash doesn't
have, a binding and a function usually do the job:

```sh
bind ctrl-g 'commandline -r (git branch --format="%(refname:short)" | fzf)'
```
