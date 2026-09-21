#!/usr/bin/env python3
"""Build completions, verify release payloads, and publish a complete draft."""
from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import struct
import subprocess
import tarfile
import tempfile
import time

TARGETS = [(os_name, arch) for os_name in ("darwin", "linux") for arch in ("amd64", "arm64")]
STABLE_TAG = re.compile(r"v(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\Z")


def digest(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def verify_binary(data, os_name, arch):
    if os_name == "linux":
        expected = {"amd64": 62, "arm64": 183}[arch]
        valid = len(data) >= 20 and data[:6] == b"\x7fELF\x02\x01" and struct.unpack_from("<H", data, 18)[0] == expected
    else:
        expected = {"amd64": 0x01000007, "arm64": 0x0100000C}[arch]
        valid = len(data) >= 8 and data[:4] == b"\xcf\xfa\xed\xfe" and struct.unpack_from("<I", data, 4)[0] == expected
    if not valid:
        raise ValueError(f"binary does not match {os_name}/{arch}")


def verify_dist(dist, project, binary, version, smoke=False, tag=None):
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9.+-]*", version):
        raise ValueError("invalid archive version")
    expected = {f"{project}_{version}_{os_name}_{arch}.tar.gz": (os_name, arch) for os_name, arch in TARGETS}
    checksums = {}
    for line in (dist / "checksums.txt").read_text().splitlines():
        parts = line.split()
        if len(parts) != 2 or not re.fullmatch(r"[0-9a-f]{64}", parts[0]) or parts[1] in checksums:
            raise ValueError("malformed or duplicate checksum entry")
        checksums[parts[1]] = parts[0]
    if set(checksums) != set(expected):
        raise ValueError("checksum manifest must name exactly the four release archives")
    required = {binary, "LICENSE", f"completions/{binary}.bash", f"completions/{binary}.zsh"}
    host = (platform.system().lower(), {"x86_64": "amd64", "aarch64": "arm64"}.get(platform.machine().lower(), platform.machine().lower()))
    for name, target in expected.items():
        archive = dist / name
        if digest(archive) != checksums[name]:
            raise ValueError(f"checksum mismatch: {name}")
        with tarfile.open(archive, "r:gz") as stream:
            members = stream.getmembers()
            files = [m for m in members if not m.isdir()]
            if any(not m.isfile() for m in files) or len(files) != len(required) or {m.name for m in files} != required:
                raise ValueError(f"unexpected archive entries: {name}")
            if any(m.name not in {"completions", "completions/"} for m in members if m.isdir()):
                raise ValueError(f"unexpected archive directory: {name}")
            payload = stream.extractfile(binary).read()
            verify_binary(payload, *target)
            if stream.getmember(binary).mode & 0o111 == 0:
                raise ValueError(f"binary is not executable: {name}")
            for member in required - {binary}:
                if not stream.extractfile(member).read().strip():
                    raise ValueError(f"empty release file: {member}")
            if smoke and target == host:
                with tempfile.TemporaryDirectory(prefix="release-smoke-") as temporary:
                    executable = Path(temporary) / binary
                    executable.write_bytes(payload)
                    executable.chmod(0o755)
                    version_output = subprocess.check_output([str(executable), "--version"], text=True, timeout=20).strip()
                    subprocess.run([str(executable), "--help"], check=True, stdout=subprocess.DEVNULL, timeout=20)
                    if tag and tag not in version_output:
                        raise ValueError(f"binary version does not report {tag}")
    return {name: dist / name for name in sorted(expected)} | {"checksums.txt": dist / "checksums.txt"}


class GitHub:
    def __init__(self, repo):
        self.repo = repo

    def call(self, *args):
        return subprocess.check_output(["gh", *args], text=True)

    def release(self, tag):
        result = subprocess.run(["gh", "api", f"repos/{self.repo}/releases/tags/{tag}"],
                                capture_output=True, text=True)
        if result.returncode == 0:
            return json.loads(result.stdout)
        if "HTTP 404" not in result.stderr:
            raise RuntimeError(f"cannot inspect release: {result.stderr.strip()}")
        # GitHub's tag endpoint can omit an existing draft. Inspect every page
        # before deciding creation is safe; gh --slurp preserves page boundaries.
        pages = json.loads(self.call("api", "--paginate", "--slurp",
                                     f"repos/{self.repo}/releases?per_page=100"))
        if not isinstance(pages, list) or any(not isinstance(page, list) for page in pages):
            raise RuntimeError("unexpected paginated release listing")
        matches = []
        for page in pages:
            for candidate in page:
                if not isinstance(candidate, dict):
                    raise RuntimeError("unexpected release in paginated listing")
                if candidate.get("tag_name") == tag:
                    matches.append(candidate)
        if len(matches) > 1:
            raise RuntimeError(f"multiple releases use {tag}; refusing ambiguous publication")
        return matches[0] if matches else None

    def create(self, tag):
        self.call("release", "create", tag, "--repo", self.repo, "--verify-tag", "--draft",
                  "--title", tag, "--generate-notes")

    def upload(self, tag, path):
        # Deliberately never use --clobber.
        self.call("release", "upload", tag, str(path), "--repo", self.repo)

    def asset_digest(self, tag, name):
        with tempfile.TemporaryDirectory(prefix="release-asset-") as temporary:
            self.call("release", "download", tag, "--repo", self.repo, "--pattern", name, "--dir", temporary)
            return digest(Path(temporary) / name)

    def publish(self, tag):
        self.call("release", "edit", tag, "--repo", self.repo, "--draft=false", "--latest")


def publish_complete(remote, tag, assets):
    release = remote.release(tag)
    if release is None:
        remote.create(tag)
        # Draft creation can precede visibility in both GitHub lookup endpoints.
        # Retry only reads; creating again could make an ambiguous duplicate.
        for delay in (0, 2, 4, 8):
            if delay:
                time.sleep(delay)
            release = remote.release(tag)
            if release is not None:
                break
        if release is None:
            raise RuntimeError("created draft is not visible yet; rerun to resume")
    if release is None or release["prerelease"]:
        raise ValueError("expected a stable release or draft")
    existing = {a["name"] for a in release["assets"]}
    if len(existing) != len(release["assets"]) or not existing <= set(assets):
        raise ValueError("release contains unexpected or duplicate assets")
    for name in sorted(existing):
        if remote.asset_digest(tag, name) != digest(assets[name]):
            raise ValueError(f"existing release asset differs; refusing to replace {name}")
    missing = set(assets) - existing
    if missing and not release["draft"]:
        raise ValueError("published release is incomplete; refusing to mutate it")
    for name in sorted(missing):
        remote.upload(tag, assets[name])
    current = remote.release(tag)
    if current is None or len(current["assets"]) != len(assets) or {a["name"] for a in current["assets"]} != set(assets):
        raise ValueError("release assets are incomplete")
    for name in assets:
        if remote.asset_digest(tag, name) != digest(assets[name]):
            raise ValueError(f"uploaded release asset does not match: {name}")
    if current["draft"]:
        remote.publish(tag)


def verify_tag(repo, tag):
    if not STABLE_TAG.fullmatch(tag):
        raise ValueError("release requires an immutable stable vMAJOR.MINOR.PATCH tag")
    commit = subprocess.check_output(["git", "rev-parse", f"{tag}^{{commit}}"], text=True).strip()
    head = subprocess.check_output(["git", "rev-parse", "HEAD"], text=True).strip()
    if commit != head:
        raise ValueError("checkout is not the tagged commit")
    subprocess.run(["git", "fetch", "origin", "main"], check=True)
    subprocess.run(["git", "merge-base", "--is-ancestor", commit, "origin/main"], check=True)
    obj = json.loads(subprocess.check_output(["gh", "api", f"repos/{repo}/git/ref/tags/{tag}"], text=True))["object"]
    while obj["type"] == "tag":
        obj = json.loads(subprocess.check_output(["gh", "api", f"repos/{repo}/git/tags/{obj['sha']}"], text=True))["object"]
    if obj["type"] != "commit" or obj["sha"] != commit:
        raise ValueError("remote tag differs from the checked-out immutable tag")


def completions(main, binary):
    output = Path("completions")
    output.mkdir(exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="release-completions-") as temporary:
        executable = Path(temporary) / binary
        subprocess.run(["go", "build", "-trimpath", "-o", str(executable), main], check=True)
        for shell in ("bash", "zsh"):
            generated = subprocess.check_output([str(executable), "completion", shell], timeout=30)
            if not generated.strip():
                raise ValueError(f"empty {shell} completion")
            (output / f"{binary}.{shell}").write_bytes(generated)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("completions", "check", "publish"))
    parser.add_argument("--project", required=True)
    parser.add_argument("--binary", required=True)
    parser.add_argument("--main", default=".")
    parser.add_argument("--dist", type=Path, default=Path("dist"))
    parser.add_argument("--tag")
    parser.add_argument("--repo")
    parser.add_argument("--smoke", action="store_true")
    args = parser.parse_args()
    for value in (args.project, args.binary):
        if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_-]*", value):
            parser.error("project and binary must be plain names")
    if args.action == "completions":
        completions(args.main, args.binary)
        return
    metadata = json.loads((args.dist / "metadata.json").read_text())
    version = metadata["version"]
    if args.tag and (not STABLE_TAG.fullmatch(args.tag) or version != args.tag[1:]):
        parser.error("artifact version must match the stable tag")
    assets = verify_dist(args.dist, args.project, args.binary, version, args.smoke, args.tag)
    if args.action == "publish":
        if not args.tag or not args.repo or args.repo.split("/")[-1] != args.project:
            parser.error("publish requires the matching repository and stable tag")
        verify_tag(args.repo, args.tag)
        publish_complete(GitHub(args.repo), args.tag, assets)
    print(f"{args.action}: verified {len(assets)} release assets")


if __name__ == "__main__":
    main()
