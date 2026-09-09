"""Failure and evidence-boundary checks for the Mini verification entry point."""

import importlib.machinery
import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from types import SimpleNamespace
from unittest.mock import patch


loader = importlib.machinery.SourceFileLoader("verify_mini", str(Path(__file__).with_name("verify-mini")))
spec = importlib.util.spec_from_loader(loader.name, loader)
verify = importlib.util.module_from_spec(spec)
loader.exec_module(verify)


class VerificationEvidenceTests(unittest.TestCase):
    def test_cache_cleanup_preserves_evidence_and_symlink_targets(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            output = root / "evidence"
            output.mkdir()
            for name in ("summary.json", "source.tar", "test.log", "agent-go"):
                (output / name).write_text("retained")
            module = output / "modules" / "module"
            module.mkdir(parents=True)
            (module / "source.go").write_text("regenerable")
            module.chmod(0o555)
            external = root / "external-cache"
            external.mkdir()
            (external / "keep").write_text("untouched")
            (output / "cache").symlink_to(external, target_is_directory=True)
            self.assertEqual(verify.cleanup_caches(output), ["cache", "modules"])
            self.assertEqual((external / "keep").read_text(), "untouched")
            for name in ("summary.json", "source.tar", "test.log", "agent-go"):
                self.assertEqual((output / name).read_text(), "retained")

    def test_low_disk_space_fails_before_commands(self):
        with patch.object(verify.shutil, "disk_usage") as usage:
            usage.return_value.free = 1024
            with self.assertRaisesRegex(RuntimeError, "4 GiB"):
                verify.require_free_space(Path("."))

    def test_stage_reclaims_only_build_cache_when_space_is_low(self):
        with tempfile.TemporaryDirectory() as temporary:
            output = Path(temporary)
            (output / "cache").mkdir()
            (output / "cache/build").write_text("regenerable")
            retained = ("modules/source.go", "source/main.go", "source.tar", "summary.json", "test.log", "agent-go")
            for name in retained:
                path = output / name
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text("retained")
            with patch.object(verify.shutil, "disk_usage", side_effect=[
                    SimpleNamespace(free=3 * 1024**3), SimpleNamespace(free=4 * 1024**3)]) as usage:
                self.assertTrue(verify.require_free_space(output, reclaim_cache=True))
                self.assertEqual(usage.call_count, 2)
            self.assertEqual(list((output / "cache").iterdir()), [])
            self.assertEqual((output / "cache").stat().st_mode & 0o777, 0o700)
            for name in retained:
                self.assertEqual((output / name).read_text(), "retained")

    def test_stage_keeps_build_cache_when_space_is_sufficient(self):
        with tempfile.TemporaryDirectory() as temporary:
            output = Path(temporary)
            (output / "cache").mkdir()
            cached = output / "cache/build"
            cached.write_text("retained")
            with patch.object(verify.shutil, "disk_usage", return_value=SimpleNamespace(free=4 * 1024**3)) as usage:
                self.assertFalse(verify.require_free_space(output, reclaim_cache=True))
                usage.assert_called_once_with(output)
            self.assertEqual(cached.read_text(), "retained")

    def test_stage_still_fails_when_reclamation_is_insufficient(self):
        with tempfile.TemporaryDirectory() as temporary:
            output = Path(temporary)
            (output / "cache").mkdir()
            (output / "cache/build").write_text("regenerable")
            with patch.object(verify.shutil, "disk_usage", return_value=SimpleNamespace(free=1024)) as usage:
                with self.assertRaisesRegex(RuntimeError, "4 GiB"):
                    verify.require_free_space(output, reclaim_cache=True)
                self.assertEqual(usage.call_count, 2)
            self.assertEqual(list((output / "cache").iterdir()), [])

    def test_failed_toolchain_keeps_committed_snapshot_and_failure_evidence(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            repo = root / "repo"
            repo.mkdir()
            subprocess.run(["git", "init", "-q", str(repo)], check=True)
            committed = "module example.test/fixture\n\ngo 1.22\n"
            (repo / "go.mod").write_text(committed)
            subprocess.run(["git", "add", "go.mod"], cwd=repo, check=True)
            subprocess.run(["git", "-c", "user.name=Fixture", "-c", "user.email=fixture@example.test",
                            "-c", "commit.gpgsign=false", "commit", "-qm", "fixture"], cwd=repo, check=True)
            # Dirty working files must never be mistaken for the verified commit.
            (repo / "go.mod").write_text("uncommitted and invalid\n")
            evidence = root / "evidence"
            argv = ["verify-mini", "--repo", str(repo), "--output", str(evidence),
                    "--test-exec", sys.executable, "--go", "/usr/bin/false"]
            with patch.object(sys, "argv", argv), patch.object(verify.platform, "system", return_value="Darwin"), \
                    patch.object(verify.platform, "machine", return_value="arm64"):
                self.assertEqual(verify.main(), 1)
            summary = json.loads((evidence / "summary.json").read_text())
            self.assertEqual(summary["status"], "failed")
            self.assertEqual(summary["checks"], [])
            self.assertEqual((evidence / "source/go.mod").read_text(), committed)
            self.assertIn("error", summary)
            self.assertEqual(summary["native_tests"], "not run")
            # A retry cannot replace earlier evidence, even after failure.
            before = (evidence / "summary.json").read_bytes()
            with patch.object(sys, "argv", argv), patch.object(verify.platform, "system", return_value="Darwin"), \
                    patch.object(verify.platform, "machine", return_value="arm64"):
                with self.assertRaises(FileExistsError):
                    verify.main()
            self.assertEqual((evidence / "summary.json").read_bytes(), before)

    def test_socket_directory_is_short_private_and_exclusive(self):
        first = verify.short_test_directory()
        second = verify.short_test_directory()
        try:
            self.assertNotEqual(first, second)
            self.assertLessEqual(len(str(first)), 9)
            self.assertEqual(first.stat().st_mode & 0o777, 0o700)
        finally:
            first.rmdir()
            second.rmdir()


if __name__ == "__main__":
    unittest.main()
