"""The S3 smoke must use an immutable isolated fixture and always clean it up."""

import importlib.util
import json
import os
from pathlib import Path
import re
import subprocess
import unittest
from unittest import mock


SPEC = importlib.util.spec_from_file_location("integration_s3", Path(__file__).with_name("integration_s3.py"))
fixture = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(fixture)


class S3FixtureTests(unittest.TestCase):
    def test_default_is_official_immutable_multi_platform_server_pin(self):
        self.assertRegex(fixture.RUSTFS_IMAGE, r"^rustfs/rustfs:1\.0\.0@sha256:[0-9a-f]{64}$")
        source = Path(__file__).resolve().parents[1] / "internal/server/render.go"
        pin = re.search(r'const RustFSImage = "([^"]+)"', source.read_text()).group(1)
        self.assertEqual(fixture.RUSTFS_IMAGE, pin)

    def run_fixture(self, provider, fail_start=False, fail_body=False):
        commands, config_paths = [], []

        def run(argv, **kwargs):
            commands.append(argv)
            env = kwargs["env"]
            self.assertNotIn("DOCKER_CONTEXT", env)
            path = Path(env["DOCKER_CONFIG"])
            config_paths.append(path)
            self.assertEqual(json.loads((path / "config.json").read_text()),
                             {"auths": {"https://index.docker.io/v1/": {}}})
            if "run" in argv and fail_start:
                return subprocess.CompletedProcess(argv, 1, stderr="fixture startup failed")
            return subprocess.CompletedProcess(argv, 0, stdout="fixture-id", stderr="")

        def check_output(argv, **kwargs):
            self.assertIn("port", argv)
            return "127.0.0.1:12345\n"

        image = fixture.RUSTFS_IMAGE if provider == "rustfs" else "example.invalid/explicit-minio@sha256:" + "a" * 64
        with mock.patch.dict(os.environ, {"DOCKER_HOST": "unix:///fixture.sock", "DOCKER_CONTEXT": "user-context"}, clear=True), \
                mock.patch.object(fixture.subprocess, "run", side_effect=run), \
                mock.patch.object(fixture.subprocess, "check_output", side_effect=check_output), \
                mock.patch.object(fixture, "wait_healthy") as health:
            if fail_start or fail_body:
                with self.assertRaisesRegex(RuntimeError, "fixture (startup|assertion) failed"):
                    with fixture.docker_s3(image, provider):
                        raise RuntimeError("fixture assertion failed")
            else:
                with fixture.docker_s3(image, provider) as endpoint:
                    self.assertEqual(endpoint, "http://127.0.0.1:12345")
            if not fail_start:
                suffix = "/health" if provider == "rustfs" else "/minio/health/ready"
                health.assert_called_once_with("http://127.0.0.1:12345" + suffix)

        startup, removal = commands[0], commands[-1]
        self.assertEqual(startup[:3], ["docker", "--host", "unix:///fixture.sock"])
        self.assertIn("--rm", startup)
        self.assertEqual(startup[startup.index("--publish") + 1], "127.0.0.1::9000")
        self.assertNotIn("--volume", startup)
        self.assertNotIn("--mount", startup)
        self.assertIn(image, startup)
        name = startup[startup.index("--name") + 1]
        self.assertTrue(name.startswith("lazymlflow-s3-"))
        self.assertEqual(removal[-3:], ["rm", "--force", name])
        for path in config_paths:
            self.assertFalse(path.exists(), "isolated Docker registry config was leaked")
        return startup

    def test_rustfs_uses_private_tmpfs_and_explicit_fixture_credentials(self):
        argv = self.run_fixture("rustfs")
        self.assertEqual(argv[argv.index("--tmpfs") + 1], "/data:rw,uid=10001,gid=10001,mode=0700")
        self.assertIn("RUSTFS_ACCESS_KEY=lazymlflow-test", argv)
        self.assertIn("RUSTFS_SECRET_KEY=lazymlflow-test-secret", argv)
        self.assertIn("RUSTFS_CONSOLE_ENABLE=false", argv)
        self.assertEqual(argv[-1], "/data")

    def test_custom_minio_uses_its_own_environment_command_and_health(self):
        argv = self.run_fixture("minio")
        self.assertIn("MINIO_ROOT_USER=lazymlflow-test", argv)
        self.assertIn("MINIO_ROOT_PASSWORD=lazymlflow-test-secret", argv)
        self.assertEqual(argv[-4:], ["server", "/data", "--address", ":9000"])

    def test_startup_or_product_assertion_failure_still_removes_owned_fixture(self):
        self.run_fixture("rustfs", fail_start=True)
        self.run_fixture("rustfs", fail_body=True)


if __name__ == "__main__":
    unittest.main()
