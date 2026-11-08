# Plugins

A fish or zsh plugin is shell code: it runs inside the shell, in the
shell's language, and it slows down every prompt it hooks. A nushell plugin
can only add commands. An ash plugin is a separate program, written in
whatever language you like, that keeps running next to the shell. ash tells
it what happens (a prompt is drawn, a command runs, the directory changes),
and the plugin answers whenever it's ready: it can set variables and prompt
segments, change the directory, put text on the command line, print a
message, run shell code (so it can bring functions, abbreviations and key
bindings along), and add commands and completions.

The two talk in JSON, one object per line, over the plugin's stdin and
stdout. The shell never waits for a plugin, except when you run one of its
commands.

## Loading plugins

```sh
plugin load git ~/src/ash/examples/plugins/git-status.py
plugin list
plugin unload git
```

| | |
|---|---|
| `plugin load NAME COMMAND [ARGS...]` | start a plugin. `NAME` is what you call it from then on |
| `plugin load -w NAME COMMAND...` | the same, and wait (up to 5 seconds) until it's ready, failing if it isn't |
| `plugin list` | name, pid, state and the events each plugin listens to |
| `plugin list -v` | also its commands, completions, command line and log file |
| `plugin unload NAME...` | stop plugins |
| `plugin reload [-w] NAME...` | restart plugins, with the shell's current directory and environment |

Every executable file in `~/.config/ash/plugins/` is loaded when an
interactive shell starts, under the file's name without its extension
(`git-status.py` becomes `git-status`). Hidden files and names ending in
`~` are skipped. The directory is read and the plugins started in the
background, so they never delay the first prompt. `ash -N` skips them, as
it skips the rest of the config.

`plugin load` doesn't wait for the plugin either: it returns after a few
microseconds and the process starts in the background. In an interactive
shell, the plugins your config loads start once the first prompt is on the
screen, the same as the ones in the plugins directory. The plugin's
commands exist once it has said hello, usually a few milliseconds later.
To use a plugin's command right away, in a script or in config.ash, load
it with `-w`.

## A first plugin

This one keeps a count of the commands you ran in the prompt segment
`count`, and adds a command that resets it:

```python
#!/usr/bin/env python3
import json, sys

def send(msg):
    print(json.dumps(msg), flush=True)

send({"hello": {"name": "count", "events": ["postexec"],
                "commands": [{"name": "reset-count", "description": "start counting again"}]}})
n = 0
for line in sys.stdin:
    msg = json.loads(line)
    if msg.get("event") == "postexec":
        n += 1
    elif msg.get("request") == "run":
        n = 0
        send({"id": msg["id"], "stdout": "back to zero\n", "status": 0})
    send({"segment": {"name": "count", "text": f"#{n}"}})
```

Save it as `~/.config/ash/plugins/count.py`, make it executable, and start
a new shell. The built-in prompt shows `#1`, `#2` and so on after the git
branch, and `reset-count` is a command.

## Prompt segments

A segment is a piece of text a plugin keeps up to date for the prompt. The
segment `git` is the variable `$ash_plugin_git`; setting it to empty text
erases it. Segments go away when their plugin stops.

The built-in prompt shows every segment after the git branch, in name
order. In your own prompt, use the variables:

```sh
function ash_prompt
    echo $PWD $ash_plugin_git
    echo -n '❯ '
end
```

With starship, the segments are in starship's environment (and only
there), so the `env_var` module shows them:

```toml
[env_var.ash_plugin_git]
format = "[$env_value]($style) "
```

When a segment changes while you're at the prompt, ash redraws the prompt.
That's what lets a plugin do slow work, like asking git about a big
repository, without the prompt waiting for it: the prompt appears at once
and fills in a moment later.

## The protocol

### Lines

Both ways, every line is one JSON object, in UTF-8, ending with a newline.
Keys ash doesn't know are ignored, so the protocol can grow. A line that
isn't a JSON object is skipped, and noted in the plugin's log.

The plugin's stderr goes to its log, `~/.cache/ash/plugins/NAME.log`
(`$XDG_CACHE_HOME/ash/plugins`). ash writes its own notes there too (when
it started the plugin, what it skipped, how the plugin exited), on lines
that start with `ash`. A log bigger than 1 MB is moved to `NAME.log.old`
when the plugin starts.

### hello

The first line a plugin writes says what it wants and what it adds:

```json
{"hello": {"name": "git-status",
           "events": ["prompt", "cd"],
           "commands": [{"name": "gst", "description": "short git status"}],
           "complete": ["make"]}}
```

| key | |
|---|---|
| `name` | a name for humans; ash uses the name you loaded it under |
| `events` | the events to send: `prompt`, `preexec`, `postexec`, `cd`, `exit`. Unknown ones are ignored |
| `commands` | commands to add, with a description for completion |
| `complete` | commands whose arguments the plugin completes. They don't have to be its own |

All keys are optional. Until a plugin has said hello it gets no events and
no requests, and `plugin list` shows it as `starting`. Sending hello again
replaces what the first one said.

### Events

ash sends a plugin only the events it asked for.

| event | sent | example |
|---|---|---|
| `prompt` | before each prompt is drawn | `{"event":"prompt","cwd":"/home/me/src","status":1,"pipestatus":[0,1],"duration_ms":1530}` |
| `preexec` | before a command line runs | `{"event":"preexec","line":"make test","cwd":"/home/me/src"}` |
| `postexec` | after a command line ran | `{"event":"postexec","line":"make test","status":2,"duration_ms":5130,"cwd":"/home/me/src"}` |
| `cd` | after the shell changed directory | `{"event":"cd","from":"/home/me","to":"/home/me/src"}` |
| `exit` | when the shell exits | `{"event":"exit"}` |

- `status`, `pipestatus` and `duration_ms` in `prompt` are about the last
  command line. `cwd` in `postexec` is where the shell is after it.
- `cd` comes for every change of the shell's own directory: `cd`, `pushd`,
  a directory typed as a command, `z`, a plugin's `cd`. A `cd` inside a
  subshell, like `(cd build; make)`, doesn't change it.
- A plugin that says hello while a prompt is already up (the ones in
  `~/.config/ash/plugins` always do) gets that prompt's `prompt` event right
  away, so its segments don't have to wait for the next prompt.
- After `exit`, ash closes the plugin's stdin.

### Actions

A plugin can write these at any time, not only in answer to something.

| line | does |
|---|---|
| `{"set": {"name": "VIRTUAL_ENV", "value": ["/src/.venv"], "export": true}}` | sets a global variable. `value` is a list, or one string. With `"export": true` it is exported; otherwise an existing variable keeps its export flag and a new one isn't exported |
| `{"unset": "VIRTUAL_ENV"}` | erases a global variable (or a list of them) |
| `{"segment": {"name": "git", "text": "↑1 +2"}}` | sets the prompt segment `$ash_plugin_git`; empty text erases it. Names are letters, digits and `_` |
| `{"cd": "/home/me/src"}` | changes directory. A relative path starts from the shell's directory, `~` works, `$CDPATH` doesn't apply. An error is shown above the prompt |
| `{"eval": "abbr -a gco git checkout"}` | runs shell code, like a line of config.ash. What it prints is shown above the prompt. It can't exit the shell and doesn't change `$status`. The shell waits for it, so keep it short |
| `{"buffer": "git push"}` | puts text on the command line: right away if you're at the prompt and the line is empty, otherwise on the next one |
| `{"message": "tests passed"}` | prints a line above the prompt |

Actions only take effect when no command is running: before each prompt,
and while ash waits at the prompt. In the second case they apply right away
and the prompt is redrawn if a variable, a segment or the directory
changed. Actions that come in while a command runs wait for it to finish.

One line can hold several actions. They're applied in the order of the
table: set, unset, segment, cd, eval, buffer, message. Setting a variable
or a segment to the value it already has does nothing, so it's fine to
send the same segment after every event.

`eval` is how a plugin brings the things a fish plugin would: functions,
abbreviations, `complete` rules, key bindings. With a command of its own,
that makes a key that asks the plugin something:

```python
send({"hello": {"name": "branches",
                "commands": [{"name": "pick-branch", "description": "choose a git branch"}]}})
send({"eval": "bind ctrl-b 'commandline -i (pick-branch)'"})
```

### Requests

ash asks questions with an `id`, and the answer must carry the same `id`.
Answers can come in any order, so a plugin may work on several requests at
once.

**run.** A command from the hello runs like a builtin:

```json
{"id": 7, "request": "run", "name": "gst", "args": ["-s"], "cwd": "/home/me/src"}
```

When something is piped into the command (`git log | gst`), the request
also has `"stdin": "..."` with all of it. The answer:

```json
{"id": 7, "stdout": "M docs/plugins.md\n", "stderr": "", "status": 0}
```

Missing fields are empty, and `status` 0. ash prints `stdout` and `stderr`
and the command exits with `status` (modulo 256). An answer with a value
of the wrong type, like `"status": "1"`, makes the command fail at once
with status 1 and says why on stderr. It waits 30 seconds at
most: then the command fails with status 124. `ctrl-c` stops the wait, with
status 130. Either way ash sends `{"id": 7, "request": "cancel"}`, which the
plugin may use to stop working, and ignores a later answer.

A plugin's command loses to a builtin of the same name (the hello's entry
is ignored and a line goes to the log) and to a function of the same name.
If two plugins add the same command, the one loaded first has it.

**complete.** For the commands listed under `complete`:

```json
{"id": 8, "request": "complete", "name": "make", "line": "make te", "cursor": 7, "cwd": "/home/me/src"}
```

`cursor` is a byte offset into `line`. The answer lists candidates; each
one's `text` replaces the word under the cursor:

```json
{"id": 8, "candidates": [{"text": "test", "description": "run the tests"}]}
```

ash waits 300 ms for it. An answer that comes later is kept and used the
next time you complete the same thing; answers are reused for 3 seconds.

### The process

- It starts in the shell's directory, with the shell's exported variables,
  plus `ASH_PLUGIN_NAME` (its name) and `ASH_PID` (the shell's pid).
- It runs in its own process group, so `ctrl-c` at the prompt or in a
  command doesn't reach it.
- When its stdin closes, it should exit. That's how ash says goodbye, and
  it is also what happens when ash dies.
- `plugin unload` closes its stdin and sends SIGTERM to its process group,
  then SIGKILL if anything is left after 250 ms. That includes programs the
  plugin started.
- When the shell exits, plugins that asked get `exit`, every plugin's stdin
  is closed, and they have 200 ms to exit before their process groups get
  SIGKILL.
- If a plugin exits on its own, ash says so once, above the prompt, with
  the path of its log. `plugin list` shows it as `dead`, and `plugin reload`
  starts it again.

### Limits

- Up to 256 lines wait to be written to a plugin. When a plugin stops
  reading, events beyond that are dropped and counted (`plugin list` shows
  how many); requests wait for room until they time out.
- Up to 1024 actions wait to be applied; more are dropped and counted. A
  segment update replaces one for the same segment that is still waiting,
  so a plugin can't flood the shell with them.
- A line from a plugin can be up to 64 MB; longer ones are skipped.
- A command gets up to 16 MB of piped stdin.
- Strings are UTF-8. Bytes that aren't valid UTF-8 (in a file name, say)
  arrive as U+FFFD.

## Letting other programs drive the shell

A plugin can hand the shell to programs that aren't plugins.
`examples/plugins/remote.py` listens on a Unix socket only you can open
and exports its path as `$ASH_REMOTE`. Anything started from that shell
can send it `cd`, `buffer` and `message` lines:

```sh
echo '{"cd": "/tmp"}' | nc -U "$ASH_REMOTE"
```

That turns a lot of terminal programs into something that works with the
shell. Some examples:

```sh
# lf: the shell follows lf around. In another tmux pane it follows live;
# started from the shell itself, the shell ends up where lf was when
# you quit. (In ~/.config/lf/lfrc.)
cmd on-cd &{{ printf '{"cd":"%s"}\n' "$PWD" | nc -U "$ASH_REMOTE" }}
```

```lua
-- neovim: send the shell to the directory of the file you're editing
vim.keymap.set("n", "<leader>cd", function()
  vim.fn.system({ "nc", "-U", vim.env.ASH_REMOTE },
    vim.json.encode({ cd = vim.fn.expand("%:p:h") }) .. "\n")
end)
```

```sh
# a picker in a tmux popup puts its choice on the shell's command line
branch=$(git branch --format='%(refname:short)' | fzf) &&
    jq -cn --arg b "git switch $branch" '{buffer: $b}' | nc -U "$ASH_REMOTE"
```

## Examples

In `examples/plugins/`:

| | |
|---|---|
| `git-status.py` | ahead, behind, staged, changed and untracked counts as the segment `git`, computed in the background; a stale `git` is killed |
| `took.sh` | how long the last slow command took, as the segment `took`. In sh, with jq |
| `make-targets.py` | completes `make` targets from the Makefile, and adds a `targets` command |
| `remote.py` | the socket described above |

## When something goes wrong

`plugin list -v` shows where a plugin's log is. The log has everything the
plugin wrote to stderr, and ash's notes about what it skipped: lines that
aren't JSON, commands that clash with builtins, segment names it can't
use, variables it couldn't set. `plugin load -w` reports a plugin that
exits or stays quiet before saying hello.
