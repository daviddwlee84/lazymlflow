#!/usr/bin/env python3
"""Real-terminal Activity/metric-preference acceptance, with disposable state."""
import argparse
from copy import deepcopy
import json
import os
from pathlib import Path
import re
import sqlite3
import tempfile
import time
import tomllib
from urllib.parse import parse_qs, urlparse

from pty_smoke import Fixture, Terminal


class ActivityFixture(Fixture):
    """Exercise real multi-experiment filters, ordering and opaque pagination."""

    def __init__(self):
        super().__init__("activity")
        now = int(time.time() * 1000)
        self.ids = {name: f"{index:032x}" for index, name in enumerate(
            ("train", "complete", "failed", "other", "running"), 1)}
        self.records = {}
        for name, experiment, status, start, end in (
            ("train", "1", "RUNNING", now - 300000, 0),
            ("complete", "1", "FINISHED", now - 250000, now - 1000),
            ("failed", "2", "FAILED", now - 220000, now - 2000),
            ("other", "2", "FINISHED", now - 200000, now - 3000),
            ("running", "2", "RUNNING", now - 100000, 0),
        ):
            metrics = [{"key": "train_loss", "value": .3, "step": 10, "timestamp": now - 10000}]
            if name == "complete":
                metrics.append({"key": "valid_corr", "value": .8, "step": 10, "timestamp": now - 9000})
            if name == "failed":
                metrics.append({"key": "valid_corr", "value": "NaN", "step": 11, "timestamp": now - 8000})
            self.records[self.ids[name]] = {
                "info": {"run_id": self.ids[name], "experiment_id": experiment,
                         "run_name": "activity-" + name, "status": status,
                         "start_time": start, "end_time": end, "lifecycle_stage": "active",
                         "artifact_uri": f"mlflow-artifacts:/{experiment}/{self.ids[name]}/artifacts"},
                "data": {"metrics": metrics, "params": [{"key": "lr", "value": "0.01"}], "tags": []},
            }
        fixture = self
        original = self.server.RequestHandlerClass

        class Handler(original):
            def do_POST(self):
                payload = json.loads(self.rfile.read(int(self.headers.get("Content-Length", 0))))
                fixture.events.append((self.path, payload))
                if self.path.endswith("experiments/search"):
                    experiments = [{"experiment_id": "1", "name": "Alpha metrics", "lifecycle_stage": "active"},
                                   {"experiment_id": "2", "name": "Beta alerts", "lifecycle_stage": "active"}]
                    offset = fixture.offset(payload.get("page_token", ""))
                    page = experiments[offset:offset + 1]
                    result = {"experiments": page}
                    if offset + len(page) < len(experiments):
                        result["next_page_token"] = f"opaque:{offset + len(page)}"
                    self.respond(result)
                    return
                if not self.path.endswith("runs/search"):
                    self.respond({"message": "unsupported operation"}, 404)
                    return
                if fixture.delay:
                    time.sleep(fixture.delay)
                runs = [deepcopy(run) for run in list(fixture.records.values())
                        if run["info"]["experiment_id"] in payload["experiment_ids"]]
                expression = payload.get("filter", "")
                if expression:
                    match = re.fullmatch(r"attributes\.(status|start_time|end_time)\s*(=|>=|>)\s*(?:'([^']*)'|(\d+))", expression)
                    if not match:
                        self.respond({"error_code": "INVALID_PARAMETER_VALUE", "message": "unsupported fixture filter: " + expression}, 400)
                        return
                    field, op, status, number = match.groups()
                    value = status if field == "status" else int(number)
                    def matches(run):
                        actual = run["info"].get(field, 0)
                        return actual == value if op == "=" else actual >= value if op == ">=" else actual > value
                    runs = [run for run in runs if matches(run)]
                for order in reversed(payload.get("order_by", ["attributes.start_time DESC"])):
                    field, direction = order.split()
                    field = field.removeprefix("attributes.")
                    runs.sort(key=lambda run: run["info"].get(field, 0), reverse=direction == "DESC")
                offset = fixture.offset(payload.get("page_token", ""))
                limit = min(2, payload.get("max_results", 2))
                page = runs[offset:offset + limit]
                result = {"runs": page}
                if offset + len(page) < len(runs):
                    result["next_page_token"] = f"opaque:{offset + len(page)}"
                self.respond(result)

            def do_GET(self):
                url = urlparse(self.path)
                if not url.path.endswith("metrics/get-history"):
                    return super().do_GET()
                query = parse_qs(url.query)
                fixture.events.append((url.path, query))
                run = fixture.get_run(query["run_id"][0])
                key = query["metric_key"][0]
                latest = next((metric for metric in run["data"]["metrics"] if metric["key"] == key), None)
                if latest is None:
                    self.respond({"metrics": []})
                else:
                    self.respond({"metrics": [dict(latest, step=i, timestamp=latest["timestamp"] - (10-i)*1000)
                                              for i in range(1, 11)]})

        self.server.RequestHandlerClass = Handler

    @staticmethod
    def offset(token):
        return int(token.split(":")[1]) if token else 0

    def get_run(self, run_id):
        return deepcopy(self.records[run_id])

    def update(self, name, status=None, metric=None):
        run = self.get_run(self.ids[name])
        now = int(time.time() * 1000)
        if status is not None:
            run["info"]["status"] = status
            run["info"]["end_time"] = 0 if status == "RUNNING" else now
        if metric is not None:
            run["data"]["metrics"] = [v for v in run["data"]["metrics"] if v["key"] != metric[0]]
            run["data"]["metrics"].append({"key": metric[0], "value": metric[1], "step": 12, "timestamp": now})
        self.records[self.ids[name]] = run

    def histories(self, offset=0):
        return [(q["run_id"][0], q["metric_key"][0]) for path, q in self.events[offset:]
                if path.endswith("metrics/get-history")]


def read_json_rows(path, table, identity):
    if not path.exists():
        return {}
    try:
        with sqlite3.connect(f"file:{path}?mode=ro", uri=True, timeout=.1) as db:
            return {key: json.loads(value) for key, value in db.execute(f"SELECT {identity},value FROM {table}")}
    except sqlite3.Error:
        return {}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--binary", default="./bin/lazymlflow")
    args = parser.parse_args()
    binary = str(Path(args.binary).resolve())
    fixture, terminal = ActivityFixture(), None
    try:
        with tempfile.TemporaryDirectory(prefix="lazymlflow-activity-pty-") as directory:
            root = Path(directory)
            config = root / "config.toml"
            config.write_text(f'default_target = "activity"\n[[targets]]\nid = "activity"\ntracking_uri = "{fixture.url}"\n'
                              '[activity]\nrefresh_seconds = 0\ninitial_unread_days = 7\nread_on = "open"\n', encoding="utf-8")
            env = {key: value for key, value in os.environ.items() if key not in ("LAZYMLFLOW_TARGET", "MLFLOW_TRACKING_URI")}
            env.update(TERM="xterm-256color", NO_COLOR="1", XDG_CONFIG_HOME=str(root / "config"),
                       XDG_DATA_HOME=str(root / "data"), XDG_STATE_HOME=str(root / "state"))
            state = root / "data" / "lazymlflow" / "state.db"
            records = lambda: read_json_rows(state, "activity_records", "run_id")
            views = lambda: read_json_rows(state, "experiment_views", "experiment")
            unread = lambda: sum(record["revision"] > record["read_revision"] for record in records().values())
            terminal = Terminal([binary, "--config", str(config)], env, root)
            terminal.wait(lambda: len(records()) == 5, "cross-experiment activity baseline")
            terminal.wait(lambda: len(read_json_rows(state, "activity_count_cache", "experiment")) == 2,
                          "background full counts for both experiments")
            assert any(path.endswith("experiments/search") and q.get("page_token") for path, q in fixture.events), "experiment pages not followed"
            assert any(path.endswith("runs/search") and q.get("page_token") for path, q in fixture.events), "run pages not followed"
            assert any(path.endswith("runs/search") and set(q["experiment_ids"]) == {"1", "2"}
                       and q.get("filter") == "attributes.status = 'RUNNING'" for path, q in fixture.events), "Running did not span experiments"

            # The four pinned scopes precede the real experiment rows. Keyboard
            # navigation deliberately avoids assumptions about terminal width.
            for index in range(4):
                terminal.send("1gg" + "j" * index + "\r")
            terminal.send("1ggjjjj\r2gg3]")
            terminal.wait(lambda: (fixture.ids["complete"], "train_loss") in fixture.histories(), "metric table on completed run")
            terminal.send("*")
            terminal.wait(lambda: views().get("1", {}).get("metric_pins") == ["train_loss"], "first pin saved")
            terminal.send("j*<>")
            terminal.wait(lambda: views().get("1", {}).get("metric_pins") == ["train_loss", "valid_corr"], "pin order saved")
            terminal.send("p \x1b[B \r")
            terminal.wait(lambda: (fixture.ids["complete"], "valid_corr") in fixture.histories(), "two-metric overlay loaded")
            terminal.send("p \x1b")  # Cancel a draft change; shared selection stays intact.
            mark = len(fixture.events)
            terminal.send("2j")
            terminal.wait(lambda: (fixture.ids["train"], "train_loss") in fixture.histories(mark), "overlay inherited by training run")
            assert (fixture.ids["train"], "valid_corr") not in fixture.histories(mark), "missing metric fetched"
            fixture.update("train", metric=("valid_corr", .65))
            terminal.send("3r")
            terminal.wait(lambda: (fixture.ids["train"], "valid_corr") in fixture.histories(mark), "late validation metric joined existing overlay")
            assert views().get("1", {}).get("metric_pins") == ["train_loss", "valid_corr"]

            # Read state comes from SQLite, not stale text accumulated by the
            # terminal renderer. Selecting unread runs does not mark them read.
            terminal.send("W")
            terminal.wait(lambda: unread() == 0, "mark-all-read baseline")
            fixture.update("train", status="FINISHED")
            fixture.update("running", status="FINISHED")
            terminal.send("Ir")
            terminal.wait(lambda: unread() >= 2, "new terminal transitions became unread")
            before = unread()
            terminal.send("JK")
            assert unread() == before, "unread navigation marked rows read under open policy"
            terminal.send("\r")
            terminal.wait(lambda: unread() == before - 1, "Enter marks exactly the opened run read")
            terminal.send("W")
            terminal.wait(lambda: unread() == 0, "mark all current target inbox read")

            terminal.send("1ggjjj\ra")
            def acknowledged():
                alerts = records().get(fixture.ids["failed"], {}).get("alerts", [])
                return len(alerts) >= 2 and all(alert["acknowledged"] for alert in alerts)
            terminal.wait(acknowledged, "acknowledge failed run without deleting it")
            assert fixture.ids["failed"] in records(), "acknowledgment removed the run"
            assert not any(alert["active"] and not alert["acknowledged"] for alert in records()[fixture.ids["failed"]]["alerts"])
            fixture.update("failed", metric=("valid_corr", "NaN"))
            terminal.send("r")
            terminal.wait(lambda: any(metric["key"] == "valid_corr" and metric["step"] == 12
                                      for metric in records()[fixture.ids["failed"]]["metrics"]), "ongoing nonfinite sample refreshed")
            assert acknowledged(), "ongoing nonfinite sample reopened acknowledged episode"

            mark = len(terminal.raw)
            terminal.send("!")
            terminal.wait(lambda: "mark read on" in terminal.text(mark), "activity settings opened")
            terminal.send("jj\r")
            terminal.wait(lambda: tomllib.loads(config.read_text()).get("activity", {}).get("metric_updates") is True,
                          "activity preference saved")
            terminal.send("\x1b")
            terminal.send("2/qjIJKWa!*<>")
            assert terminal.process.poll() is None, "typed shortcut text escaped its field"
            terminal.send("\x1b")
            for width, height in ((80, 24), (38, 12), (12, 4), (140, 42)):
                terminal.resize(width, height)
                terminal.pump(.1)
            terminal.send("q")
            terminal.process.wait(8)
            assert terminal.process.returncode == 0
            terminal.close()
            terminal = None

            # Durable pins survive restart; overlay choices are session-only.
            mark = len(fixture.events)
            terminal = Terminal([binary, "--config", str(config)], env, root)
            terminal.wait(lambda: any(path.endswith("runs/search") and q["experiment_ids"] == ["1"]
                                     for path, q in fixture.events[mark:]), "restart real experiment loaded")
            terminal.send("1ggjjjj\r2gg3]")
            terminal.wait(lambda: (fixture.ids["complete"], "train_loss") in fixture.histories(mark), "saved first pin restored")
            assert (fixture.ids["complete"], "valid_corr") not in fixture.histories(mark), "overlay persisted across restart"
            assert views()["1"]["metric_pins"] == ["train_loss", "valid_corr"]
            fixture.delay = .75
            terminal.send("Ir")
            terminal.resize(80, 24)
            terminal.send("q")
            terminal.process.wait(8)
            assert terminal.process.returncode == 0, "quit failed during Activity refresh"
            terminal.close()
            terminal = None
            print("PASS Activity PTY: paginated cross-experiment scopes, J/K/open/read-all, acknowledgments, settings, metric overlays/missing keys/pins/restart, typing, resize, terminal cleanup")
    finally:
        if terminal:
            terminal.close()
        fixture.close()


if __name__ == "__main__":
    main()
