#!/usr/bin/env python3
"""remote: let other programs drive this shell.

    plugin load remote ~/src/ash/examples/plugins/remote.py

The plugin listens on a Unix socket that only you can use, and exports
its path as $ASH_REMOTE. Programs started from this shell (a file
manager, an editor, a script in a tmux popup) can then send the shell
the same actions plugins send, one JSON object per line:

    echo '{"cd": "/tmp"}' | nc -U "$ASH_REMOTE"
    echo '{"buffer": "git push"}' | nc -U "$ASH_REMOTE"
    echo '{"message": "the build is done"}' | nc -U "$ASH_REMOTE"

If the shell is waiting at its prompt, a cd happens right away and the
prompt is redrawn; if a command is running (say, the file manager
itself), it happens when that command exits. Only cd, buffer and message
are passed on.
"""

import json
import os
import shutil
import socket
import sys
import tempfile
import threading

ALLOWED = ("cd", "buffer", "message")
out_lock = threading.Lock()


def send(msg):
    with out_lock:
        sys.stdout.write(json.dumps(msg) + "\n")
        sys.stdout.flush()


def serve(conn):
    with conn, conn.makefile("r", encoding="utf-8", errors="replace") as f:
        for line in f:
            try:
                msg = json.loads(line)
            except ValueError:
                continue
            if isinstance(msg, dict):
                action = {k: v for k, v in msg.items() if k in ALLOWED and isinstance(v, str)}
                if action:
                    send(action)


def listen(server):
    while True:
        try:
            conn, _ = server.accept()
        except OSError:
            return
        threading.Thread(target=serve, args=(conn,), daemon=True).start()


# A directory only we can enter, so nobody else can connect.
home = tempfile.mkdtemp(prefix="ash-remote-")
path = os.path.join(home, "socket")
server = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
server.bind(path)
server.listen()
threading.Thread(target=listen, args=(server,), daemon=True).start()

send({"hello": {"name": "remote", "events": ["exit"]}})
send({"set": {"name": "ASH_REMOTE", "value": [path], "export": True}})

try:
    for line in sys.stdin:
        if json.loads(line).get("event") == "exit":
            break
finally:
    server.close()
    shutil.rmtree(home, ignore_errors=True)
