#!/usr/bin/env python3
"""Check GHCR destinations and publication channels without registry access."""

import os
from pathlib import Path
import subprocess
import tempfile
import unittest


SCRIPT = Path(__file__).resolve().with_name("docker-metadata.sh")
SHA = "a" * 40


class DockerMetadataTest(unittest.TestCase):
    def metadata(self, ref, version="v1.2.3", repository="Example/SingBox-Panel", succeeds=True):
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            (root / "cmd/agent").mkdir(parents=True)
            (root / "cmd/agent/VERSION").write_text(version + "\n")
            output = root / "outputs"
            env = dict(os.environ, GITHUB_REF=ref, GITHUB_SHA=SHA,
                       GITHUB_REPOSITORY=repository, GITHUB_OUTPUT=str(output))
            result = subprocess.run(["bash", str(SCRIPT)], cwd=root, env=env,
                                    stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
            self.assertEqual(result.returncode == 0, succeeds, result.stderr)
            return output.read_text() if output.exists() else ""

    def test_main_publishes_only_development_and_commit_tags(self):
        output = self.metadata("refs/heads/main")
        self.assertIn("image=ghcr.io/example/singbox-panel\n", output)
        self.assertIn("version=v1.2.3-edge.aaaaaaaaaaaa\n", output)
        self.assertIn("ghcr.io/example/singbox-panel:edge\n", output)
        self.assertIn("ghcr.io/example/singbox-panel:sha-aaaaaaaaaaaa\n", output)
        self.assertNotIn(":latest", output)

    def test_stable_release_publishes_version_and_latest(self):
        output = self.metadata("refs/tags/v1.2.3")
        self.assertIn("version=v1.2.3\n", output)
        self.assertIn("ghcr.io/example/singbox-panel:v1.2.3\n", output)
        self.assertIn("ghcr.io/example/singbox-panel:latest\n", output)
        self.assertNotIn(":edge", output)
        self.assertNotIn(":sha-", output)

    def test_prereleases_do_not_replace_latest(self):
        for version in ("v1.2.3-rc.1", "v1.2.3.beta1"):
            with self.subTest(version=version):
                output = self.metadata("refs/tags/" + version, version=version)
                self.assertIn("ghcr.io/example/singbox-panel:" + version + "\n", output)
                self.assertNotIn(":latest", output)

    def test_untrusted_or_invalid_refs_cannot_publish(self):
        for ref in ("refs/pull/1/merge", "refs/heads/feature", "refs/tags/latest", "refs/tags/v1.2.3;echo nope"):
            with self.subTest(ref=ref):
                self.assertEqual(self.metadata(ref, succeeds=False), "")

    def test_release_must_match_agent_version(self):
        self.assertEqual(self.metadata("refs/tags/v1.2.4", succeeds=False), "")

    def test_fork_uses_its_own_registry_namespace(self):
        output = self.metadata("refs/heads/main", repository="ForkOwner/Custom-Panel")
        self.assertIn("image=ghcr.io/forkowner/custom-panel\n", output)
        self.assertNotIn("hann0w0", output)


if __name__ == "__main__":
    unittest.main()
