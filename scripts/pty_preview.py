#!/usr/bin/env python3
"""Artifact preview acceptance through a real terminal and disposable HTTP data."""
import argparse
import json
import os
from pathlib import Path
import re
import tempfile
from urllib.parse import parse_qs, unquote, urlparse

from pty_smoke import Fixture, Terminal


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--binary", default="./bin/lazymlflow")
    binary = str(Path(parser.parse_args().binary).resolve())
    fixture = Fixture("preview")
    terminal = None
    files = {
        "report.json": b'{"loss":0.5,"status":"ready"}',
        "large.log": b"line-000 bounded prefix\n" + b"x" * 4096,
        "mystery.txt": b"unknown size text\n" * 20,
        "binary.bin": b"\x00\x01\x02binary",
    }
    original = fixture.server.RequestHandlerClass
    original_get = original.do_GET

    def get(self):
        url = urlparse(self.path)
        query = parse_qs(url.query)
        if url.path.endswith("artifacts/list"):
            fixture.events.append((url.path, query))
            if query.get("path", [""])[0]:
                self.respond({"files": []})
            else:
                self.respond({"files": [
                    {"path": name, "is_dir": False, **({} if name == "mystery.txt" else {"file_size": len(body)})}
                    for name, body in files.items()
                ] + [{"path": "folder", "is_dir": True}]})
            return
        if "/mlflow-artifacts/artifacts/" in url.path:
            name = unquote(url.path.rsplit("/", 1)[-1])
            fixture.events.append((url.path, {"range": self.headers.get("Range", "")}))
            data = files[name]
            match = re.fullmatch(r"bytes=0-(\d+)", self.headers.get("Range", ""))
            body = data[:int(match[1]) + 1] if match else data
            self.send_response(206 if match else 200)
            self.send_header("Content-Type", "application/octet-stream")
            self.send_header("Content-Length", str(len(body)))
            if match:
                self.send_header("Content-Range", f"bytes 0-{len(body)-1}/{len(data)}")
            self.end_headers()
            try:
                self.wfile.write(body)
            except (BrokenPipeError, ConnectionResetError):
                pass
            return
        original_get(self)

    original.do_GET = get

    def transfers():
        return [event for event in fixture.events if "/mlflow-artifacts/artifacts/" in event[0]]

    try:
        with tempfile.TemporaryDirectory(prefix="lazymlflow-preview-pty-") as directory:
            root = Path(directory)
            fake_bin = root / "bin"
            fake_bin.mkdir()
            pager_log = root / "pager.json"
            bat = fake_bin / "bat"
            bat.write_text("""#!/usr/bin/env python3
import json, os, sys
from pathlib import Path
path = Path(sys.argv[-1])
Path(os.environ['PREVIEW_PAGER_LOG']).write_text(json.dumps({'argv': sys.argv, 'path': str(path), 'text': path.read_text()}))
print('PREVIEW-PAGER-READY', flush=True)
input()
print('PREVIEW-PAGER-DONE', flush=True)
""", encoding="utf-8")
            bat.chmod(0o700)
            config = root / "config.toml"
            config.write_text(f'''default_target="preview"
[tui]
preview_max_bytes=64
[activity]
refresh_seconds=0
[[targets]]
id="preview"
tracking_uri="{fixture.url}"
''', encoding="utf-8")
            env = {key: value for key, value in os.environ.items() if key not in ("LAZYMLFLOW_TARGET", "MLFLOW_TRACKING_URI")}
            env.update(TERM="xterm-256color", NO_COLOR="1", PATH=str(fake_bin) + os.pathsep + env.get("PATH", ""),
                       PREVIEW_PAGER_LOG=str(pager_log), TMPDIR=str(root), HOME=str(root / "home"),
                       XDG_CONFIG_HOME=str(root / "config"), XDG_DATA_HOME=str(root / "data"), XDG_STATE_HOME=str(root / "state"))
            terminal = Terminal([binary, "--config", str(config)], env, root)
            terminal.wait(lambda: "preview-run-0" in terminal.text(), "initial run")
            terminal.send("3]]]]")
            terminal.wait(lambda: "report.json" in terminal.text(), "artifact list")
            mark = len(terminal.raw)
            terminal.send("G\r")  # Artifacts are directory-first, then name sorted.
            terminal.wait(lambda: "Artifact preview" in terminal.text(mark) and "ready" in terminal.text(mark), "built-in formatted JSON")
            assert len(transfers()) == 1, "small preview fetched more than one content object"
            terminal.send("/status\r")
            terminal.send("b")
            terminal.wait(lambda: "PREVIEW-PAGER-READY" in terminal.text(), "external pager receives terminal")
            first = json.loads(pager_log.read_text())
            assert "report.json" in first["argv"] and "ready" in first["text"]
            assert len(transfers()) == 1, "pager fetched full artifact again"
            mark = len(terminal.raw)
            terminal.send("q\r")
            terminal.wait(lambda: "Artifact preview" in terminal.text(mark), "preview restored after pager")
            assert not Path(first["path"]).exists(), "pager tempfile survived return"

            terminal.send("\x1b")
            terminal.send("ggjj\r")
            terminal.wait(lambda: "Confirm artifact preview" in terminal.text(), "large artifact confirmation")
            before = len(transfers())
            terminal.send("\r")
            assert len(transfers()) == before, "default confirmation read artifact bytes"
            mark = len(terminal.raw)
            terminal.send("p")
            terminal.wait(lambda: "Confirm artifact preview" in terminal.text(mark), "large preview confirmation reopened")
            mark = len(terminal.raw)
            terminal.send("j\r")
            terminal.wait(lambda: "TRUNCATED PREFIX" in terminal.text(mark), "approved bounded prefix")
            assert len(transfers()) == before + 1
            assert transfers()[-1][1]["range"] == "bytes=0-64", transfers()[-1]
            terminal.send("b")
            terminal.wait(lambda: pager_log.exists() and "large.log" in json.loads(pager_log.read_text())["text"], "bounded large preview handed to pager")
            second = json.loads(pager_log.read_text())
            assert "TRUNCATED" in second["text"].upper() and "x" * 128 not in second["text"]
            assert len(transfers()) == before + 1, "large pager caused another transfer"
            mark = len(terminal.raw)
            terminal.send("q\r")
            terminal.wait(lambda: "Artifact preview" in terminal.text(mark), "large preview restored")
            assert not Path(second["path"]).exists()

            mark = len(terminal.raw)
            terminal.send("\x1b")
            terminal.send("j\r")
            terminal.wait(lambda: "Artifact size: unknown" in terminal.text(mark), "unknown size requires consent")
            before = len(transfers())
            terminal.resize(30, 8)
            terminal.send("\x1b")
            assert len(transfers()) == before, "cancelled unknown artifact was read"
            terminal.resize(120, 36)
            mark = len(terminal.raw)
            terminal.send("ggj\r")
            terminal.wait(lambda: "Binary preview unavailable" in terminal.text(mark), "binary metadata fallback")
            terminal.send("\x1b")
            terminal.send("gg\r")
            terminal.wait(lambda: any(path.endswith("artifacts/list") and query.get("path") == ["folder"] for path, query in fixture.events), "directory Enter navigates")
            terminal.send("q")
            terminal.process.wait(8)
            assert terminal.process.returncode == 0
            terminal.close()
            terminal = None
            assert not list(root.glob("lazymlflow-preview-*")), "preview temp directories leaked"
            print("PASS artifact preview PTY: bounded JSON/text, consent default-cancel, unknown/binary handling, search, resize, pager handoff/cleanup, directory navigation, terminal restored")
    finally:
        if terminal is not None:
            terminal.close()
        fixture.close()


if __name__ == "__main__":
    main()
