#!/usr/bin/env python3
"""git-status: the state of the git repository as a prompt segment.

    plugin load git ~/src/ash/examples/plugins/git-status.py

or copy it into ~/.config/ash/plugins/. It sets the segment "git"
($ash_plugin_git) to something like "↑1 ↓2 +3 !1 ?4": commits ahead
and behind, staged, changed and untracked files. The built-in prompt
already shows the branch, so the segment doesn't.

The shell never waits for git. The plugin runs it when a prompt is drawn
or the directory changes, and ash redraws the prompt when the answer
comes. If a new question comes in while git is still busy with the old
one, the old git is killed: its answer would be out of date.
"""

import json
import subprocess
import sys
import threading

lock = threading.Condition()
wanted = None  # the directory to look at next
running = None  # the git process at work
sent = None  # the last segment text sent


def send(msg):
    sys.stdout.write(json.dumps(msg) + "\n")
    sys.stdout.flush()


def git_status(cwd):
    global running
    try:
        p = subprocess.Popen(
            ["git", "--no-optional-locks", "status", "--porcelain=v2", "--branch"],
            cwd=cwd,
            stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL,
            text=True,
        )
    except OSError:
        return ""
    with lock:
        running = p
    out, _ = p.communicate()
    with lock:
        running = None
    if p.returncode != 0:
        return ""  # not a repository (or git was killed)

    ahead = behind = staged = changed = untracked = 0
    for line in out.splitlines():
        if line.startswith("# branch.ab "):
            a, b = line.split()[2:4]
            ahead, behind = int(a), -int(b)
        elif line[:2] in ("1 ", "2 "):
            xy = line.split()[1]
            staged += xy[0] != "."
            changed += xy[1] != "."
        elif line.startswith("u "):
            changed += 1
        elif line.startswith("? "):
            untracked += 1
    parts = []
    for n, sign in ((ahead, "↑"), (behind, "↓"), (staged, "+"), (changed, "!"), (untracked, "?")):
        if n:
            parts.append(f"{sign}{n}")
    return "\x1b[33m" + " ".join(parts) + "\x1b[0m" if parts else ""


def worker():
    global wanted, sent
    while True:
        with lock:
            while wanted is None:
                lock.wait()
            cwd, wanted = wanted, None
        text = git_status(cwd)
        with lock:
            if wanted is not None:
                continue  # already out of date
        if text != sent:
            sent = text
            send({"segment": {"name": "git", "text": text}})


def look_at(cwd):
    global wanted
    with lock:
        wanted = cwd
        if running is not None:
            running.kill()
        lock.notify()


send({"hello": {"name": "git-status", "events": ["prompt", "cd"]}})
threading.Thread(target=worker, daemon=True).start()

for line in sys.stdin:
    event = json.loads(line)
    if event.get("event") == "prompt":
        look_at(event["cwd"])
    elif event.get("event") == "cd":
        look_at(event["to"])
# ash closed our input: the shell is gone, so are we.
