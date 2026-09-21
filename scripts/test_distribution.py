"""Packaging boundaries must fail independently of each other."""
import importlib.util
import io
from pathlib import Path
import subprocess
import tarfile
import tempfile
import unittest
from unittest import mock

SPEC = importlib.util.spec_from_file_location("distribution", Path(__file__).with_name("check-distribution.py"))
distribution = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(distribution)


class DistributionTests(unittest.TestCase):
    def test_runtime_smokes_use_disposable_user_state(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            config = {"main": ".", "version_symbol": "main.version"}
            with mock.patch.dict(distribution.os.environ, {"DEV_CONFIG": "/real/config", "XDG_CONFIG_HOME": "/real/xdg"}), mock.patch.object(distribution, "run") as build, mock.patch.object(distribution.subprocess, "check_output", return_value="fixture-version") as command:
                distribution.build_and_smoke(root, root / "candidate", config, "fixture-version")
            self.assertIn("DEV_CONFIG", build.call_args.kwargs["env"])
            for call in command.call_args_list:
                env = call.kwargs["env"]
                self.assertNotIn("DEV_CONFIG", env)
                self.assertTrue(env["HOME"].startswith(str(root)))
                self.assertTrue(env["XDG_CONFIG_HOME"].startswith(str(root)))
                self.assertEqual(env["HOME"], env["USERPROFILE"])

    def test_missing_runtime_asset_and_evidence_are_rejected(self):
        with self.assertRaisesRegex(ValueError, "missing required build inputs"):
            distribution.check_names(["go.mod"], ["go.mod", "embedded/help.md"])
        for root in distribution.EXCLUDED:
            with self.subTest(root=root), self.assertRaisesRegex(ValueError, "evidence"):
                distribution.check_names(["go.mod", root + "/history.md"], ["go.mod"])

    def test_rootless_source_contract_and_unsafe_path(self):
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / "source.tar.gz"
            for name in ["prefix/go.mod", "../go.mod"]:
                with tarfile.open(path, "w:gz") as archive:
                    info = tarfile.TarInfo(name)
                    info.size = 1
                    archive.addfile(info, io.BytesIO(b"x"))
                with self.assertRaises(ValueError):
                    distribution.check_source(path, ["go.mod"])

    def test_module_boundary_cannot_pass_only_because_of_export_ignore(self):
        # An actual tiny committed repository makes both builds run, then proves
        # that removing ONLY the nested go.mod is caught by the Go ZIP check.
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            def git(*args):
                return subprocess.run(["git", *args], cwd=root, check=True, capture_output=True)
            git("init", "-q")
            git("config", "user.email", "fixture@example.invalid")
            git("config", "user.name", "Distribution fixture")
            (root / "go.mod").write_text("module example.invalid/fixture\n\ngo 1.25.0\n")
            (root / "go.sum").write_text("")
            (root / "LICENSE").write_text("fixture\n")
            (root / "help.txt").write_text("embedded fixture help")
            (root / "main.go").write_text('package main\nimport("fmt"; "os"; _ "embed")\nvar version="dev"\n//go:embed help.txt\nvar help string\nfunc main(){if len(os.Args)>1 && os.Args[1]=="--version" {fmt.Println(version)} else {fmt.Println(help)}}\n')
            evidence = root / ".specstory"
            evidence.mkdir()
            (evidence / "history.md").write_text("fixture evidence\n")
            (evidence / "go.mod").write_text("// Evidence is not part of the parent Go module.\n")
            (root / ".gitattributes").write_text("/.specstory export-ignore\n/.specstory/** export-ignore\n")
            git("add", ".")
            git("commit", "-qm", "fixture with independent source and module boundaries")
            config = {"main": ".", "version_symbol": "main.version", "required": ["main.go", "help.txt"]}
            result = distribution.verify(root, "HEAD", "fixture-version", config)
            self.assertEqual(result["module_zip"]["check_zip_unzip_build_and_smoke"], "passed")
            (evidence / "go.mod").unlink()
            git("add", "-u")
            git("commit", "-qm", "remove only the module boundary")
            with self.assertRaisesRegex(ValueError, "evidence"):
                distribution.verify(root, "HEAD", "fixture-version", config)
            # Removing a build input must fail even though boundaries are valid.
            (evidence / "go.mod").write_text("// Evidence boundary.\n")
            (root / "help.txt").unlink()
            git("add", "-A")
            git("commit", "-qm", "remove required embedded help")
            with self.assertRaisesRegex(ValueError, "missing required build inputs"):
                distribution.verify(root, "HEAD", "fixture-version", config)


if __name__ == "__main__":
    unittest.main()
