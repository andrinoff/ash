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
fzf's own shell integration.

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

## Anything else

Tools that print shell code for you to `eval` usually work, because ash
runs sh syntax:

```sh
eval "$(brew shellenv)"
eval "$(ssh-agent -s)"
source ~/.cargo/env
```

And fish-flavoured ones often work too, with `| source`, since ash reads
fish syntax. When a tool's `init` script leans on internals ash doesn't
have, a binding and a function usually do the job:

```sh
bind ctrl-g 'commandline -r (git branch --format="%(refname:short)" | fzf)'
```
