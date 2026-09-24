#!/usr/bin/env python3
"""Exercise the built CLI against disposable, real MLflow stores.

Run in the MLflow environment being tested, for example:
  uv run --no-project --with mlflow==3.16.1 python scripts/integration_mlflow.py
No user configuration or experiment data is used.
"""
import argparse
import hashlib
import importlib.metadata
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import time


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--binary", default="./bin/lazymlflow")
    args = parser.parse_args()
    binary = str(Path(args.binary).resolve())
    os.environ.update(MLFLOW_ALLOW_FILE_STORE="true", MLFLOW_ENABLE_TELEMETRY="false",
                      DO_NOT_TRACK="1", MLFLOW_SERVER_ENABLE_JOB_EXECUTION="false")
    from mlflow.tracking import MlflowClient
    version = importlib.metadata.version("mlflow")
    with tempfile.TemporaryDirectory(prefix="lazymlflow-integration-") as directory:
        root = Path(directory)
        env = {k: v for k, v in os.environ.items()
               if not k.startswith(("MLFLOW_TRACKING", "MLFLOW_REGISTRY", "LAZYMLFLOW_"))}
        env.update(XDG_CONFIG_HOME=str(root / "config"), XDG_CACHE_HOME=str(root / "cache"),
                   XDG_STATE_HOME=str(root / "state"), XDG_DATA_HOME=str(root / "data"), NO_COLOR="1")
        config = root / "targets.toml"
        artifact = root / "seed"
        (artifact / "nested").mkdir(parents=True)
        (artifact / "nested" / "weights.bin").write_bytes(b"\x00\x01\x02MLflow\xff")
        (artifact / "專案 é.txt").write_text("hello MLflow\n", encoding="utf-8")
        recorded = {}
        activity_seeds = {}
        for target, uri in (("sqlite", "sqlite:///" + str(root / "tracking.db")),
                            ("files", (root / "mlruns").as_uri())):
            client = MlflowClient(tracking_uri=uri)
            experiment = client.create_experiment("training 專案", artifact_location=(root / (target + "-artifacts")).as_uri())
            other = client.create_experiment("validation", artifact_location=(root / (target + "-validation-artifacts")).as_uri())
            runs = []
            for index in range(3):
                run = client.create_run(experiment if index < 2 else other,
                                        tags={"mlflow.runName": f"baseline-{index}", "team": "research"})
                run_id = run.info.run_id
                runs.append(run_id)
                client.log_param(run_id, "learning_rate", str(0.01 * (index + 1)))
                for step, value in enumerate([0.9, 0.6, 0.2]):
                    client.log_metric(run_id, "loss", value + index * 0.1, timestamp=1700000000000 + step * 1000, step=step)
                client.log_metric(run_id, "loss", 0.25 + index * 0.1, timestamp=1700000002500, step=2)
                client.log_artifacts(run_id, str(artifact))
                client.set_terminated(run_id)
            recorded[target] = (experiment, runs, uri)

            # Keep Activity cases in separate experiments so the existing
            # two-run filtering/comparison assertions remain unchanged.
            review = client.create_experiment("activity review", artifact_location=(root / (target + "-review-artifacts")).as_uri())
            validation = client.create_experiment("activity validation", artifact_location=(root / (target + "-activity-validation")).as_uri())
            failed = client.create_run(review, tags={"mlflow.runName": "known failure"}).info.run_id
            client.set_terminated(failed, status="FAILED")
            nonfinite = client.create_run(validation, tags={"mlflow.runName": "nonfinite validation"}).info.run_id
            timestamp = int(time.time() * 1000)
            client.log_metric(nonfinite, "valid_corr", float("nan"), timestamp=timestamp, step=1)
            best = client.create_run(review, tags={"mlflow.runName": "producer best metric"}).info.run_id
            client.log_metric(best, "best_corr", .7, timestamp=timestamp, step=1)
            activity_seeds[target] = (review, failed, nonfinite, best, timestamp)

        def call(*arguments, expected=0):
            p = subprocess.run([binary, "--config", str(config), *arguments], env=env,
                               cwd=root, capture_output=True, text=True, timeout=120)
            if p.returncode != expected:
                raise AssertionError(f"{arguments}: exit {p.returncode}\n{p.stdout}\n{p.stderr}")
            if "--json" in arguments and p.stdout.strip():
                return json.loads(p.stdout)
            return p.stdout

        for target, (_, _, uri) in recorded.items():
            call("targets", "add", target, "--uri", uri, "--python", sys.executable)
        before = hashlib.sha256((root / "tracking.db").read_bytes()).hexdigest()
        for target, (experiment, runs, _) in recorded.items():
            print(f"MLflow {version}: {target} experiments/runs/history/artifacts", flush=True)
            page = call("--target", target, "experiments", "list", "--limit", "1", "--all", "--json")
            assert len(page["experiments"]) >= 3
            page = call("--target", target, "runs", "list", experiment, "--order-by", "metrics.loss ASC", "--limit", "1", "--all", "--json")
            assert len(page["runs"]) == 2 and page["runs"][0]["info"]["run_id"] == runs[0]
            page = call("--target", target, "runs", "list", experiment, "--filter", "metrics.loss < 0.3", "--json")
            assert len(page["runs"]) == 1
            comparison = call("--target", target, "runs", "compare", runs[0], runs[2], "--differences", "--json")
            assert len(comparison["runs"]) == 2 and comparison["rows"]
            history = call("--target", target, "metrics", "history", runs[0], "loss", "--json")["metrics"]
            assert len(history) == 4 and sum(p["step"] == 2 for p in history) == 2
            listing = call("--target", target, "artifacts", "ls", runs[0], "--json")
            assert {item["path"] for item in listing["files"]} == {"nested", "專案 é.txt"}
            output = root / (target + "-download")
            download = call("--target", target, "artifacts", "download", runs[0], "--dest", str(output), "--json")
            assert download["files"] == 2
            assert (output / "nested" / "weights.bin").read_bytes() == (artifact / "nested" / "weights.bin").read_bytes()
            call("--target", target, "artifacts", "download", runs[0], "--dest", str(output), "--json", expected=1)
        assert before == hashlib.sha256((root / "tracking.db").read_bytes()).hexdigest(), "read-only SQLite changed"

        for target, (_, baseline_runs, uri) in recorded.items():
            print(f"MLflow {version}: {target} Activity counts/inbox/alerts/subscriptions", flush=True)
            review, failed, nonfinite, best, timestamp = activity_seeds[target]
            client = MlflowClient(tracking_uri=uri)
            expected_ids = set(baseline_runs + [failed, nonfinite, best])

            def activity_list(view):
                return call("--target", target, "activity", "list", "--view", view, "--all", "--json")

            def refresh():
                return call("--target", target, "activity", "refresh", "--json")["activity"]

            def ids(view):
                return {row["run_id"] for row in activity_list(view)["runs"]}

            snapshot = call("--target", target, "activity", "refresh", "--full", "--json")["activity"]
            assert snapshot["complete"] and not snapshot["errors"], snapshot
            assert sum(count["total"] for count in snapshot["counts"].values()) == 6, snapshot["counts"]
            assert all(count["complete"] for count in snapshot["counts"].values())
            assert set(snapshot["records"]) == expected_ids
            assert ids("running") == {nonfinite, best}, "running must span experiments"
            recent = activity_list("recent")["runs"]
            assert {row["run_id"] for row in recent} == set(baseline_runs + [failed])
            assert len({row["experiment_id"] for row in recent}) >= 3
            assert [row["end_time"] for row in recent] == sorted((row["end_time"] for row in recent), reverse=True)

            call("--target", target, "activity", "read", "--all", "--json")
            assert not ids("unread")
            call("--target", target, "activity", "unread", best, "--json")
            assert ids("unread") == {best}
            call("--target", target, "activity", "read", best, "--json")
            rediscovery = call("--target", target, "activity", "unread", "--since", "7d", "--json")
            assert rediscovery["unread"] == 6 and ids("unread") == expected_ids
            call("--target", target, "activity", "read", "--all", "--json")

            assert ids("alerts") == {failed, nonfinite}
            call("--target", target, "activity", "acknowledge", failed, nonfinite, "--json")
            assert not ids("alerts") and ids("acknowledged") == {failed, nonfinite}
            assert ids("all") == expected_ids, "acknowledgment removed a run"
            client.log_metric(nonfinite, "valid_corr", float("nan"), timestamp=timestamp + 1000, step=2)
            refresh()
            assert not ids("alerts"), "ongoing NaN samples reopened an acknowledged episode"
            client.log_metric(nonfinite, "valid_corr", .5, timestamp=timestamp + 2000, step=3)
            refresh()
            client.log_metric(nonfinite, "valid_corr", float("nan"), timestamp=timestamp + 3000, step=4)
            refresh()
            assert ids("alerts") == {nonfinite}, "a new nonfinite episode did not reappear"

            # best_corr is an exact producer-logged key. Value subscriptions
            # compare its returned latest scalar, not each logged sample.
            view = call("--target", target, "view", "set", review,
                        "--notify-metric", "best_corr=value", "--metric-updates", "false", "--json")["view"]
            assert view["activity"]["subscriptions"] == [{"key": "best_corr", "mode": "value"}]
            refresh()  # Establish subscription baseline for this existing run.
            call("--target", target, "activity", "read", "--all", "--json")
            client.log_metric(best, "best_corr", .8, timestamp=timestamp + 4000, step=2)
            refresh()
            unread = activity_list("unread")["runs"]
            assert {row["run_id"] for row in unread} == {best}, unread
            assert "metric:best_corr" in unread[0]["reasons"]
            call("--target", target, "activity", "read", best, "--json")
            client.log_metric(best, "best_corr", .8, timestamp=timestamp + 5000, step=3)
            snapshot = refresh()
            assert not ids("unread"), "equal-value sample triggered a value subscription"
            latest = next(metric for metric in snapshot["records"][best]["metrics"] if metric["key"] == "best_corr")
            assert latest["step"] == 3 and latest["value"] == .8
        missing = root / "missing.db"
        call("--tracking-uri", "sqlite:///" + str(missing), "experiments", "list", "--json", expected=1)
        assert not missing.exists()
        print(f"PASS real MLflow {version}: both stores, queries/comparisons/histories/downloads; read-only SQLite unchanged; "
              "Activity full counts, cross-experiment recent/running, read/unread windows, FAILED/NaN episodes and exact best-metric subscriptions", flush=True)


if __name__ == "__main__":
    main()
