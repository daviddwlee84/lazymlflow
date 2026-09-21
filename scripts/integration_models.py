#!/usr/bin/env python3
"""Test model handoff against a disposable real MLflow 3.16.1 server.

  uv run --no-project --with mlflow==3.16.1 python scripts/integration_models.py

Creates only synthetic files, a temporary SQLite database and a loopback server.
Does not deserialize models, load user config, or contact an existing target.
"""
import argparse
import hashlib
import importlib.metadata
import json
import os
from pathlib import Path
import signal
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request


def port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def stop(process):
    if process.poll() is None:
        os.killpg(process.pid, signal.SIGTERM)
        try:
            process.wait(timeout=10)
        except subprocess.TimeoutExpired:
            os.killpg(process.pid, signal.SIGKILL)
            process.wait(timeout=10)


def ready(uri, process):
    deadline = time.monotonic() + 60
    while time.monotonic() < deadline:
        if process.poll() is not None:
            raise RuntimeError(f"temporary MLflow server exited: {process.returncode}")
        try:
            with urllib.request.urlopen(uri + "/health", timeout=1) as response:
                if response.status == 200:
                    return
        except (OSError, urllib.error.URLError):
            pass
        time.sleep(0.1)
    raise TimeoutError("temporary MLflow server readiness exceeded 60 seconds")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--binary", default="./bin/lazymlflow")
    args = parser.parse_args()
    binary = str(Path(args.binary).resolve())
    assert importlib.metadata.version("mlflow") == "3.16.1", "use the pinned MLflow environment"
    with tempfile.TemporaryDirectory(prefix="lazymlflow-model-integration-") as directory:
        root = Path(directory)
        env = {k: v for k, v in os.environ.items() if not k.startswith(("MLFLOW_", "LAZYMLFLOW_"))}
        env.update(MLFLOW_ENABLE_TELEMETRY="false", MLFLOW_DISABLE_AGENT_HINT="1", DO_NOT_TRACK="1",
                   MLFLOW_SERVER_ENABLE_JOB_EXECUTION="false", NO_PROXY="127.0.0.1,localhost",
                   XDG_CONFIG_HOME=str(root / "config"), XDG_STATE_HOME=str(root / "state"),
                   XDG_CACHE_HOME=str(root / "cache"), XDG_DATA_HOME=str(root / "data"))
        os.environ.update({k: v for k, v in env.items() if k.startswith("MLFLOW_") or k == "NO_PROXY"})
        for key in list(os.environ):
            if key.startswith(("MLFLOW_TRACKING_", "MLFLOW_REGISTRY_")):
                os.environ.pop(key)
        from mlflow import MlflowClient
        from mlflow.entities import LoggedModelInput, LoggedModelOutput
        uri = f"http://127.0.0.1:{port()}"
        log_path = root / "server.log"
        with log_path.open("wb") as log:
            process = subprocess.Popen([sys.executable, "-m", "mlflow", "server",
                "--backend-store-uri", "sqlite:///" + str(root / "tracking.db"),
                "--artifacts-destination", str(root / "artifacts"), "--host", "127.0.0.1",
                "--port", uri.rsplit(":", 1)[1], "--workers", "1", "--uvicorn-opts", "--lifespan off"],
                cwd=root, env=env, stdout=log, stderr=log, start_new_session=True)
            try:
                ready(uri, process)
                client = MlflowClient(tracking_uri=uri, registry_uri=uri)
                experiment = client.create_experiment("synthetic-model-handoff")
                run = client.create_run(experiment)
                model_dir = root / "fixture"
                model_dir.mkdir()
                # A declaration for inspection only; this module must never be imported.
                (model_dir / "MLmodel").write_text("flavors:\n  python_function:\n    loader_module: do.not.import\n    env: conda.yaml\n", encoding="utf-8")
                (model_dir / "conda.yaml").write_text("dependencies:\n- python=3.12\n- pip:\n  - synthetic-package==1.0\n", encoding="utf-8")
                (model_dir / "weights.pkl").write_bytes(b"not-a-pickle\x00\xff")
                logged = client.create_logged_model(experiment, name="synthetic", source_run_id=run.info.run_id)
                client.log_model_artifacts(logged.model_id, str(model_dir))
                client.finalize_logged_model(logged.model_id, "READY")
                client.log_inputs(run.info.run_id, models=[LoggedModelInput(logged.model_id)])
                client.log_outputs(run.info.run_id, [LoggedModelOutput(logged.model_id, 0)])
                client.log_artifacts(run.info.run_id, str(model_dir), "raw-checkpoint")
                client.set_terminated(run.info.run_id)
                client.create_registered_model("synthetic")
                version = client.create_model_version("synthetic", f"models:/{logged.model_id}", run_id=run.info.run_id, model_id=logged.model_id, await_creation_for=60)
                client.set_registered_model_alias("synthetic", "candidate", version.version)
                config = root / "targets.toml"
                def call(*arguments, expected=0):
                    command = [binary, "--config", str(config), *arguments]
                    child = subprocess.Popen(command, cwd=root, env=env, stdout=subprocess.PIPE,
                                             stderr=subprocess.PIPE, text=True, start_new_session=True)
                    try:
                        stdout, stderr = child.communicate(timeout=120)
                        result = subprocess.CompletedProcess(command, child.returncode, stdout, stderr)
                    finally:
                        stop(child)
                    if result.returncode != expected:
                        raise AssertionError(f"{arguments}: exit {result.returncode}\n{result.stdout}\n{result.stderr}")
                    return json.loads(result.stdout) if "--json" in arguments and result.stdout.strip() else result.stdout
                call("targets", "add", "fixture", "--uri", uri, "--python", sys.executable)
                prefix = ("--target", "fixture", "models")
                assert call(*prefix, "list", "--all", "--json")["registered_models"][0]["name"] == "synthetic"
                assert call(*prefix, "versions", "synthetic", "--all", "--json")["model_versions"][0]["version"] == str(version.version)
                related = call(*prefix, "related", run.info.run_id, "--json")
                assert related["complete"] and {x["role"] for x in related["models"]} == {"input", "output"}
                assert next(x for x in related["models"] if x["role"] == "output")["step"] == 0
                sources = [f"runs:/{run.info.run_id}/raw-checkpoint", f"models:/{logged.model_id}",
                           f"models:/synthetic/{version.version}", "models:/synthetic@candidate"]
                original = {p.name: hashlib.sha256(p.read_bytes()).hexdigest() for p in model_dir.iterdir()}
                for index, source in enumerate(sources):
                    inspection = call(*prefix, "inspect", source, "--json")
                    assert inspection["metadata"]["runtime_validation"] == "not_run"
                    assert inspection["metadata"]["flavors"] == ["python_function"]
                    assert inspection["metadata"]["environments"][0]["status"] == "read"
                    bundle = root / f"bundle-{index}"
                    result = call(*prefix, "export", source, "--dest", str(bundle), "--json")
                    receipt = Path(result["manifest"])
                    expected = root / f"reviewed-{index}.json"
                    expected.write_bytes(receipt.read_bytes())
                    assert {p.name: hashlib.sha256(p.read_bytes()).hexdigest() for p in (bundle / "payload").iterdir()} == original
                    assert call("models", "verify", str(bundle), "--manifest", str(expected), "--json")["valid"]
                    (bundle / "payload" / "weights.pkl").write_bytes(b"tampered")
                    receipt.write_text("{}")
                    assert not call("models", "verify", str(bundle), "--manifest", str(expected), "--json", expected=1)["valid"]
                weights_inspection = call(*prefix, "inspect", f"runs:/{run.info.run_id}/raw-checkpoint/weights.pkl", "--json")
                assert weights_inspection["metadata"]["status"] == "absent"
                assert weights_inspection["metadata"]["serving"] == "custom-or-unknown"
                # A raw file is exported inside payload, without requiring MLmodel.
                raw = call(*prefix, "export", f"runs:/{run.info.run_id}/raw-checkpoint/weights.pkl", "--dest", str(root / "raw"), "--json")
                assert raw["files"] == 1
                # Direct storage fallback exercises the official artifact CLI without model wrappers.
                local_exp = client.create_experiment("direct-storage", artifact_location=(root / "direct").as_uri())
                direct_run = client.create_run(local_exp)
                client.log_artifacts(direct_run.info.run_id, str(model_dir), "model")
                direct = call(*prefix, "export", f"runs:/{direct_run.info.run_id}/model", "--dest", str(root / "direct-bundle"), "--json", expected=1)
                # Remote file:// is deliberately rejected: its filesystem is not the client's.
                assert direct == "" or isinstance(direct, str)
                call("targets", "add", "local", "--uri", "sqlite:///" + str(root / "tracking.db"),
                     "--python", sys.executable, "--artifacts-destination", str(root / "artifacts"))
                direct = call("--target", "local", "models", "export", f"runs:/{direct_run.info.run_id}/model",
                              "--dest", str(root / "local-direct-bundle"), "--json")
                assert direct["files"] == 3
                print("PASS MLflow 3.16.1: registry/logged/run/alias inspection, relationships, original-byte bundles, offline external-manifest verification; no model deserialization", flush=True)
            except BaseException:
                print(log_path.read_text(errors="replace")[-6000:], file=sys.stderr)
                raise
            finally:
                stop(process)


if __name__ == "__main__":
    main()
