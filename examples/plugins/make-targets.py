#!/usr/bin/env python3
"""make-targets: completion for make's targets, and a command that lists them.

    plugin load make ~/src/ash/examples/plugins/make-targets.py

It shows the two things a plugin can add besides actions:

- completions: "make" is listed under "complete" in the hello, so ash
  asks the plugin when you complete `make <tab>`;
- commands: `targets` becomes a command, which ash runs by sending the
  plugin a request and printing the answer.

Targets come from the Makefile in the shell's directory, with the text of
a "## comment" after them as the description. They're cached until the
file changes.
"""

import json
import os
import re
import sys

RULE = re.compile(r"^([A-Za-z0-9_][^:=#\s]*(?:\s+[^:=#\s]+)*)\s*::?(?!=)[^#]*(?:##\s*(.*))?$")
cache = {}  # path -> (mtime, {target: description})


def send(msg):
    sys.stdout.write(json.dumps(msg) + "\n")
    sys.stdout.flush()


def targets(cwd):
    for name in ("GNUmakefile", "makefile", "Makefile"):
        path = os.path.join(cwd, name)
        try:
            mtime = os.stat(path).st_mtime
        except OSError:
            continue
        hit = cache.get(path)
        if hit and hit[0] == mtime:
            return hit[1]
        found = {}
        with open(path, errors="replace") as f:
            for line in f:
                m = RULE.match(line)
                if m:
                    for t in m.group(1).split():
                        found.setdefault(t, (m.group(2) or "").strip())
        cache[path] = (mtime, found)
        return found
    return {}


def complete(req):
    # cursor counts bytes, Python strings count characters
    before = req["line"].encode()[: req["cursor"]].decode(errors="ignore")
    word = before.split(" ")[-1]
    cands = [
        {"text": t, "description": d}
        for t, d in sorted(targets(req["cwd"]).items())
        if t.startswith(word)
    ]
    send({"id": req["id"], "candidates": cands})


def run(req):
    found = targets(req["cwd"])
    if not found:
        send({"id": req["id"], "stderr": "targets: no makefile here\n", "status": 1})
        return
    width = max(len(t) for t in found)
    out = "".join(f"{t:<{width}}  {d}".rstrip() + "\n" for t, d in sorted(found.items()))
    send({"id": req["id"], "stdout": out, "status": 0})


send({
    "hello": {
        "name": "make-targets",
        "commands": [{"name": "targets", "description": "list the make targets here"}],
        "complete": ["make"],
    }
})

for line in sys.stdin:
    req = json.loads(line)
    if req.get("request") == "complete":
        complete(req)
    elif req.get("request") == "run":
        run(req)
