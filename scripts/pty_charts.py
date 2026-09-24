#!/usr/bin/env python3
"""Real-PTY chart profiles, transformations and Activity overlay acceptance."""
import argparse
import os
from pathlib import Path
import tempfile

from pty_smoke import Terminal
from pty_activity import ActivityFixture, read_json_rows


def repaint(terminal):
    terminal.send("?")
    mark = len(terminal.raw)
    terminal.send("\x1b")
    return mark


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--binary", default="./bin/lazymlflow")
    binary = str(Path(parser.parse_args().binary).resolve())
    fixture, terminal = ActivityFixture(), None
    fixture.update("running", metric=("valid_corr", .7))
    try:
        with tempfile.TemporaryDirectory(prefix="lazymlflow-charts-pty-") as directory:
            root = Path(directory)
            config = root / "config.toml"
            config.write_text(f'default_target = "activity"\n[[targets]]\nid = "activity"\ntracking_uri = "{fixture.url}"\n'
                              '[activity]\nrefresh_seconds = 0\ninitial_unread_days = 7\nread_on = "open"\n', encoding="utf-8")
            env = {key: value for key, value in os.environ.items() if key not in ("LAZYMLFLOW_TARGET", "MLFLOW_TRACKING_URI")}
            env.update(TERM="xterm-256color", NO_COLOR="1", XDG_CONFIG_HOME=str(root / "config"),
                       XDG_DATA_HOME=str(root / "data"), XDG_STATE_HOME=str(root / "state"))
            state = root / "data" / "lazymlflow" / "state.db"
            terminal = Terminal([binary, "--config", str(config)], env, root)
            terminal.resize(160, 48)
            terminal.wait(lambda: len(read_json_rows(state, "activity_records", "run_id")) == 5, "Activity fixture baseline")
            terminal.send("1ggjjjj\r2gg3]")
            terminal.wait(lambda: (fixture.ids["complete"], "train_loss") in fixture.histories(), "normal experiment chart")
            terminal.send("p \x1b[B \r")
            terminal.wait(lambda: (fixture.ids["complete"], "valid_corr") in fixture.histories(), "normal experiment overlay")

            # The first Running row belongs to the other experiment. It should
            # inherit the normal chart once and then have independent options.
            mark = len(fixture.events)
            terminal.send("1gg\r3")
            terminal.wait(lambda: (fixture.ids["running"], "valid_corr") in fixture.histories(mark), "Activity overlay crossed experiments")
            mark = len(fixture.events)
            terminal.send("0r")
            terminal.wait(lambda: (fixture.ids["running"], "train_loss") in fixture.histories(mark), "overlay off uses one metric")
            assert (fixture.ids["running"], "valid_corr") not in fixture.histories(mark), "disabled overlay still fetched the second metric"
            terminal.send("0")
            terminal.wait(lambda: (fixture.ids["running"], "valid_corr") in fixture.histories(mark), "overlay on restored selected keys")

            terminal.send("Fn")
            mark = repaint(terminal)
            terminal.wait(lambda: "EMA 10 samples" in terminal.text(mark) and "per-series 0–1" in terminal.text(mark), "EMA and normalization visible")
            terminal.send("d")
            mark = repaint(terminal)
            terminal.wait(lambda: "Raw logged points only" in terminal.text(mark), "raw points drawing")
            terminal.send("d")
            mark = repaint(terminal)
            terminal.wait(lambda: "EMA line only" in terminal.text(mark), "EMA lines drawing")
            terminal.send("dbu")  # Both layers, extrema, ASCII; NO_COLOR is set.
            mark = repaint(terminal)
            terminal.wait(lambda: "v min / ^ max / * both" in terminal.text(mark) and "Δ9 steps" in terminal.text(mark), "raw extrema and distance annotation")
            terminal.send("f\x1b[B\x1b[C\x1b[B\x1b[B\x1b[B\x1b[B\r\x1b")
            mark = repaint(terminal)
            terminal.wait(lambda: "EMA 11 samples" in terminal.text(mark) and "Best (min)" in terminal.text(mark), "editable EMA span and best direction")

            mark = len(fixture.events)
            terminal.send("2j")
            terminal.wait(lambda: (fixture.ids["train"], "train_loss") in fixture.histories(mark), "Running next experiment keeps chart profile")
            assert (fixture.ids["train"], "valid_corr") not in fixture.histories(mark), "missing selected key fetched"
            fixture.update("train", metric=("valid_corr", .6))
            terminal.send("3r")
            terminal.wait(lambda: (fixture.ids["train"], "valid_corr") in fixture.histories(mark), "late metric joins inherited overlay")
            mark = repaint(terminal)
            terminal.wait(lambda: "EMA 11 samples" in terminal.text(mark), "profile survived cross-experiment run switch")

            terminal.send("p0\x1b")
            mark = repaint(terminal)
            terminal.wait(lambda: "Shared Y axis" in terminal.text(mark), "overlay draft clear was cancelled")
            terminal.send("p0\r")
            mark = repaint(terminal)
            terminal.wait(lambda: "train_loss" in terminal.text(mark), "overlay draft clear applied")
            assert "Shared Y axis" not in terminal.text(mark), "cleared overlay still displayed multiple series"
            terminal.send("f\x1b[F\r\x1b")
            mark = repaint(terminal)
            terminal.wait(lambda: "Digits = raw logged samples" in terminal.text(mark), "chart reset restored raw defaults")
            assert "EMA 11 samples" not in terminal.text(mark) and "per-series 0–1" not in terminal.text(mark), "reset retained transforms"

            terminal.send("1ggjjjj\r2gg3")
            mark = repaint(terminal)
            terminal.wait(lambda: "Shared Y axis" in terminal.text(mark), "leaving Activity restored experiment overlay")
            assert "EMA 11 samples" not in terminal.text(mark) and "per-series 0–1" not in terminal.text(mark), "Activity options overwrote experiment options"
            # Comparison is full-screen but entered from the run pane. Its
            # hidden stored focus must not disable the chart shortcuts.
            terminal.send("2gg j cvFndb")
            mark = repaint(terminal)
            terminal.wait(lambda: "EMA 10 samples" in terminal.text(mark) and "Raw logged points only" in terminal.text(mark),
                          "comparison controls work without changing hidden pane focus")
            terminal.send("c3/q0fFndbp")
            assert terminal.process.poll() is None, "chart shortcut escaped text input"
            terminal.send("\x1b")
            for width, height in ((80, 24), (38, 12), (160, 48)):
                terminal.resize(width, height)
                terminal.pump(.1)
            terminal.send("q")
            terminal.process.wait(8)
            assert terminal.process.returncode == 0
            terminal.close()
            terminal = None
            print("PASS Chart PTY: Activity inheritance/isolation, overlay off/restore/clear, EMA span, normalization, points/lines/ASCII, raw extrema/best distances, comparison controls, late metrics, reset, typing and resize")
    finally:
        if terminal:
            terminal.close()
        fixture.close()


if __name__ == "__main__":
    main()
