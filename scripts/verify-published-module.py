#!/usr/bin/env python3
"""Download public Go module ZIPs and install fixed/latest into disposable state."""
import argparse
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import re
import time
import sys

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--root", type=Path, required=True)
parser.add_argument("--version", required=True)
args = parser.parse_args()
args.root = args.root.resolve()
if not re.fullmatch(r"v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)", args.version):
    parser.error("version must be a stable vMAJOR.MINOR.PATCH tag")
config = json.loads((args.root / "scripts/distribution.json").read_text())
module = next(line.split()[1] for line in (args.root / "go.mod").read_text().splitlines() if line.startswith("module "))
spec = importlib.util.spec_from_file_location("distribution", args.root / "scripts/check-distribution.py")
distribution = importlib.util.module_from_spec(spec)
spec.loader.exec_module(distribution)
package = module + ("/" + config["main"].removeprefix("./") if config["main"] != "." else "")
with tempfile.TemporaryDirectory(prefix="published-go-module-") as temporary:
    root = Path(temporary)
    env = os.environ.copy()
    for key in list(env):
        if key.startswith(("DEV_", "EXP_", "TRANSLATE_", "LAZYCLASH_", "LAZYMLFLOW_", "LAZYPUEUE_", "LAZYCHEZMOI_", "XDG_")) or key in {"GOOS", "GOARCH", "GOFLAGS", "GOPRIVATE", "GONOPROXY", "GONOSUMDB"}:
            env.pop(key, None)
    for name in ("home", "config", "data", "cache", "state", "run", "tmp", "bin", "gopath", "gomodcache", "gocache"):
        (root / name).mkdir()
    env.update({
        "HOME": str(root / "home"), "USERPROFILE": str(root / "home"),
        "XDG_CONFIG_HOME": str(root / "config"), "XDG_DATA_HOME": str(root / "data"),
        "XDG_CACHE_HOME": str(root / "cache"), "XDG_STATE_HOME": str(root / "state"),
        "XDG_RUNTIME_DIR": str(root / "run"), "APPDATA": str(root / "config"),
        "LOCALAPPDATA": str(root / "data"), "TMPDIR": str(root / "tmp"),
        "TEMP": str(root / "tmp"), "TMP": str(root / "tmp"),
        "GOPATH": str(root / "gopath"), "GOMODCACHE": str(root / "gomodcache"),
        "GOCACHE": str(root / "gocache"), "GOBIN": str(root / "bin"), "GOENV": "off", "GOWORK": "off",
        "GOPROXY": "https://proxy.golang.org", "GOSUMDB": "sum.golang.org", "CGO_ENABLED": "0",
    })
    results = []
    for query in (args.version, "latest"):
        for attempt in range(6):
            downloaded = subprocess.run(["go", "mod", "download", "-json", module + "@" + query], cwd=root, env=env, timeout=300, capture_output=True, text=True)
            if downloaded.returncode == 0:
                data = json.loads(downloaded.stdout)
                break
            message = downloaded.stdout or downloaded.stderr
            message = re.sub(r"(https?://)[^/@\s]+@", r"\1[redacted]@", message)
            if attempt == 5:
                raise RuntimeError("public module download failed: " + message)
            print("Public module not available yet; retrying read: " + message, file=sys.stderr, flush=True)
            time.sleep(15)
        if data.get("Version") != args.version:
            raise ValueError("public proxy resolved unexpected version: " + str(data.get("Version")))
        info = distribution.check_module(Path(data["Zip"]), module, args.version, ["go.mod", "go.sum", "LICENSE", *config.get("required", [])])
        subprocess.run(["go", "install", package + "@" + query], cwd=root, env=env, timeout=600, check=True)
        binaries = list((root / "bin").iterdir())
        if len(binaries) != 1:
            raise ValueError("expected one installed command")
        binary = str(binaries[0])
        version = subprocess.check_output([binary, "--version"], cwd=root, env=env, timeout=30, text=True).strip()
        if args.version not in version:
            raise ValueError("installed command reports unexpected version: " + version)
        for argv in [["--help"], ["completion", "bash"], ["completion", "zsh"], *config.get("smoke", [])]:
            if not subprocess.check_output([binary, *argv], cwd=root, env=env, timeout=30).strip():
                raise ValueError("installed command returned empty smoke output")
        results.append({"query": query, "resolved": data["Version"], "zip": info, "install_version": version, "smoke": "passed"})
    print(json.dumps({"module": module, "results": results}))
