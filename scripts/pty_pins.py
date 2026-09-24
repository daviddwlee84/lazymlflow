#!/usr/bin/env python3
"""Real-PTY global run pins, source isolation and unavailable-run recovery."""
import argparse
import base64
from copy import deepcopy
import hashlib
import json
import os
from pathlib import Path
import sqlite3
import tempfile
import time
from urllib.parse import parse_qs, urlparse

from pty_activity import ActivityFixture, read_json_rows
from pty_smoke import Terminal


class PinsFixture(ActivityFixture):
    def __init__(self):
        super().__init__()
        self.long_names = {
            "complete": "pin-alpha-" + "long-component-" * 7 + "ALPHA-FULL-NAME-END",
            "running": "pin-beta-" + "long-component-" * 7 + "BETA-FULL-NAME-END",
        }
        for name, full_name in self.long_names.items():
            self.records[self.ids[name]]["info"]["run_name"] = full_name
        fixture, original = self, self.server.RequestHandlerClass

        class Handler(original):
            def do_GET(self):
                url = urlparse(self.path)
                query = parse_qs(url.query)
                if url.path.endswith("runs/get") and query["run_id"][0] not in fixture.records:
                    fixture.events.append((url.path, query))
                    self.respond({"error_code": "RESOURCE_DOES_NOT_EXIST", "message": "Run unavailable in this fixture"}, 404)
                    return
                super().do_GET()

        self.server.RequestHandlerClass = Handler

    def reads(self, run_id, offset=0):
        return sum(path.endswith("runs/get") and query.get("run_id") == [run_id]
                   for path, query in self.events[offset:])


def source_key(target, uri):
    # core.SourceKey's canonical configured-origin identity (ordinary target).
    return hashlib.sha256(json.dumps([target, uri, ""], separators=(",", ":")).encode()).hexdigest()


def saved_pins(path, source):
    if not path.exists():
        return {}
    prefix = "run-pin/v1/" + base64.urlsafe_b64encode(source.encode()).decode().rstrip("=") + "/"
    try:
        with sqlite3.connect(f"file:{path}?mode=ro", uri=True, timeout=.1) as db:
            rows = db.execute("SELECT key,value FROM preferences WHERE key>=? AND key<?", (prefix, prefix[:-1] + "0"))
            pins = {}
            for key, raw in rows:
                pin = json.loads(raw)
                expected = prefix + base64.urlsafe_b64encode(pin["run_id"].encode()).decode().rstrip("=")
                assert key == expected, "persisted pin identity mismatch"
                pins[pin["run_id"]] = pin
            return pins
    except sqlite3.Error:
        return {}


def redraw(terminal):
    terminal.send("?")
    offset = len(terminal.raw)
    terminal.send("\x1b")
    return offset


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--binary", default="./bin/lazymlflow")
    binary = str(Path(parser.parse_args().binary).resolve())
    fixture, terminal = PinsFixture(), None
    try:
        with tempfile.TemporaryDirectory(prefix="lazymlflow-pins-pty-") as directory:
            root = Path(directory)
            config = root / "config.toml"
            config.write_text('default_target="pins"\n[activity]\nrefresh_seconds=0\n' + "".join(
                f'[[targets]]\nid="{target}"\ntracking_uri="{fixture.url}"\n' for target in ("pins", "other")))
            env = {key: value for key, value in os.environ.items() if key not in ("LAZYMLFLOW_TARGET", "MLFLOW_TRACKING_URI")}
            env.update(TERM="xterm-256color", NO_COLOR="1", XDG_CONFIG_HOME=str(root / "config"),
                       XDG_DATA_HOME=str(root / "data"), XDG_STATE_HOME=str(root / "state"))
            state = root / "data" / "lazymlflow" / "state.db"
            source = source_key("pins", fixture.url)
            other_source = source_key("other", fixture.url)
            pins = lambda: saved_pins(state, source)
            alpha, beta = fixture.ids["complete"], fixture.ids["running"]
            terminal = Terminal([binary, "--config", str(config)], env, root)
            terminal.resize(180, 42)
            terminal.wait(lambda: len(read_json_rows(state, "activity_records", "run_id")) == 5, "Activity records and local pins loaded")
            terminal.send("1ggjjjjj\r2gg")
            terminal.wait(lambda: "pin-alpha-" in terminal.text(), "first experiment run selected")
            terminal.send("*")
            terminal.wait(lambda: set(pins()) == {alpha}, "first run pin persisted")
            first_time = pins()[alpha]["pinned_at"]
            terminal.send("1gg\r2gg")
            terminal.wait(lambda: fixture.reads(beta) > 0, "Running view hydrated second experiment")
            terminal.send("*")
            terminal.wait(lambda: set(pins()) == {alpha, beta}, "cross-experiment second pin persisted")
            assert {pin["experiment_id"] for pin in pins().values()} == {"1", "2"}

            terminal.send("1ggjjjj\r2")
            mark = redraw(terminal)
            terminal.wait(lambda: "Pinned 2" in terminal.text(mark), "Pinned sidebar combines both experiments")
            terminal.send("/\x01\x0b" + alpha + "\r")
            mark = len(terminal.raw)
            terminal.send("i")
            terminal.wait(lambda: fixture.long_names["complete"] in terminal.text(mark), "full pinned run name in info popup")
            terminal.send("\x1b")
            terminal.send("/\x1b")

            # The same server under another configured source has separate pins.
            terminal.send("tj\r")
            terminal.send("1ggjjjj\r2")
            mark = redraw(terminal)
            terminal.wait(lambda: "Pinned 0" in terminal.text(mark), "different configured source has no pins")
            assert not saved_pins(state, other_source)
            assert set(pins()) == {alpha, beta}
            terminal.send("tk\r")
            terminal.send("1ggjjjj\r2")
            mark = redraw(terminal)
            terminal.wait(lambda: "Pinned 2" in terminal.text(mark), "returning source restores pins")
            terminal.send("/\x01\x0b" + beta + "\r*")
            terminal.wait(lambda: set(pins()) == {alpha}, "unpin deletes only selected source/run")
            terminal.send("/\x1b")
            assert pins()[alpha]["pinned_at"] == first_time
            terminal.send("q")
            terminal.process.wait(8)
            assert terminal.process.returncode == 0
            terminal.close()
            terminal = None

            # Restart after the server no longer has the run. The local pin
            # remains readable and removable independently of Activity records.
            # Simulate disposal of the rebuildable cache in this test-only DB;
            # the independently stored pin row must survive that reset.
            with sqlite3.connect(state) as db:
                for table in ("activity_records", "activity_sources", "activity_count_cache", "activity_run_cache"):
                    db.execute(f"DELETE FROM {table}")
            assert set(pins()) == {alpha}, "Activity cache reset removed the independent pin"
            original = deepcopy(fixture.records.pop(alpha))
            terminal = Terminal([binary, "--config", str(config)], env, root)
            terminal.resize(180, 42)
            terminal.wait(lambda: "Pinned 1" in terminal.text(), "saved pin survives restart")
            mark = len(terminal.raw)
            terminal.send("1ggjjjj\r2r")
            terminal.wait(lambda: "unavailable" in terminal.text(mark).lower(), "missing pinned run retains unavailable row")
            assert set(pins()) == {alpha}, "missing backend run deleted the pin"
            attempts = fixture.reads(alpha)
            until = time.monotonic() + .5
            while time.monotonic() < until:
                terminal.pump(.03)
            assert fixture.reads(alpha) == attempts, "unavailable pin caused automatic retry loop"
            terminal.send("r")
            terminal.wait(lambda: fixture.reads(alpha) > attempts, "explicit retry reads missing pin again")

            original["info"]["run_name"] += "-RESTORED-OK"
            fixture.records[alpha] = original
            attempts = fixture.reads(alpha)
            terminal.send("r")
            terminal.wait(lambda: fixture.reads(alpha) > attempts, "retry after backend restoration")
            mark = len(terminal.raw)
            terminal.send("i")
            terminal.wait(lambda: "RESTORED-OK" in terminal.text(mark), "restored run metadata replaces unavailable placeholder")
            terminal.send("\x1b")
            assert set(pins()) == {alpha} and pins()[alpha]["pinned_at"] == first_time
            fixture.records[alpha]["info"]["lifecycle_stage"] = "deleted"
            mark = len(terminal.raw)
            terminal.send("r")
            terminal.wait(lambda: "deleted remotely" in terminal.text(mark), "remotely deleted pin stays visible with lifecycle note")
            assert set(pins()) == {alpha}, "remote deletion removed the local pin"
            terminal.send("2/*qij\x1b")
            assert terminal.process.poll() is None and set(pins()) == {alpha}, "typed shortcut escaped search"
            for width, height in ((80, 24), (38, 12), (180, 42)):
                terminal.resize(width, height)
                terminal.pump(.1)
            terminal.send("2*")
            terminal.wait(lambda: not pins(), "final unpin persisted")
            terminal.send("q")
            terminal.process.wait(8)
            assert terminal.process.returncode == 0
            terminal.close()
            terminal = None
            print("PASS Pins PTY: cross-experiment pins, full names, source isolation, unpin/restart persistence, missing-run retry/recovery, deleted-run retention, typing, resize and terminal cleanup")
    finally:
        if terminal:
            terminal.close()
        fixture.close()


if __name__ == "__main__":
    main()
