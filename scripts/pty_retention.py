#!/usr/bin/env python3
"""Keep a finishing Activity run readable through real terminal input."""
import argparse
import os
from pathlib import Path
import tempfile

from pty_activity import ActivityFixture, read_json_rows
from pty_smoke import Terminal


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--binary", default="./bin/lazymlflow")
    args = parser.parse_args()
    binary = str(Path(args.binary).resolve())
    fixture, terminal = ActivityFixture(), None
    try:
        with tempfile.TemporaryDirectory(prefix="lazymlflow-retention-pty-") as directory:
            root = Path(directory)
            config = root / "config.toml"
            config.write_text(f'default_target="activity"\n[activity]\nrefresh_seconds=1\n'
                              f'[[targets]]\nid="activity"\ntracking_uri="{fixture.url}"\n')
            env = {key: value for key, value in os.environ.items()
                   if key not in ("LAZYMLFLOW_TARGET", "MLFLOW_TRACKING_URI")}
            env.update(TERM="xterm-256color", NO_COLOR="1", XDG_CONFIG_HOME=str(root / "config"),
                       XDG_DATA_HOME=str(root / "data"), XDG_STATE_HOME=str(root / "state"))
            state = root / "data" / "lazymlflow" / "state.db"
            records = lambda: read_json_rows(state, "activity_records", "run_id")
            terminal = Terminal([binary, "--config", str(config)], env, root)
            terminal.wait(lambda: len(records()) == 5, "initial activity scan")
            terminal.send("1gg\r3]")
            terminal.wait(lambda: (fixture.ids["running"], "train_loss") in fixture.histories(),
                          "selected running metric history")
            mark = len(terminal.raw)
            fixture.update("running", status="FINISHED")
            terminal.wait(lambda: records().get(fixture.ids["running"], {}).get("status") == "FINISHED",
                          "background completion observation")
            terminal.wait(lambda: "kept while reading" in terminal.text(mark), "visible retained row")
            mark = len(fixture.events)
            terminal.send("3r")
            terminal.wait(lambda: (fixture.ids["running"], "train_loss") in fixture.histories(mark),
                          "detail refresh still targets completed selected run")
            terminal.send("?")
            terminal.send("\x1b")
            terminal.send("z")
            terminal.send("z")
            terminal.resize(80, 24)
            terminal.resize(150, 40)
            mark = len(fixture.events)
            terminal.send("2r")
            terminal.wait(lambda: (fixture.ids["train"], "train_loss") in fixture.histories(mark),
                          "manual list refresh advances to remaining running run")
            mark = len(terminal.raw)
            fixture.update("train", status="FINISHED")
            terminal.wait(lambda: records().get(fixture.ids["train"], {}).get("status") == "FINISHED",
                          "last running run completed")
            terminal.wait(lambda: "kept while reading" in terminal.text(mark), "last row remains readable")
            mark = len(terminal.raw)
            terminal.send("2r")
            terminal.wait(lambda: "No matching runs" in terminal.text(mark), "manual cleanup keeps empty Running view")
            terminal.send("q")
            terminal.process.wait(8)
            assert terminal.process.returncode == 0, terminal.text()[-1500:]
            terminal.close()
            terminal = None
            print("PASS retention PTY: background completion, visible retained row, detail refresh, resize, manual cleanup, empty view, terminal restoration")
    finally:
        if terminal:
            terminal.close()
        fixture.close()


if __name__ == "__main__":
    main()
