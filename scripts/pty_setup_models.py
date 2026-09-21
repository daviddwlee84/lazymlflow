#!/usr/bin/env python3
"""Real PTY checks for setup, experiment exports and model handoff.

Uses disposable local HTTP/configuration/files only. Docker and desktop
clipboard tools are replaced with private fixtures; no model code is loaded.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import sys
import tempfile
import tomllib
from urllib.parse import parse_qs, unquote, urlparse

from pty_smoke import Fixture, Terminal


class ModelFixture(Fixture):
    def __init__(self):
        super().__init__("model")
        self.model_name = "pty-model"
        self.model_id = "m-pty-fixture"
        self.files = {
            "MLmodel": b"flavors:\n  python_function:\n    loader_module: do.not.import\n    env: conda.yaml\n",
            "conda.yaml": b"dependencies:\n- python=3.12\n- pip:\n  - synthetic-package==1.0\n",
            "weights.pkl": b"not-a-pickle\x00\xff\x01",
        }
        parent = self.server.RequestHandlerClass
        fixture = self

        class Handler(parent):
            def raw(self, data):
                self.send_response(200)
                self.send_header("Content-Type", "application/octet-stream")
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)

            def do_GET(self):
                parsed = urlparse(self.path)
                path = unquote(parsed.path)
                query = parse_qs(parsed.query)
                version = {"name": fixture.model_name, "version": "1", "status": "READY",
                           "run_id": fixture.ids[0], "source": f"runs:/{fixture.ids[0]}/model",
                           "aliases": ["candidate"]}
                payload = None
                if path.endswith("/registered-models/search"):
                    payload = {"registered_models": [{"name": fixture.model_name}]}
                elif path.endswith("/model-versions/search"):
                    payload = {"model_versions": [version]}
                elif path.endswith(("/model-versions/get", "/registered-models/alias")):
                    payload = {"model_version": version}
                elif path.endswith("/model-versions/get-download-uri"):
                    payload = {"artifact_uri": version["source"]}
                elif path.endswith("/logged-models/" + fixture.model_id):
                    payload = {"model": {"info": {"model_id": fixture.model_id, "name": "Logged PTY model",
                               "source_run_id": fixture.ids[0], "status": "READY", "experiment_id": "1",
                               "artifact_uri": f"mlflow-artifacts:/1/{fixture.ids[0]}/artifacts/model"}}}
                elif path.endswith("/logged-models/" + fixture.model_id + "/artifacts/directories"):
                    payload = {"files": [{"path": name, "is_dir": False, "file_size": len(data)}
                                         for name, data in fixture.files.items()]}
                elif path.endswith("/artifacts/list"):
                    prefix = query.get("path", [""])[0]
                    if prefix == "model":
                        payload = {"files": [{"path": "model/" + name, "is_dir": False, "file_size": len(data)}
                                             for name, data in fixture.files.items()]}
                    elif prefix == "":
                        payload = {"files": [{"path": "model", "is_dir": True}]}
                    else:
                        payload = {"files": []}
                elif "/mlflow-artifacts/artifacts/" in path and "/model/" in path:
                    name = path.rsplit("/model/", 1)[1]
                    if name in fixture.files:
                        fixture.events.append((path, query))
                        self.raw(fixture.files[name])
                        return
                if payload is None:
                    super().do_GET()
                else:
                    fixture.events.append((path, query))
                    self.respond(payload)

        self.server.RequestHandlerClass = Handler

    def run(self, index, experiment):
        run = super().run(index, experiment)
        if index == 0:
            run["inputs"] = {"model_inputs": [{"model_id": self.model_id}]}
            run["outputs"] = {"model_outputs": [{"model_id": self.model_id, "step": 0}]}
        return run


def shown(terminal, text, offset=0):
    terminal.wait(lambda: text in terminal.text(offset), text)


def step(terminal, keys, title):
    offset = len(terminal.raw)
    terminal.send(keys)
    # Bubble Tea can retain an identical first character in a title and paint
    # only its changed suffix. This checks the newly emitted frame, not an old
    # matching title elsewhere in the accumulated PTY transcript.
    terminal.wait(lambda: title in terminal.text(offset) or title[1:] in terminal.text(offset), title)


def fill_setup(terminal, name, directory, register=False, back=False):
    shown(terminal, "Choose a setup")
    terminal.send("\x01\x0b" + name + "\t\x01\x0b" + str(directory))
    assert terminal.process.poll() is None, "literal q/j exited setup"
    step(terminal, "\x0e", "Connections and protection")
    if back:
        step(terminal, "\x1b", "Choose a setup")
        assert str(directory) in terminal.text(), "Back discarded the output directory"
        terminal.resize(38, 12)
        terminal.send("\t")
        terminal.resize(120, 36)
        step(terminal, "\x0e", "Connections and protection")
    step(terminal, "\x0e", "Storage and credentials")
    step(terminal, "\x0e", "Finish options")
    if register:
        terminal.send("\t\x1b[C")  # Keep start=no, choose register=yes.
    step(terminal, "\x0e", "Review and create")
    shown(terminal, "Start: false")
    assert not directory.exists(), "setup wrote files before review was submitted"
    terminal.send("\r")
    terminal.wait(lambda: (directory / "stack.json").exists(), "generated stack")
    stack = json.loads((directory / "stack.json").read_text())
    assert stack["spec"]["id"] == name
    assert stack["spec"]["backend"] == "sqlite" and stack["spec"]["artifacts"] == "local"
    assert (directory / "compose.yaml").is_file()


def fixture_tools(root):
    tools = root / "tools"
    tools.mkdir()
    clipboard = root / "clipboard.txt"
    docker = root / "docker-was-called"
    for name in ("pbcopy", "wl-copy", "xclip", "xsel"):
        path = tools / name
        path.write_text(f"#!{sys.executable}\nimport pathlib,sys\npathlib.Path({str(clipboard)!r}).write_bytes(sys.stdin.buffer.read())\n")
        path.chmod(0o700)
    path = tools / "docker"
    path.write_text(f"#!{sys.executable}\nimport pathlib,sys\npathlib.Path({str(docker)!r}).write_text('called')\nsys.exit(99)\n")
    path.chmod(0o700)
    return tools, clipboard, docker


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--binary", default="./bin/lazymlflow")
    args = parser.parse_args()
    binary = str(Path(args.binary).resolve())
    fixture = ModelFixture()
    terminal = None
    try:
        with tempfile.TemporaryDirectory(prefix="lazymlflow-setup-models-pty-") as directory:
            root = Path(directory)
            tools, clipboard, docker = fixture_tools(root)
            config = root / "config.toml"
            config.write_text(f'default_target = "fixture"\n[[targets]]\nid = "fixture"\ntracking_uri = "{fixture.url}"\ntoken_env = "PTY_MLFLOW_TOKEN"\n')
            env = {k: v for k, v in os.environ.items() if not k.startswith(("MLFLOW_", "LAZYMLFLOW_"))}
            env.update(TERM="xterm-256color", NO_COLOR="1", PTY_MLFLOW_TOKEN="fixture-token-never-render",
                       PATH=str(tools) + os.pathsep + env.get("PATH", ""),
                       XDG_CONFIG_HOME=str(root / "config"), XDG_DATA_HOME=str(root / "data"),
                       XDG_STATE_HOME=str(root / "state"), XDG_CACHE_HOME=str(root / "cache"))

            # Bare wizard and cancellation: these must work without Docker.
            terminal = Terminal([binary, "--config", str(config), "server", "init"], env, root)
            fill_setup(terminal, "cli-jqkh", root / "cli jq literal", back=True)
            terminal.process.wait(timeout=8)
            assert terminal.process.returncode == 0, terminal.text()
            terminal.close()
            terminal = None
            assert tomllib.loads(config.read_text())["default_target"] == "fixture"

            terminal = Terminal([binary, "--config", str(config), "server", "init"], env, root)
            shown(terminal, "Choose a setup")
            terminal.send("\x01\x0bcancelled\t\x01\x0b" + str(root / "cancelled"))
            terminal.resize(12, 4)
            terminal.send("\x03")
            terminal.process.wait(timeout=8)
            assert terminal.process.returncode == 130, terminal.text()
            assert not (root / "cancelled").exists()
            terminal.close()
            terminal = None

            terminal = Terminal([binary, "--config", str(config)], env, root)
            shown(terminal, "model-run-0")
            step(terminal, "E", "Experiment environment")
            shown(terminal, "PTY_MLFLOW_TOKEN")
            assert "fixture-token-never-render" not in terminal.text()
            terminal.send("y")
            terminal.wait(clipboard.exists, "copied environment recipe")
            exported = clipboard.read_text()
            assert '${PTY_MLFLOW_TOKEN?PTY_MLFLOW_TOKEN is required}' in exported
            assert "fixture-token-never-render" not in exported
            terminal.send("\x1b")

            # Embedded wizard returns to the original target and retains its default.
            step(terminal, "\x0e", "Choose a setup")
            fill_setup(terminal, "embedded-jq", root / "embedded jq literal", register=True, back=True)
            shown(terminal, "Connection target saved: embedded-jq")
            saved = tomllib.loads(config.read_text())
            assert saved["default_target"] == "fixture"
            assert {t["id"] for t in saved["targets"]} == {"fixture", "embedded-jq"}
            terminal.send("\x1b")
            step(terminal, "t", "Targets")
            step(terminal, "s", "Choose a setup")
            terminal.send("\x03")
            shown(terminal, "Server setup cancelled")
            terminal.send("\x1b")

            # Registry -> immutable version -> metadata -> reviewed byte bundle.
            step(terminal, "O", "Registered models")
            shown(terminal, fixture.model_name)
            step(terminal, "\r", "Versions")
            shown(terminal, "v1")
            step(terminal, "\r", "Model inspection")
            terminal.wait(lambda: any(p.endswith("/model/MLmodel") for p, _ in fixture.events), "bounded MLmodel inspection")
            assert not any(p.endswith("weights.pkl") for p, _ in fixture.events), "inspection downloaded weights"
            terminal.resize(38, 12)
            terminal.send("j")
            terminal.resize(120, 36)
            step(terminal, "e", "Export directory")
            bundle = root / "bundle jq literal"
            terminal.send("\x01\x0b" + str(bundle) + "\r")
            shown(terminal, "Enter exports")
            assert not bundle.exists(), "model export wrote before review"
            terminal.send("\r")
            terminal.wait(lambda: (bundle / "manifest.json").exists(), "model bundle")
            shown(terminal, "Exported 3 files")
            manifest = json.loads((bundle / "manifest.json").read_text())
            assert manifest["source"]["resolved_uri"] == "models:/pty-model/1"
            hashes = {item["path"]: item["sha256"] for item in manifest["files"]}
            assert hashes == {name: hashlib.sha256(data).hexdigest() for name, data in fixture.files.items()}
            for name, data in fixture.files.items():
                assert (bundle / "payload" / name).read_bytes() == data
            terminal.send("\x1b")

            # The default run ordering is newest first; the first fixture run
            # with logged-model relationships is the final row.
            terminal.send("2G")
            step(terminal, "C", "Models used or produced")
            shown(terminal, "output")
            shown(terminal, "step 0")
            terminal.send("\x1b")
            step(terminal, "\x0f", "Model source URI")
            terminal.send("\x01\x0bmodels:/pty-model@candidate\r")
            shown(terminal, "Model inspection")
            terminal.send("\x1b")
            terminal.send("q")
            terminal.process.wait(timeout=8)
            assert terminal.process.returncode == 0, terminal.text()
            terminal.close()
            terminal = None
            assert not docker.exists(), "generation invoked Docker"
            assert not list(root.glob(".lazymlflow-model-*")), "export left staging directories"
            print("PASS setup/model PTY: CLI + embedded generation/review/back/cancel/resize, unchanged default, secret-reference env copy, registry/version inspection, verified export, related models and explicit URI; Docker not invoked")
    finally:
        if terminal is not None:
            primary_error = sys.exc_info()[0] is not None
            try:
                terminal.close()
            except Exception:
                if not primary_error:
                    raise
        fixture.close()


if __name__ == "__main__":
    main()
