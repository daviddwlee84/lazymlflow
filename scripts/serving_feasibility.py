#!/usr/bin/env python3
# /// script
# requires-python = ">=3.11,<3.14"
# dependencies = ["mlflow==3.16.1", "scikit-learn==1.7.2", "pandas==2.3.3"]
# ///
"""Synthetic-only MLflow serving feasibility study; no lazymlflow serving command.

  uv run --no-project scripts/serving_feasibility.py

Creates three small synthetic models, starts one bounded loopback server at a
 time with --env-manager local, checks HTTP predictions, and removes all state.
This intentionally loads only model code created by this script, never a user
artifact. Explicit recurrent state travels in each request and response.
"""
import argparse
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


def unused_port():
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


def captured_process(command, *, cwd, env, timeout):
    process = subprocess.Popen(command, cwd=cwd, env=env, stdout=subprocess.PIPE,
                               stderr=subprocess.PIPE, text=True, start_new_session=True)
    try:
        stdout, stderr = process.communicate(timeout=timeout)
        return subprocess.CompletedProcess(command, process.returncode, stdout, stderr)
    finally:
        stop(process)


def wait_ready(uri, process):
    deadline = time.monotonic() + 60
    while time.monotonic() < deadline:
        if process.poll() is not None:
            raise RuntimeError(f"serving subprocess exited: {process.returncode}")
        try:
            with urllib.request.urlopen(uri + "/ping", timeout=1) as response:
                if response.status == 200:
                    return
        except (OSError, urllib.error.URLError):
            pass
        time.sleep(0.1)
    raise TimeoutError("temporary model server readiness exceeded 60 seconds")


def invoke(uri, columns, rows):
    request = urllib.request.Request(uri + "/invocations",
        data=json.dumps({"dataframe_split": {"columns": columns, "data": rows}}).encode(),
        headers={"Content-Type": "application/json"}, method="POST")
    with urllib.request.urlopen(request, timeout=10) as response:
        return json.load(response)["predictions"]


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--json", action="store_true", help="Print the experiment result as JSON")
    args = parser.parse_args()
    assert importlib.metadata.version("mlflow") == "3.16.1", "run with uv and the pinned script dependencies"
    with tempfile.TemporaryDirectory(prefix="lazymlflow-serving-study-") as directory:
        root = Path(directory)
        env = {k: v for k, v in os.environ.items() if not k.startswith(("MLFLOW_", "LAZYMLFLOW_"))}
        env.update(MLFLOW_TRACKING_URI="sqlite:///" + str(root / "tracking.db"),
                   MLFLOW_REGISTRY_URI="sqlite:///" + str(root / "tracking.db"),
                   MLFLOW_ENABLE_TELEMETRY="false", MLFLOW_DISABLE_AGENT_HINT="1", DO_NOT_TRACK="1",
                   MLFLOW_SERVER_ENABLE_JOB_EXECUTION="false", MLFLOW_ENV_ROOT=str(root / "model-envs"), UV_NO_CONFIG="1", NO_PROXY="127.0.0.1,localhost",
                   XDG_CONFIG_HOME=str(root / "config"), XDG_STATE_HOME=str(root / "state"),
                   XDG_CACHE_HOME=str(root / "cache"))
        for key in list(os.environ):
            if key.startswith(("MLFLOW_", "LAZYMLFLOW_")):
                os.environ.pop(key)
        os.environ.update({k: v for k, v in env.items() if k.startswith("MLFLOW_") or k in {"NO_PROXY", "DO_NOT_TRACK"}})
        import mlflow
        import mlflow.pyfunc
        import mlflow.sklearn
        import pandas as pd
        from sklearn.linear_model import LinearRegression

        class Stateless(mlflow.pyfunc.PythonModel):
            def predict(self, context, model_input, params=None):
                return pd.DataFrame({"prediction": model_input["x"].astype(float) * 2})

        class ExplicitState(mlflow.pyfunc.PythonModel):
            def predict(self, context, model_input, params=None):
                # Per-stream state is supplied by the caller; workers own no session state.
                next_hidden = model_input["hidden"].astype(float) * 0.5 + model_input["x"].astype(float)
                return pd.DataFrame({"prediction": next_hidden * 2, "next_hidden": next_hidden})

        deps = [f"mlflow=={importlib.metadata.version('mlflow')}",
                f"pandas=={importlib.metadata.version('pandas')}",
                f"cloudpickle=={importlib.metadata.version('cloudpickle')}"]
        x = pd.DataFrame({"x": [0.0, 1.0, 2.0]})
        sklearn = LinearRegression().fit(x, [0.0, 2.0, 4.0])
        mlflow.sklearn.save_model(sklearn, str(root / "sklearn"),
            pip_requirements=[*deps, f"scikit-learn=={importlib.metadata.version('scikit-learn')}"],
            signature=mlflow.models.infer_signature(x, sklearn.predict(x)))
        mlflow.pyfunc.save_model(str(root / "stateless"), python_model=Stateless(), pip_requirements=deps,
            signature=mlflow.models.infer_signature(x, pd.DataFrame({"prediction": [0.0, 2.0, 4.0]})))
        state_input = pd.DataFrame({"x": [1.0], "hidden": [0.0]})
        mlflow.pyfunc.save_model(str(root / "explicit-state"), python_model=ExplicitState(), pip_requirements=deps,
            signature=mlflow.models.infer_signature(state_input, pd.DataFrame({"prediction": [2.0], "next_hidden": [1.0]})))
        # Recreate one recorded environment with MLflow's uv manager; do not
        # substitute the already-running script environment for this check.
        input_path = root / "predict-input.json"
        input_path.write_text(json.dumps({"dataframe_split": {"columns": ["x"], "data": [[3.0]]}}))
        predict_results = {}
        for manager in ("local", "uv"):
            output_path = root / f"predict-{manager}.json"
            prediction = captured_process([sys.executable, "-m", "mlflow", "models", "predict",
                "--model-uri", str(root / "sklearn"), "--input-path", str(input_path),
                "--content-type", "json", "--output-path", str(output_path), "--env-manager", manager],
                cwd=root, env=env, timeout=240)
            if prediction.returncode:
                raise RuntimeError(f"{manager} prediction failed:\n{prediction.stderr[-6000:]}")
            values = json.loads(output_path.read_text())
            if isinstance(values, dict):
                values = values["predictions"]
            assert abs(values[0] - 6.0) < 1e-9, values
            predict_results[manager] = values[0]
        cases = []
        for name in ("sklearn", "stateless", "explicit-state"):
            uri = f"http://127.0.0.1:{unused_port()}"
            log_path = root / f"{name}.log"
            with log_path.open("wb") as log:
                process = subprocess.Popen([sys.executable, "-m", "mlflow", "models", "serve",
                    "--model-uri", str(root / name), "--host", "127.0.0.1", "--port", uri.rsplit(":", 1)[1],
                    "--env-manager", "local"], cwd=root, env=env, stdout=log, stderr=log, start_new_session=True)
                try:
                    wait_ready(uri, process)
                    if name == "sklearn":
                        rejected = []
                        for label, columns, rows in (
                            ("missing-input", ["wrong"], [[3.0]]),
                            ("wrong-dtype", ["x"], [["not-numeric"]]),
                            ("wrong-shape", ["x"], [[3.0, 4.0]]),
                        ):
                            try:
                                invoke(uri, columns, rows)
                            except urllib.error.HTTPError as error:
                                assert error.code in (400, 422), error.code
                                rejected.append(label)
                            else:
                                raise AssertionError(f"signature accepted {label}")
                    if name == "explicit-state":
                        first = invoke(uri, ["x", "hidden"], [[1.0, 0.0]])
                        repeated = invoke(uri, ["x", "hidden"], [[1.0, 0.0]])
                        second = invoke(uri, ["x", "hidden"], [[1.0, first[0]["next_hidden"]]])
                        assert first == repeated == [{"prediction": 2.0, "next_hidden": 1.0}]
                        assert second == [{"prediction": 3.0, "next_hidden": 1.5}]
                        cases.append({"case": name, "passed": True, "first": first, "second": second,
                                      "state_ownership": "caller; explicit input/output per stream"})
                    else:
                        output = invoke(uri, ["x"], [[3.0]])
                        value = output[0] if name == "sklearn" else output[0]["prediction"]
                        assert abs(value - 6.0) < 1e-9, output
                        cases.append({"case": name, "passed": True, "prediction": value})
                except BaseException:
                    print(log_path.read_text(errors="replace")[-6000:], file=sys.stderr)
                    raise
                finally:
                    stop(process)
        result = {"mlflow": importlib.metadata.version("mlflow"), "python": sys.version.split()[0],
                  "scope": "synthetic local HTTP and recreated-environment smoke tests", "cases": cases,
                  "local_predict": predict_results["local"], "uv_recreated_predict": predict_results["uv"],
                  "signature_rejections": rejected,
                  "limitations": ["No production model evaluated", "No latency or throughput benchmark",
                                  "No Python-free embedded-runtime compatibility proof",
                                  "No server-side per-stream state manager", "No deployment lifecycle implemented"]}
        print(json.dumps(result, indent=2) if args.json else "PASS synthetic MLflow 3.16.1: local/uv recreated prediction, sklearn/stateless/explicit-state serving, signature rejection; temporary environments and processes cleaned up")


if __name__ == "__main__":
    main()
