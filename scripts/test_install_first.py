import importlib.util
from pathlib import Path
import plistlib
import subprocess
import tempfile
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location("install_first", Path(__file__).with_name("install-first.py"))
installer = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(installer)
IDENTITY = "A" * 40


class InstallFirstTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.home = Path(self.tmp.name)
        self.runtime = self.home / ".local/share/agent-go"
        self.runtime.mkdir(parents=True, mode=0o700)
        (self.runtime / "config.json").write_text("{}")
        (self.runtime / "config.json").chmod(0o600)
        self.codex = self.home / "codex & home"
        self.codex.mkdir()
        self.calls = []
        self.loaded = False
        self.fail_sign = False
        self.missing_identity = False
        self.race = False
        for target, value in (("sys.platform", "darwin"), ("os.getuid", lambda: 501)):
            mock = patch.object(installer.sys if target.startswith("sys") else installer.os,
                                target.split(".")[1], value)
            mock.start()
            self.addCleanup(mock.stop)
        mock = patch.object(installer.subprocess, "run", self.fake_run)
        mock.start()
        self.addCleanup(mock.stop)

    def fake_run(self, args, **kwargs):
        self.calls.append(args)
        if args[0] == "launchctl":
            return subprocess.CompletedProcess(args, 0 if self.loaded else 1)
        if args[0] == "security":
            return subprocess.CompletedProcess(args, 0, stdout="" if self.missing_identity else IDENTITY)
        if args[0] == "go":
            Path(args[args.index("-o") + 1]).write_bytes(b"test executable")
            if self.race:
                self.binary.parent.mkdir(parents=True)
                self.binary.write_bytes(b"other installation")
        if args[0] == "codesign" and self.fail_sign:
            raise subprocess.CalledProcessError(1, args)
        return subprocess.CompletedProcess(args, 0)

    @property
    def binary(self):
        return self.home / ".local/bin/agent-go"

    @property
    def plist(self):
        return self.home / "Library/LaunchAgents/ai.teslashibe.agent.plist"

    def install(self, identity=IDENTITY):
        installer.install(self.home, identity, self.codex)

    def test_prepares_signed_binary_and_escaped_plist_without_starting(self):
        self.install()
        self.assertEqual(self.binary.read_bytes(), b"test executable")
        self.assertEqual(self.binary.stat().st_mode & 0o777, 0o700)
        self.assertEqual(self.plist.stat().st_mode & 0o777, 0o600)
        data = plistlib.loads(self.plist.read_bytes())
        self.assertEqual(data["EnvironmentVariables"]["CODEX_HOME"], str(self.codex))
        self.assertEqual(data["ProgramArguments"], [str(self.binary), "-config", str(self.runtime / "config.json")])
        self.assertEqual([c for c in self.calls if c[0] == "launchctl"],
                         [("launchctl", "print", "gui/501/ai.teslashibe.agent")])
        self.assertTrue(any("-R" in c and IDENTITY in c[c.index("-R") + 1] for c in self.calls))

    def test_refuses_each_existing_artifact(self):
        for path in (
            self.binary,
            self.plist,
            self.runtime / "state.db",
            self.runtime / "signing.keychain-db",
            self.runtime / "deploy-secret.pbkdf2",
            self.runtime / "ship.pin",
        ):
            with self.subTest(path=path):
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_bytes(b"preserve")
                with self.assertRaisesRegex(ValueError, "existing installation"):
                    self.install()
                self.assertEqual(path.read_bytes(), b"preserve")
                path.unlink()
        self.assertEqual(self.calls, [])

    def test_refuses_dangling_binary_symlink(self):
        self.binary.parent.mkdir(parents=True)
        self.binary.symlink_to(self.home / "missing")
        with self.assertRaisesRegex(ValueError, "existing installation"):
            self.install()

    def test_refuses_loaded_service(self):
        self.loaded = True
        with self.assertRaisesRegex(ValueError, "already loaded"):
            self.install()
        self.assertFalse(self.binary.exists())

    def test_refuses_adhoc_identity(self):
        with self.assertRaisesRegex(ValueError, "never ad-hoc"):
            self.install("-")
        self.assertEqual(self.calls, [])

    def test_refuses_missing_private_key(self):
        self.missing_identity = True
        with self.assertRaisesRegex(ValueError, "unavailable"):
            self.install()
        self.assertFalse(self.binary.exists())

    def test_signing_failure_publishes_nothing(self):
        self.fail_sign = True
        with self.assertRaises(subprocess.CalledProcessError):
            self.install()
        self.assertFalse(self.binary.exists())
        self.assertFalse(self.plist.exists())

    def test_raced_binary_is_never_overwritten(self):
        self.race = True
        with self.assertRaises(FileExistsError):
            self.install()
        self.assertEqual(self.binary.read_bytes(), b"other installation")
        self.assertFalse(self.plist.exists())

    def test_missing_config(self):
        (self.runtime / "config.json").unlink()
        with self.assertRaisesRegex(ValueError, "write and review"):
            self.install()

    def test_plist_publish_failure_removes_only_our_binary(self):
        original_link = installer.os.link

        def link(source, destination):
            if destination == self.plist:
                destination.write_bytes(b"other plist")
                raise FileExistsError(destination)
            original_link(source, destination)

        with patch.object(installer.os, "link", side_effect=link):
            with self.assertRaises(FileExistsError):
                self.install()
        self.assertFalse(self.binary.exists())
        self.assertEqual(self.plist.read_bytes(), b"other plist")

    def test_refuses_public_runtime_or_config(self):
        for path, mode in ((self.runtime, 0o755), (self.runtime / "config.json", 0o644)):
            with self.subTest(path=path):
                original = path.stat().st_mode & 0o777
                path.chmod(mode)
                with self.assertRaisesRegex(ValueError, "must be private"):
                    self.install()
                path.chmod(original)

    def test_non_mac_and_root_refused(self):
        with patch.object(installer.sys, "platform", "linux"):
            with self.assertRaisesRegex(ValueError, "macOS"):
                self.install()
        with patch.object(installer.os, "getuid", return_value=0):
            with self.assertRaisesRegex(ValueError, "not root"):
                self.install()


if __name__ == "__main__":
    unittest.main()
