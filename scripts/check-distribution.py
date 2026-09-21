#!/usr/bin/env python3
"""Verify real Git source and Go module distributions from a committed revision.

The product checkout, index, refs, attributes, go.mod and go.sum are never edited.
Only an isolated ordinary clone receives Go's export attribute overrides. The
official x/mod ZIP implementation runs from a separate pinned test-only module.
"""
from __future__ import annotations

import argparse
import json
import os
from pathlib import Path, PurePosixPath
import re
import subprocess
import tarfile
import tempfile
import zipfile

EXCLUDED = (".specstory", ".claude/plans", ".codex/plans", ".cursor/plans", ".opencode/plans")
XMOD_VERSION = "v0.38.0"
XMOD_SUMS = """golang.org/x/mod v0.38.0 h1:MECBjubtXD7yj4HrhIUcywNaGeNVUdfVnxmPajOk4yk=
golang.org/x/mod v0.38.0/go.mod h1:V6Xz0pq8TQ3dGqVQ1FVHuelZpAL0uNhSkk9ogYP3c40=
"""
ZIP_PROGRAM = r'''package main
import (
 "fmt"
 "os"
 "golang.org/x/mod/module"
 modzip "golang.org/x/mod/zip"
)
func run() error {
 if len(os.Args) != 7 { return fmt.Errorf("expected repo ref module version zip destination") }
 m := module.Version{Path: os.Args[3], Version: os.Args[4]}
 f, err := os.Create(os.Args[5]); if err != nil { return err }
 err = modzip.CreateFromVCS(f, m, os.Args[1], os.Args[2], "")
 closeErr := f.Close(); if err != nil { return err }; if closeErr != nil { return closeErr }
 if _, err = modzip.CheckZip(m, os.Args[5]); err != nil { return err }
 return modzip.Unzip(os.Args[6], m, os.Args[5])
}
func main() { if err := run(); err != nil { fmt.Fprintln(os.Stderr, err); os.Exit(1) } }
'''


def run(args, cwd=None, **kwargs):
    return subprocess.run(args, cwd=cwd, check=True, **kwargs)


def output(args, cwd=None):
    return subprocess.check_output(args, cwd=cwd, text=True).strip()


def excluded(name):
    return any(name == root or name.startswith(root + "/") for root in EXCLUDED)


def check_names(names, required):
    names = set(names)
    if any(excluded(name) for name in names):
        raise ValueError("distribution contains conversation or agent plan evidence")
    missing = set(required) - names
    if missing:
        raise ValueError("distribution is missing required build inputs: " + ", ".join(sorted(missing)))
    for name in names:
        path = PurePosixPath(name)
        if path.is_absolute() or ".." in path.parts or "\\" in name:
            raise ValueError("unsafe distribution path: " + name)


def check_source(path, required, destination=None):
    with tarfile.open(path, "r:gz") as archive:
        members = archive.getmembers()
        check_names((member.name.rstrip("/") for member in members), required)
        if any(not (member.isfile() or member.isdir() or member.issym()) for member in members):
            raise ValueError("source archive contains an unsupported file type")
        for member in members:
            tarfile.data_filter(member, str(destination or Path(tempfile.gettempdir()) / "distribution-inspection"))
        if destination is not None:
            # Python's data filter rejects escaping symlink targets and devices.
            archive.extractall(destination, filter="data")
        return {"bytes": path.stat().st_size, "files": sum(m.isfile() for m in members)}


def check_module(path, module, version, required):
    prefix = module + "@" + version + "/"
    with zipfile.ZipFile(path) as archive:
        names = archive.namelist()
        if any(not name.startswith(prefix) for name in names):
            raise ValueError("unexpected module ZIP prefix")
        check_names((name[len(prefix):] for name in names), required)
        return {"bytes": path.stat().st_size, "files": len(names)}


def build_and_smoke(source, target, config, version):
    env = os.environ.copy()
    # Keep the test native, independent of callers' cross-compilation settings.
    for key in ("GOOS", "GOARCH", "GOFLAGS", "GOWORK"):
        env.pop(key, None)
    env.update({"GOWORK": "off", "CGO_ENABLED": "0"})
    args = ["go", "build", "-mod=readonly", "-buildvcs=false", "-ldflags",
            "-X " + config["version_symbol"] + "=" + version, "-o", str(target), config["main"]]
    run(args, cwd=source, env=env)
    runtime_env = env.copy()
    for key in list(runtime_env):
        if key.startswith(("DEV_", "EXP_", "TRANSLATE_", "LAZYCLASH_", "LAZYMLFLOW_", "LAZYPUEUE_", "LAZYCHEZMOI_", "XDG_")):
            runtime_env.pop(key, None)
    runtime = target.parent / (target.name + "-runtime")
    for name in ("home", "config", "data", "cache", "state", "run", "tmp"):
        (runtime / name).mkdir(parents=True, exist_ok=True)
    runtime_env.update({
        "HOME": str(runtime / "home"), "USERPROFILE": str(runtime / "home"),
        "XDG_CONFIG_HOME": str(runtime / "config"), "XDG_DATA_HOME": str(runtime / "data"),
        "XDG_CACHE_HOME": str(runtime / "cache"), "XDG_STATE_HOME": str(runtime / "state"),
        "XDG_RUNTIME_DIR": str(runtime / "run"), "APPDATA": str(runtime / "config"),
        "LOCALAPPDATA": str(runtime / "data"), "TMPDIR": str(runtime / "tmp"),
        "TEMP": str(runtime / "tmp"), "TMP": str(runtime / "tmp"),
    })
    result = subprocess.check_output([str(target), "--version"], text=True, timeout=30, env=runtime_env)
    if version not in result:
        raise ValueError("distribution build did not report injected version")
    for argv in [["--help"], ["completion", "bash"], ["completion", "zsh"], *config.get("smoke", [])]:
        result = subprocess.check_output([str(target), *argv], cwd=source, timeout=30, env=runtime_env)
        if not result.strip():
            raise ValueError("empty distribution smoke output: " + repr(argv))


def verify(root, ref, version, config, baseline=None):
    oid = output(["git", "rev-parse", "--verify", "--end-of-options", ref + "^{commit}"], root)
    module_text = output(["git", "show", oid + ":go.mod"], root)
    module = re.search(r"(?m)^module\s+(\S+)", module_text).group(1)
    required = ["go.mod", "go.sum", "LICENSE", *config.get("required", [])]
    result = {"commit": oid, "module": module, "x_mod": XMOD_VERSION}
    with tempfile.TemporaryDirectory(prefix="go-distribution-check-") as temporary:
        directory = Path(temporary)
        clone = directory / "repository"
        # CreateFromVCS requires an ordinary .git directory, not a linked worktree.
        run(["git", "clone", "--quiet", "--shared", "--no-checkout", str(root), str(clone)])
        archive = directory / "source.tar.gz"
        with archive.open("wb") as stream:
            run(["git", "archive", "--format=tar.gz", oid], clone, stdout=stream)
        source = directory / "source"
        source.mkdir()
        result["source"] = check_source(archive, required, source)
        suffix = ".exe" if os.name == "nt" else ""
        build_and_smoke(source, directory / ("source-binary" + suffix), config, version)
        result["source"]["build_and_smoke"] = "passed"

        # The Go command ignores export-ignore/export-subst. Apply that override
        # only inside this throwaway clone, so markers (not archive rules) are
        # what remove evidence from the independently generated module ZIP.
        (clone / ".git/info/attributes").write_text("* -export-subst -export-ignore\n")
        helper = directory / "zip-helper"
        helper.mkdir()
        (helper / "go.mod").write_text("module distribution-check.invalid/zip\n\ngo 1.25.0\n\nrequire golang.org/x/mod " + XMOD_VERSION + "\n")
        (helper / "go.sum").write_text(XMOD_SUMS)
        (helper / "main.go").write_text(ZIP_PROGRAM)
        helper_binary = directory / ("zip-helper-bin" + suffix)
        helper_env = os.environ.copy()
        for key in ("GOOS", "GOARCH", "GOFLAGS"):
            helper_env.pop(key, None)
        helper_env["GOWORK"] = "off"
        run(["go", "build", "-mod=readonly", "-o", str(helper_binary), "."], helper, env=helper_env)
        # A synthetic stable version checks ZIP structure without requiring a tag.
        zip_version = "v0.0.0"
        module_zip, module_source = directory / "module.zip", directory / "module"
        run([str(helper_binary), str(clone), oid, module, zip_version, str(module_zip), str(module_source)])
        result["module_zip"] = check_module(module_zip, module, zip_version, required)
        build_and_smoke(module_source, directory / ("module-binary" + suffix), config, version)
        result["module_zip"]["check_zip_unzip_build_and_smoke"] = "passed"
        if baseline:
            old = output(["git", "rev-parse", "--verify", "--end-of-options", baseline + "^{commit}"], root)
            old_zip = directory / "baseline-module.zip"
            run([str(helper_binary), str(clone), old, module, zip_version, str(old_zip), str(directory / "baseline-module")])
            old_archive = directory / "baseline-source.tar.gz"
            (clone / ".git/info/attributes").unlink()
            with old_archive.open("wb") as stream:
                run(["git", "archive", "--format=tar.gz", old], clone, stdout=stream)
            result["baseline"] = {"commit": old, "source_bytes": old_archive.stat().st_size, "module_zip_bytes": old_zip.stat().st_size}
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--ref", default="HEAD")
    parser.add_argument("--version", default="distribution-check")
    parser.add_argument("--baseline", help="optional pre-packaging commit for size comparison")
    parser.add_argument("--source-archive", type=Path, help="check and build an actual release source payload instead")
    parser.add_argument("--config", type=Path, default=Path("scripts/distribution.json"))
    args = parser.parse_args()
    root = Path(output(["git", "rev-parse", "--show-toplevel"]))
    config = json.loads((root / args.config).read_text())
    if args.source_archive:
        with tempfile.TemporaryDirectory(prefix="release-source-check-") as temporary:
            source = Path(temporary) / "source"
            source.mkdir()
            result = check_source(args.source_archive.resolve(), ["go.mod", "go.sum", "LICENSE", *config.get("required", [])], source)
            build_and_smoke(source, Path(temporary) / ("binary.exe" if os.name == "nt" else "binary"), config, args.version)
            print(json.dumps({"source": result, "build_and_smoke": "passed"}))
        return
    print(json.dumps(verify(root, args.ref, args.version, config, args.baseline), sort_keys=True))


if __name__ == "__main__":
    main()
