#!/usr/bin/env python3
"""v0.3 acceptance through a real PTY, isolated SQLite and read-only fixtures."""
import argparse
import json
import os
from pathlib import Path
import sqlite3
import sys
import tempfile
import time
from urllib.parse import parse_qs, urlparse

from pty_smoke import Fixture, Terminal


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--binary", default="./bin/lazymlflow")
    args = parser.parse_args()
    binary = str(Path(args.binary).resolve())
    fixture = Fixture("inspect")
    terminal = None
    original_run = fixture.run
    fixture.status = "RUNNING"

    def run(index, experiment):
        result = original_run(index, experiment)
        result["info"]["run_id"] = f"{(int(experiment) - 1) * 3 + index + 1:032x}"
        result["info"]["status"] = fixture.status
        result["data"]["metrics"] = [
            {"key": key, "value": value, "step": 17, "timestamp": 1700000017000}
            for key, value in (("loss", 0.4447), ("valid_loss", 0.6162))
        ]
        result["inputs"] = {"dataset_inputs": [{
            "dataset": {"name": "training features", "digest": "abc123", "source_type": "local",
                        "source": '{"uri":"fixture.feather"}',
                        "schema": json.dumps({"mlflow_colspec": [
                            {"name": f"feature_{i}", "type": "double", "required": True} for i in range(146)]}),
                        "profile": '{"num_rows":4558,"num_elements":665468}'},
            "tags": [{"key": "mlflow.data.context", "value": "training"}]}]}
        return result

    fixture.run = run
    fixture.get_run = lambda run_id: run((int(run_id, 16) - 1) % 3, str((int(run_id, 16) - 1) // 3 + 1))
    handler = fixture.server.RequestHandlerClass
    old_get = handler.do_GET

    def get(self):
        url = urlparse(self.path)
        query = parse_qs(url.query)
        if url.path.endswith("metrics/get-history"):
            fixture.events.append((url.path, query))
            key = query["metric_key"][0]
            start = int(query.get("page_token", ["0"])[0])
            maximum = int(query.get("max_results", ["0"])[0])
            values = [{"key": key, "step": i, "timestamp": 1700000000000 + i * 1000,
                       "value": .452 - i * .00043 if key == "loss" else .612 + (i - 3) ** 2 * .000022}
                      for i in range(18)]
            # A realistic small server page proves history collection follows tokens.
            count = min(maximum, 7)
            result = {"metrics": values[start:start + count]}
            if start + count < len(values):
                result["next_page_token"] = str(start + count)
            self.respond(result)
        else:
            old_get(self)

    handler.do_GET = get
    try:
        with tempfile.TemporaryDirectory(prefix="lazymlflow-inspection-pty-") as directory:
            root = Path(directory)
            config = root / "config.toml"
            config.write_text(f'default_target = "inspect"\n[tui]\nrefresh_seconds = 1\n'
                              f'[[targets]]\nid = "inspect"\ntracking_uri = "{fixture.url}"\n')
            editor = root / "edit_note.py"
            editor.write_text("import pathlib,sys\np=pathlib.Path(sys.argv[1])\np.write_text(p.read_text()+'\\nExternal editor verified')\n")
            env = {k: v for k, v in os.environ.items() if k not in ("LAZYMLFLOW_TARGET", "MLFLOW_TRACKING_URI", "VISUAL")}
            env.update(TERM="xterm-256color", NO_COLOR="1", XDG_CONFIG_HOME=str(root / "config"),
                       XDG_DATA_HOME=str(root / "data"), XDG_STATE_HOME=str(root / "state"),
                       EDITOR=f"{sys.executable} {editor}")
            terminal = Terminal([binary, "--config", str(config)], env, root)
            terminal.wait(lambda: "inspect-run-0" in terminal.text(), "initial run")
            terminal.send("3]z")
            history_count = lambda: sum(path.endswith("metrics/get-history") for path, _ in fixture.events)
            terminal.wait(lambda: history_count() >= 3, "paginated selected history")
            terminal.wait(lambda: history_count() >= 6, "training auto refresh", seconds=8)
            fixture.status = "FINISHED"
            terminal.wait(lambda: any(path.endswith("runs/get") for path, _ in fixture.events), "run status refresh")
            # Wait for the terminal-status observation and final history request.
            end = time.monotonic() + 2.5
            while time.monotonic() < end:
                terminal.pump(.05)
            stopped_count = history_count()
            end = time.monotonic() + 1.4
            while time.monotonic() < end:
                terminal.pump(.05)
            assert history_count() == stopped_count, "completed run kept polling histories"
            terminal.send("\rha")
            terminal.wait(lambda: "Cursor" in terminal.text(), "expanded curve cursor")
            terminal.click(30, 8)
            terminal.send("p \x1b[B \r")
            terminal.wait(lambda: any(path.endswith("metrics/get-history") and q.get("metric_key") == ["valid_loss"]
                                      for path, q in fixture.events), "explicit metric overlay")
            terminal.send("\x1b")
            terminal.send("v")
            terminal.wait(lambda: "dashboard · 2 metrics" in terminal.text(), "dashboard")
            terminal.resize(80, 24)
            terminal.resize(150, 40)
            terminal.send("v]/lr\r\r")
            terminal.wait(lambda: "Full value" in terminal.text(), "parameter full value")
            terminal.send("\x1b")
            terminal.send("]]]")
            terminal.wait(lambda: "146 columns" in terminal.text(), "schema count")
            terminal.send("/feature_145\r")
            terminal.wait(lambda: "1 / 146 matches" in terminal.text(), "schema search")
            terminal.send("N")
            terminal.wait(lambda: "Local journal" in terminal.text(), "dataset journal")
            terminal.send("c")
            terminal.send("\x1b[200~Observation q j 1 B N S\nValidation loss rose.\x1b[201~")
            terminal.send("\x05")
            terminal.wait(lambda: "Editor returned" in terminal.text(), "external editor return")
            terminal.send("\x13")
            state = root / "data" / "lazymlflow" / "state.db"

            def stored_note():
                try:
                    with sqlite3.connect(f"file:{state}?mode=ro", uri=True, timeout=.1) as db:
                        return db.execute("SELECT kind,body FROM notes WHERE deleted_at=0").fetchall()
                except sqlite3.Error:
                    return []

            terminal.wait(lambda: any(kind == "dataset" and "External editor verified" in body
                                      for kind, body in stored_note()), "note saved to local SQLite")
            assert any("q j 1 B N S" in body for _, body in stored_note()), "editing invoked navigation"
            terminal.send("\x1b")
            terminal.send("B")
            terminal.send("A")
            terminal.wait(lambda: "Complete as of" in terminal.text(), "full dataset scan")
            # Pick the first variant when the initial selected row is a name group.
            terminal.send("1\x1b[Hj")
            terminal.send("2")
            terminal.wait(lambda: "6 visible runs" in terminal.text(), "cross-experiment relationships")
            terminal.send("S")
            terminal.wait(lambda: "Markdown report" in terminal.text(), "shared summary context", seconds=12)
            terminal.send("p")
            terminal.wait(lambda: "Agent prompt" in terminal.text(), "prompt preview")
            terminal.send("p")
            terminal.send("e\x01\x0b" + str(root / "summary.md") + "\r")
            terminal.wait((root / "summary.md").exists, "summary export")
            assert "External editor verified" in (root / "summary.md").read_text(), "report omitted dataset journal"
            terminal.send("\x1b")
            terminal.send("\r")
            terminal.wait(lambda: "Related run loaded" in terminal.text(), "related run navigation")
            terminal.send("q")
            terminal.process.wait(8)
            assert terminal.process.returncode == 0, terminal.text()[-2000:]
            terminal.close()
            terminal = None
            assert all(int(q.get("max_results", ["0"])[0]) > 0 for path, q in fixture.events
                       if path.endswith("metrics/get-history")), "history page size omitted"
            print("PASS v0.3 PTY: curves, cursor/mouse, overlays, live completion, schema search, floating notes/editor, dataset scan/relationships, summary/prompt/export, cleanup")
    finally:
        if terminal:
            terminal.close()
        fixture.close()


if __name__ == "__main__":
    main()
