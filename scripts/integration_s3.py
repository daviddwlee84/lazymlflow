#!/usr/bin/env python3
"""Opt-in integration test for proxy and direct S3 artifact access.

Requires MLflow + boto3 in this Python environment and either Docker or a
native MinIO executable:
  go build -o bin/lazymlflow ./cmd/lazymlflow
  uv run --no-project --with mlflow==3.16.1 --with boto3 \
    python scripts/integration_s3.py

The default starts a disposable MinIO container using tmpfs, random loopback
ports, fixture-only credentials, and a temporary MLflow SQLite server. Both
routes receive identical artifact bytes. No existing target, store, bucket,
credential, or Docker volume is used. All services are removed on exit.
Use --minio-executable /path/to/minio to run an existing native MinIO binary
against a temporary directory instead of Docker.
"""

import argparse
import contextlib
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
import uuid


def wait_healthy(url, process=None, timeout=60):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if process is not None and process.poll() is not None:
            raise RuntimeError(f"server exited with status {process.returncode}")
        try:
            with urllib.request.urlopen(url, timeout=1) as response:
                if response.status == 200:
                    return
        except (urllib.error.URLError, TimeoutError, OSError):
            pass
        time.sleep(0.15)
    raise TimeoutError(f"service did not become healthy: {url}")


def random_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def stop_process(process):
    if process.poll() is None:
        os.killpg(process.pid, signal.SIGTERM)
        try:
            process.wait(timeout=10)
        except subprocess.TimeoutExpired:
            os.killpg(process.pid, signal.SIGKILL)
            process.wait(timeout=10)


@contextlib.contextmanager
def native_minio(executable, root):
    endpoint = f"http://127.0.0.1:{random_port()}"
    env = dict(os.environ, MINIO_ROOT_USER="lazymlflow-test",
               MINIO_ROOT_PASSWORD="lazymlflow-test-secret")
    log_path = root / "minio.log"
    with log_path.open("wb") as log:
        process = subprocess.Popen([
            str(Path(executable).resolve()), "server", str(root / "minio-data"),
            "--address", endpoint.removeprefix("http://"),
            "--console-address", f"127.0.0.1:{random_port()}",
        ], env=env, stdout=log, stderr=log, start_new_session=True)
        try:
            wait_healthy(endpoint + "/minio/health/ready", process=process)
            yield endpoint
        except BaseException:
            print(log_path.read_text(errors="replace")[-10000:], file=sys.stderr)
            raise
        finally:
            stop_process(process)


@contextlib.contextmanager
def minio(image):
    name = "lazymlflow-s3-" + uuid.uuid4().hex[:12]
    # A public test image must not ask a user's registry credential helper to
    # unlock a keychain. Preserve the chosen daemon, isolate registry settings.
    host = os.environ.get("DOCKER_HOST") or subprocess.check_output(
        ["docker", "context", "inspect", "--format", "{{.Endpoints.docker.Host}}"],
        text=True, timeout=10,
    ).strip()
    with tempfile.TemporaryDirectory(prefix="lazymlflow-docker-") as directory:
        # A placeholder auth entry suppresses Docker's automatic platform
        # credential-helper detection while providing no actual credentials.
        Path(directory, "config.json").write_text(
            json.dumps({"auths": {"https://index.docker.io/v1/": {}}}), encoding="utf-8"
        )
        docker_env = dict(os.environ, DOCKER_CONFIG=directory)
        docker_env.pop("DOCKER_CONTEXT", None)
        docker = ["docker", "--host", host]
        try:
            result = subprocess.run([
                *docker, "run", "--detach", "--rm", "--name", name,
                "--publish", "127.0.0.1::9000", "--tmpfs", "/data",
                "--env", "MINIO_ROOT_USER=lazymlflow-test",
                "--env", "MINIO_ROOT_PASSWORD=lazymlflow-test-secret",
                image, "server", "/data", "--address", ":9000",
            ], env=docker_env, check=False, capture_output=True, text=True, timeout=240)
            if result.returncode:
                raise RuntimeError("start disposable MinIO: " + result.stderr.strip())
            binding = subprocess.check_output(
                [*docker, "port", name, "9000/tcp"], env=docker_env, text=True, timeout=10
            ).strip()
            endpoint = "http://" + binding
            wait_healthy(endpoint + "/minio/health/ready")
            yield endpoint
        finally:
            subprocess.run([*docker, "rm", "--force", name], env=docker_env,
                           stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                           timeout=30, check=False)


@contextlib.contextmanager
def tracking_server(root, env):
    port = random_port()
    endpoint = f"http://127.0.0.1:{port}"
    command = [
        sys.executable, "-m", "mlflow", "server",
        "--backend-store-uri", "sqlite:///" + str(root / "tracking.db"),
        "--host", "127.0.0.1", "--port", str(port), "--workers", "1",
        "--serve-artifacts", "--artifacts-destination", "s3://lazymlflow-test/proxy",
        "--default-artifact-root", "mlflow-artifacts:/",
    ]
    version = tuple(int(x) for x in importlib.metadata.version("mlflow").split(".")[:2])
    if version >= (3, 5):
        command += ["--uvicorn-opts", "--lifespan off"]
    log_path = root / "mlflow-server.log"
    with log_path.open("wb") as log:
        process = subprocess.Popen(command, env=env, cwd=root, stdout=log,
                                   stderr=log, start_new_session=True)
        try:
            wait_healthy(endpoint + "/health", process=process)
            yield endpoint
        except BaseException:
            print(log_path.read_text(errors="replace")[-10000:], file=sys.stderr)
            raise
        finally:
            stop_process(process)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", default="./bin/lazymlflow")
    parser.add_argument("--image", default="quay.io/minio/minio:RELEASE.2025-04-22T22-12-26Z")
    parser.add_argument("--minio-executable", help="Use a native MinIO binary and temporary data directory instead of Docker")
    args = parser.parse_args()
    binary = str(Path(args.binary).resolve())
    if not Path(binary).is_file():
        parser.error("build lazymlflow first or provide --binary")
    if args.minio_executable and not os.access(args.minio_executable, os.X_OK):
        parser.error("--minio-executable must name an executable native to this machine")
    try:
        import boto3
        os.environ["MLFLOW_DISABLE_AGENT_HINT"] = "1"
        from mlflow.tracking import MlflowClient
    except ImportError:
        parser.error("run with an environment containing mlflow and boto3 (see --help)")

    # Do not let a developer's cloud/tracking credentials influence this test.
    for key in list(os.environ):
        if key.startswith(("AWS_", "MLFLOW_", "LAZYMLFLOW_")):
            del os.environ[key]
    os.environ.update(
        AWS_ACCESS_KEY_ID="lazymlflow-test",
        AWS_SECRET_ACCESS_KEY="lazymlflow-test-secret",
        AWS_DEFAULT_REGION="us-east-1",
        AWS_EC2_METADATA_DISABLED="true",
        NO_PROXY="127.0.0.1,localhost", no_proxy="127.0.0.1,localhost",
        MLFLOW_ENABLE_TELEMETRY="false", DO_NOT_TRACK="1",
        MLFLOW_SERVER_ENABLE_JOB_EXECUTION="false",
    )
    version = importlib.metadata.version("mlflow")
    print(f"Starting disposable MinIO + MLflow {version} integration", flush=True)
    with contextlib.ExitStack() as stack:
        directory = stack.enter_context(tempfile.TemporaryDirectory(prefix="lazymlflow-s3-"))
        root = Path(directory)
        endpoint = stack.enter_context(native_minio(args.minio_executable, root)
                                       if args.minio_executable else minio(args.image))
        os.environ["MLFLOW_S3_ENDPOINT_URL"] = endpoint
        env = dict(os.environ, XDG_CONFIG_HOME=str(root / "config"), XDG_DATA_HOME=str(root / "data"),
                   XDG_CACHE_HOME=str(root / "cache"), XDG_STATE_HOME=str(root / "state"),
                   NO_COLOR="1")
        s3 = boto3.client("s3", endpoint_url=endpoint,
                          aws_access_key_id="lazymlflow-test",
                          aws_secret_access_key="lazymlflow-test-secret",
                          region_name="us-east-1")
        s3.create_bucket(Bucket="lazymlflow-test")
        seed = root / "seed"
        (seed / "nested").mkdir(parents=True)
        (seed / "nested" / "weights.bin").write_bytes(b"\x00\x01S3-MinIO-MLflow\xff")
        (seed / "專案 é.txt").write_text("Identical artifact data through proxy and S3.\n", encoding="utf-8")
        config = root / "targets.toml"

        def call(*arguments, expected=0):
            process = subprocess.run([binary, "--config", str(config), *arguments],
                                     cwd=root, env=env, capture_output=True, text=True,
                                     timeout=120)
            if process.returncode != expected:
                raise AssertionError(
                    f"{arguments}: exit {process.returncode}\n{process.stdout}\n{process.stderr}"
                )
            if "--json" in arguments and process.stdout.strip():
                return json.loads(process.stdout)
            return process.stdout

        with tracking_server(root, env) as tracking:
            client = MlflowClient(tracking_uri=tracking)
            proxy_experiment = client.create_experiment("proxy")
            direct_experiment = client.create_experiment(
                "direct", artifact_location="s3://lazymlflow-test/direct"
            )
            runs = {}
            for route, experiment in (("proxy", proxy_experiment), ("direct", direct_experiment)):
                run = client.create_run(experiment, tags={"mlflow.runName": route})
                runs[route] = run.info.run_id
                client.log_artifacts(run.info.run_id, str(seed))
                client.set_terminated(run.info.run_id)
                if route == "proxy":
                    assert run.info.artifact_uri.startswith("mlflow-artifacts:"), run.info.artifact_uri
                else:
                    assert run.info.artifact_uri.startswith("s3://"), run.info.artifact_uri
            # The proxy target deliberately names an absent runtime. Successful
            # proxy browsing/downloading therefore proves it used native Go REST.
            call("targets", "add", "proxy", "--uri", tracking,
                 "--python", str(root / "no-python-needed"))
            call("targets", "add", "direct", "--uri", tracking, "--python", sys.executable)
            for route, run_id in runs.items():
                print(f"Checking {route}: list, nested directory, Unicode file, full download", flush=True)
                listing = call("--target", route, "artifacts", "ls", run_id, "--json")
                assert {item["path"] for item in listing["files"]} == {"nested", "專案 é.txt"}
                listing = call("--target", route, "artifacts", "ls", run_id, "nested", "--json")
                assert [item["path"] for item in listing["files"]] == ["nested/weights.bin"]
                file_output = root / (route + "-file.txt")
                result = call("--target", route, "artifacts", "download", run_id, "專案 é.txt",
                              "--dest", str(file_output), "--json")
                assert result["files"] == 1 and file_output.read_bytes() == (seed / "專案 é.txt").read_bytes()
                directory_output = root / (route + "-nested")
                result = call("--target", route, "artifacts", "download", run_id, "nested",
                              "--dest", str(directory_output), "--json")
                assert result["files"] == 1
                assert (directory_output / "weights.bin").read_bytes() == (seed / "nested" / "weights.bin").read_bytes()
                full_output = root / (route + "-all")
                result = call("--target", route, "artifacts", "download", run_id,
                              "--dest", str(full_output), "--json")
                assert result["files"] == 2
                assert (full_output / "nested" / "weights.bin").read_bytes() == (seed / "nested" / "weights.bin").read_bytes()
                assert (full_output / "專案 é.txt").read_bytes() == (seed / "專案 é.txt").read_bytes()
                call("--target", route, "artifacts", "download", run_id,
                     "--dest", str(full_output), "--json", expected=1)
        print(f"PASS real MLflow {version} + MinIO: native proxy REST and direct S3 CLI list/file/directory/root downloads", flush=True)


def interrupt(_signum, _frame):
    raise KeyboardInterrupt


if __name__ == "__main__":
    signal.signal(signal.SIGTERM, interrupt)
    main()
