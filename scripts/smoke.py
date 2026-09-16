#!/usr/bin/env python3
"""Exercise a built binary with one server and two independent client configs."""
import json
import os
from pathlib import Path
import secrets
import signal
import sqlite3
import subprocess
import sys
import tempfile

binary = str(Path(sys.argv[1] if len(sys.argv) > 1 else "bin/taskhub").resolve())
environment = {k: v for k, v in os.environ.items() if k not in ("TASKHUB_URL", "TASKHUB_TOKEN")}

with tempfile.TemporaryDirectory(prefix="taskhub-smoke-") as directory:
    root = Path(directory)
    token = secrets.token_hex(32)
    database = root / "tasks.db"
    with sqlite3.connect(database) as db:
        db.executescript("CREATE TABLE tasks(id INTEGER PRIMARY KEY AUTOINCREMENT,title TEXT NOT NULL,body TEXT NOT NULL DEFAULT '',status TEXT NOT NULL); INSERT INTO tasks(title,body,status) VALUES('Pre-upgrade task','Preserve this document','done');")
    config_a, config_b = root / "laptop.json", root / "remote.json"

    def command(config, *args, input="", code=0):
        result = subprocess.run([binary, *args, "--config", str(config)], input=input, text=True, capture_output=True, env=environment, timeout=15)
        assert result.returncode == code, (args, result.returncode, result.stderr)
        assert token not in result.stdout + result.stderr
        return result.stdout

    def start():
        process = subprocess.Popen([binary, "serve", "--listen", "127.0.0.1:0", "--db", str(database)], env={**environment, "TASKHUB_TOKEN": token}, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        line = process.stderr.readline()
        if "listening on " not in line:
            process.terminate()
            process.wait(timeout=10)
            raise AssertionError("Server did not start: " + line)
        url = "http://" + line.strip().split("listening on ", 1)[1]
        for config in (config_a, config_b):
            command(config, "config", "--url", url, "--token-stdin", input=token)
        return process

    process = start()
    try:
        body = "# 跨设备任务\n\n- 保留正文和换行\n- Commit: example\n"
        legacy = json.loads(command(config_a, "show", "1", "--json"))
        assert legacy == {"id": 1, "title": "Pre-upgrade task", "body": "Preserve this document", "status": "done", "project": "", "archived": False}
        created = json.loads(command(config_a, "add", "--title", "Shared task", "--body-file", "-", "--project", "ellie", "--json", input=body))
        task_id = str(created["id"])
        assert command(config_b, "show", task_id, "--body-only") == body
        command(config_b, "update", task_id, "--status", "in_progress", "--if-status", "pending")
        command(config_a, "update", task_id, "--status", "in_progress", "--if-status", "pending", code=3)
        command(config_b, "update", task_id, "--body", body + "\nImplemented.\n", "--status", "review")
        summaries = json.loads(command(config_a, "list", "--status", "review", "--project", "ellie", "--json"))
        assert summaries == [{"id": created["id"], "title": "Shared task", "status": "review", "project": "ellie", "archived": False}]
        assert json.loads(command(config_a, "list", "--project", "plate", "--json")) == []
        process.send_signal(signal.SIGTERM)
        assert process.wait(timeout=15) == 0
        process = start()
        restored = json.loads(command(config_a, "show", task_id, "--json"))
        assert restored["body"] == body + "\nImplemented.\n" and restored["status"] == "review" and restored["project"] == "ellie"
        command(config_b, "update", task_id, "--project", "plate")
        assert json.loads(command(config_a, "show", task_id, "--json"))["project"] == "plate"
        command(config_b, "update", task_id, "--project", "")
        assert json.loads(command(config_a, "list", "--project", "", "--status", "review", "--json"))[0]["id"] == created["id"]
        command(config_a, "update", task_id, "--status", "done")
        assert json.loads(command(config_b, "show", task_id, "--json"))["status"] == "done"
        before_archive = json.loads(command(config_a, "show", task_id, "--json"))
        command(config_a, "archive", task_id)
        command(config_b, "show", task_id, "--json", code=1)
        assert all(t["id"] != created["id"] for t in json.loads(command(config_b, "list", "--json")))
        assert json.loads(command(config_b, "list", "--archived", "--json"))[0]["id"] == created["id"]
        assert json.loads(command(config_b, "show", task_id, "--archived", "--json")) == {**before_archive, "archived": True}
        process.terminate()
        assert process.wait(timeout=15) == 0
        process = start()
        command(config_a, "show", task_id, "--json", code=1)
        command(config_b, "unarchive", task_id)
        assert json.loads(command(config_a, "show", task_id, "--json")) == before_archive
        assert json.loads(command(config_a, "show", "1", "--json")) == legacy
    finally:
        if process.poll() is None:
            process.terminate()
            process.wait(timeout=15)
print("PASS: legacy migration, two clients, projects, archive hiding/restore, Markdown, claim conflict, restart, acceptance")
