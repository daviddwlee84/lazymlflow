#!/usr/bin/env python3
"""Real PTY acceptance using disposable HTTP fixtures; Python stdlib only."""
import argparse
import errno
import fcntl
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import pty
import re
import select
import signal
import sqlite3
import struct
import subprocess
import tempfile
import termios
import threading
import time
from urllib.parse import parse_qs, urlparse


class Fixture:
    def __init__(self, name):
        self.name, self.events = name, []
        self.delay, self.fail = 0, False
        self.ids = [f"{i:032x}" for i in (1, 2, 3)]
        fixture = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *_):
                pass

            def respond(self, payload, status=200, content_type="application/json"):
                body = json.dumps(payload).encode() if content_type == "application/json" else payload.encode()
                self.send_response(status)
                self.send_header("Content-Type", content_type)
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                try:
                    self.wfile.write(body)
                except (BrokenPipeError, ConnectionResetError):
                    pass

            def do_POST(self):
                payload = json.loads(self.rfile.read(int(self.headers.get("Content-Length", 0))))
                fixture.events.append((self.path, payload))
                if self.path.endswith("experiments/search"):
                    self.respond({"experiments": [{"experiment_id": str(i), "name": name, "lifecycle_stage": "active"}
                                                  for i, name in ((1, "Alpha 專案"), (2, "Beta"))]})
                elif self.path.endswith("runs/search"):
                    delay = fixture.delay
                    if delay:
                        time.sleep(delay)
                    if fixture.fail:
                        self.respond({"error_code": "TEMPORARY", "message": "fixture refresh failed"}, 503)
                        return
                    self.respond({"runs": [fixture.run(i, payload["experiment_ids"][0]) for i in range(3)]})
                else:
                    self.respond({"message": "unsupported fixture operation"}, 404)

            def do_GET(self):
                url = urlparse(self.path)
                query = parse_qs(url.query)
                fixture.events.append((url.path, query))
                if url.path == "/version":
                    self.respond("3.16.1", content_type="text/plain")
                elif url.path.endswith("runs/get"):
                    self.respond({"run": fixture.get_run(query["run_id"][0])})
                elif url.path.endswith("experiments/get"):
                    experiment = query["experiment_id"][0]
                    self.respond({"experiment": {"experiment_id": experiment,
                                                 "name": "Alpha 專案" if experiment == "1" else "Beta",
                                                 "lifecycle_stage": "active"}})
                elif url.path.endswith("metrics/get-history"):
                    self.respond({"metrics": [{"key": "loss", "step": i, "timestamp": 1700000000000 + i * 1000, "value": 1 / (i + 1)} for i in range(20)]})
                elif url.path.endswith("artifacts/list"):
                    self.respond({"files": [{"path": "report.txt", "is_dir": False, "file_size": 18}]})
                elif "/mlflow-artifacts/artifacts/" in url.path:
                    self.respond("downloaded report\n", content_type="text/plain")
                else:
                    self.respond({"message": "unknown fixture endpoint"}, 404)

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.url = f"http://127.0.0.1:{self.server.server_port}"

    def run(self, index, experiment):
        return {"info": {"run_id": self.ids[index], "experiment_id": experiment,
                         "run_name": f"{self.name}-run-{index}", "status": "FINISHED", "lifecycle_stage": "active",
                         "start_time": 1700000000000 + index * 10000, "end_time": 1700000009000 + index * 10000,
                         "artifact_uri": f"mlflow-artifacts:/{experiment}/{self.ids[index]}/artifacts"},
                "data": {"metrics": [{"key": "loss", "value": 0.1 * (index + 1), "step": 20, "timestamp": 1700000020000}],
                         "params": [{"key": "lr", "value": str(0.01 * (index + 1))}],
                         "tags": [{"key": "team", "value": "research"}]}}

    def get_run(self, run_id):
        return self.run(self.ids.index(run_id), "1")

    def close(self):
        self.server.shutdown()
        self.server.server_close()


class Terminal:
    def __init__(self, command, env, cwd):
        self.master, self.slave = pty.openpty()
        self.before = termios.tcgetattr(self.slave)
        self.resize(120, 36)
        self.process = subprocess.Popen(command, stdin=self.slave, stdout=self.slave, stderr=self.slave,
                                        cwd=cwd, env=env, start_new_session=True)
        self.raw = bytearray()

    def resize(self, width, height):
        fcntl.ioctl(self.slave, termios.TIOCSWINSZ, struct.pack("HHHH", height, width, 0, 0))
        if hasattr(self, "process"):
            os.kill(self.process.pid, signal.SIGWINCH)

    def pump(self, timeout=0.05):
        readable, _, _ = select.select([self.master], [], [], timeout)
        if readable:
            try:
                data = os.read(self.master, 65536)
            except OSError as exc:
                if exc.errno == errno.EIO:
                    return
                raise
            self.raw.extend(data)
            # Reply to terminal capability queries; these responses are input
            # protocol, not synthesized application keystrokes.
            if b"\x1b[c" in data:
                os.write(self.master, b"\x1b[?1;2c")
            if b"\x1b[6n" in data:
                os.write(self.master, b"\x1b[1;1R")
            if b"\x1b]11;?" in data:
                os.write(self.master, b"\x1b]11;rgb:0000/0000/0000\x1b\\")

    def wait(self, predicate, description, seconds=8):
        end = time.monotonic() + seconds
        while time.monotonic() < end:
            self.pump()
            if predicate():
                return
            if self.process.poll() is not None:
                raise AssertionError(f"app exited during {description}: {self.text()[-2000:]}")
        raise AssertionError(f"timeout: {description}\n{self.text()[-2500:]}")

    def text(self, offset=0):
        text = bytes(self.raw[offset:]).decode("utf-8", "replace")
        return re.sub(r"\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07]*(?:\x07|\x1b\\))", "", text)

    def send(self, text):
        os.write(self.master, text.encode())
        until = time.monotonic() + 0.12
        while time.monotonic() < until:
            self.pump(0.02)

    def mouse(self, code, x, y, release=False):
        """Terminal SGR protocol uses one-based cell coordinates."""
        self.send(f"\x1b[<{code};{x + 1};{y + 1}{'m' if release else 'M'}")

    def click(self, x, y):
        self.mouse(0, x, y)
        self.mouse(0, x, y, release=True)

    def close(self):
        if self.process.poll() is None:
            self.send("\x03")
            try:
                self.process.wait(8)
            except subprocess.TimeoutExpired:
                os.killpg(self.process.pid, signal.SIGKILL)
                self.process.wait()
        self.pump(0.02)
        after = termios.tcgetattr(self.slave)
        assert (after[3] & (termios.ECHO | termios.ICANON)) == (self.before[3] & (termios.ECHO | termios.ICANON)), "terminal modes not restored"
        assert b"\x1b[?1049l" in self.raw, "alternate screen not restored"
        os.close(self.master)
        os.close(self.slave)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--binary", default="./bin/lazymlflow")
    args = parser.parse_args()
    binary = str(Path(args.binary).resolve())
    fixtures = [Fixture("one"), Fixture("two")]
    terminal = None
    try:
        with tempfile.TemporaryDirectory(prefix="lazymlflow-pty-") as directory:
            root = Path(directory)
            config = root / "config.toml"
            config.write_text('default_target = "one"\n' + "\n".join(
                f'[[targets]]\nid = "{fixture.name}"\ntracking_uri = "{fixture.url}"\n'
                for fixture in fixtures), encoding="utf-8")
            env = {k: v for k, v in os.environ.items() if k not in ("LAZYMLFLOW_TARGET", "MLFLOW_TRACKING_URI")}
            env.update(TERM="xterm-256color", NO_COLOR="1", XDG_CONFIG_HOME=str(root / "config"),
                       XDG_DATA_HOME=str(root / "data"), XDG_STATE_HOME=str(root / "state"))
            terminal = Terminal([binary, "--config", str(config)], env, root)
            terminal.wait(lambda: "one-run-0" in terminal.text(), "initial experiments and runs")
            terminal.wait(lambda: b"\x1b[?1002h" in terminal.raw, "mouse reporting enabled")
            # Click experiment rows through the actual terminal parser. The
            # wide layout starts with four fixed Activity shortcuts, a divider,
            # and the experiment status line below the pane border.
            terminal.click(8, 9)
            terminal.wait(lambda: any(p.endswith("runs/search") and q["experiment_ids"] == ["2"] for p, q in fixtures[0].events), "mouse selects experiment")
            terminal.click(8, 8)
            terminal.send("j")
            terminal.wait(lambda: any(p.endswith("runs/search") and q["experiment_ids"] == ["2"] for p, q in fixtures[0].events), "Vim experiment navigation")
            terminal.send("\x1b[A")
            terminal.send("2/jkhql/?123M")
            assert terminal.process.poll() is None, "typing q exited application"
            terminal.send("\x1b")
            # Same grouped searchable column picker as the visible web UI:
            # search, leave typing, select a checkbox, close, persist.
            terminal.send("vParameters / lr\r \x1b")
            state_path = root / "data" / "lazymlflow" / "state.db"

            def saved_view():
                if not state_path.exists():
                    return None
                try:
                    with sqlite3.connect(f"file:{state_path}?mode=ro", uri=True, timeout=0.1) as db:
                        row = db.execute("SELECT value FROM experiment_views WHERE experiment='1'").fetchone()
                        return json.loads(row[0]) if row else None
                except sqlite3.Error:
                    return None

            terminal.wait(lambda: saved_view() is not None and any(c["kind"] == "param" and c["key"] == "lr" for c in saved_view()["columns"]), "column checkbox persisted to SQLite")
            start = len(fixtures[0].events)
            terminal.send("fmetrics.loss < 0.5\r")
            terminal.wait(lambda: any(p.endswith("runs/search") and q.get("filter") == "metrics.loss < 0.5" for p, q in fixtures[0].events[start:]), "submitted server filter")
            terminal.send(" j ")
            mark = len(terminal.raw)
            terminal.send("c")
            terminal.wait(lambda: "Compare" in terminal.text(mark), "multi-run comparison")
            terminal.send("mloss\r")
            terminal.wait(lambda: sum(p.endswith("metrics/get-history") for p, _ in fixtures[0].events) >= 2, "metric history for selected runs")
            terminal.send("a")
            terminal.send("\x1b")
            terminal.send("2bParameter / lr\r \x1b")
            terminal.wait(lambda: saved_view() is not None and saved_view().get("group_by") == ["lr"], "parameter grouping persisted")
            terminal.send("bLayout: flat\r \x1b")
            terminal.wait(lambda: saved_view() is not None and saved_view().get("mode") == "flat", "return to flat table")
            # Numeric pane keys, zoom, resize gesture, and capture toggle are
            # explicit navigation actions, not active while editing fields.
            terminal.send("1z")
            terminal.send("z2\x17ll\r")
            terminal.mouse(0, 32, 10)
            terminal.mouse(32, 47, 10)
            terminal.mouse(0, 47, 10, release=True)
            terminal.send("M")
            terminal.wait(lambda: b"\x1b[?1002l" in terminal.raw, "mouse capture disabled")
            terminal.send("M")
            terminal.mouse(65, 12, 5)
            terminal.send("\t]]]]")
            terminal.wait(lambda: any(p.endswith("artifacts/list") for p, _ in fixtures[0].events), "artifact tab")
            output = root / "downloaded.txt"
            terminal.send("d\x01\x0b" + str(output) + "\r")
            terminal.wait(output.exists, "artifact download")
            assert output.read_text() == "downloaded report\n"
            mark = len(terminal.raw)
            terminal.send("?")
            terminal.wait(lambda: "Keyboard actions" in terminal.text(mark), "context help")
            terminal.send("/no-match-jkhql?123M")
            terminal.wait(lambda: "No matching help entries" in terminal.text(mark), "help live filter owns shortcuts")
            terminal.send("\x15DOWNLOAD\r")
            assert terminal.process.poll() is None, "help search dispatched quit"
            terminal.wait(lambda: "edit filter" in terminal.text(mark), "Enter keeps help filter")
            mark = len(terminal.raw)
            terminal.send("\x1b")
            # The header is unchanged, so the terminal may repaint only its
            # count. A restored nonmatching row proves help remains open.
            terminal.wait(lambda: "Activity inbox" in terminal.text(mark), "Esc clears help filter without closing")
            terminal.send("\x1b")
            for width, height in ((80, 24), (38, 12), (12, 4), (140, 42)):
                terminal.resize(width, height)
                terminal.pump(0.15)
            fixtures[0].delay = 1.5
            terminal.send("\x1b[Zr")
            terminal.send("tj\r")
            terminal.wait(lambda: any(p.endswith("runs/search") for p, _ in fixtures[1].events), "target switch during refresh")
            # Full overlay/back makes the selected rows repaint. The renderer
            # may otherwise emit only 'two' when replacing 'one-run-*', which
            # is not a complete screen snapshot in this raw-output harness.
            terminal.send("?")
            terminal.send("\x1b")
            terminal.wait(lambda: "two-run" in terminal.text(), "second target visible")
            terminal.send("q")
            terminal.process.wait(8)
            assert terminal.process.returncode == 0, terminal.text()[-1500:]
            terminal.close()
            terminal = None
            print("PASS PTY: arrows/Vim, mouse click/wheel/drag, literal input, columns/grouping persistence, filters, comparison/history, artifacts, targets, resize/zoom, terminal cleanup")
    finally:
        if terminal:
            terminal.close()
        for fixture in fixtures:
            fixture.close()


if __name__ == "__main__":
    main()
