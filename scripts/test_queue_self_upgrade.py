import importlib.machinery
import importlib.util
import json
from pathlib import Path
import sqlite3
import tempfile
from types import SimpleNamespace
import unittest
from unittest import mock


loader = importlib.machinery.SourceFileLoader("upgrade", str(Path(__file__).with_name("queue-self-upgrade")))
spec = importlib.util.spec_from_loader(loader.name, loader)
upgrade = importlib.util.module_from_spec(spec)
loader.exec_module(upgrade)


class UpgradeTests(unittest.TestCase):
    def setUp(self):
        self.db = sqlite3.connect(":memory:")
        self.addCleanup(self.db.close)
        self.db.execute("CREATE TABLE jobs(id INTEGER PRIMARY KEY,source TEXT,sender TEXT,state TEXT)")
        # Canonical persisted DM shape from internal/store/store.go Source.key.
        self.identity = {"Name": "fixture", "Sender": "owner", "ChatID": 1, "ChatGUID": "dm"}
        self.raw = json.dumps(self.identity)
        self.db.execute("INSERT INTO jobs VALUES (1,?,'owner','running')", (self.raw,))
        self.db.commit()
        self.config = {"source": "fixture", "agents": [{"chat_id": 1, "chat_guid": "dm", "group": False,
                        "allowed_senders": ["owner"], "work_dir": "/workspace"}]}

    def test_queue_requires_exact_running_coding_identity(self):
        source = Path("/workspace/agent-go")
        self.assertEqual(upgrade.owner_job(self.db, self.config, source)["id"], 1)
        for field, value in [("Name", "other"), ("Group", True), ("ChatID", 2),
                             ("ChatGUID", "other"), ("Sender", "other"),
                             ("AllowedSenders", ["owner"])]:
            with self.subTest(field=field):
                self.db.execute("UPDATE jobs SET source=?", (json.dumps({**self.identity, field: value}),))
                with self.assertRaises(RuntimeError):
                    upgrade.owner_job(self.db, self.config, source)
        self.db.execute("UPDATE jobs SET source=?,sender='other'", (self.raw,))
        with self.assertRaises(RuntimeError):
            upgrade.owner_job(self.db, self.config, source)
        self.db.execute("UPDATE jobs SET sender='owner'")
        with self.assertRaises(RuntimeError):
            upgrade.owner_job(self.db, self.config, Path("/unrelated/agent-go"))
        self.db.execute("INSERT INTO jobs VALUES(2,?,'owner','running')", (self.raw,))
        with self.assertRaises(RuntimeError):
            upgrade.owner_job(self.db, self.config, source)

    def test_owner_config_is_still_authoritative(self):
        source = Path("/workspace/agent-go")
        for changes in [{"group": True}, {"allowed_senders": ["other"]},
                        {"allowed_senders": ["owner", "other"]},
                        {"chat_id": 2}, {"chat_guid": "other"}, {"work_dir": ""}]:
            with self.subTest(changes=changes):
                config = {**self.config, "agents": [{**self.config["agents"][0], **changes}]}
                with self.assertRaises(RuntimeError):
                    upgrade.owner_job(self.db, config, source)

    def test_completed_owner_job_and_global_idle_are_both_required(self):
        record = {"job": {"id": 1, "source": self.raw, "sender": "owner"}}
        deploy = SimpleNamespace(idle=mock.Mock(return_value={"jobs": 0}))
        self.assertFalse(upgrade.ready(self.db, record, deploy))
        deploy.idle.assert_not_called()
        for state in ["failed", "unknown", "queued"]:
            self.db.execute("UPDATE jobs SET state=?", (state,))
            with self.assertRaises(RuntimeError):
                upgrade.ready(self.db, record, deploy)
        self.db.execute("UPDATE jobs SET state='completed'")
        deploy.idle.return_value = {"effects": 1}
        self.assertFalse(upgrade.ready(self.db, record, deploy))
        deploy.idle.return_value = {"effects": 0}
        self.assertTrue(upgrade.ready(self.db, record, deploy))
        self.db.execute("UPDATE jobs SET sender='other'")
        with self.assertRaises(RuntimeError):
            upgrade.ready(self.db, record, deploy)

    def test_artifact_requires_full_evidence_and_identical_clean_main(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "scripts").mkdir()
            (root / "scripts/verify-mini").write_text("reviewed verifier")
            (root / "agent-go").write_text("signed artifact")
            summary = {"status": "passed", "tree": "tree", "verifier_sha256": upgrade.digest(root / "scripts/verify-mini"),
                       "artifact_sha256": upgrade.digest(root / "agent-go"), "checks": [
                           {"name": name, "passed": True} for name in
                           ["modules", "format", "test", "race", "vet", "build", "scripts", "artifact", "signed-artifact"]]}
            def verify():
                (root / "summary.json").write_text(json.dumps(summary))
                return upgrade.verified_artifact(root, root)
            with mock.patch.object(upgrade.subprocess, "check_output", side_effect=["tree", "main", b""]):
                self.assertEqual(verify()[0], root / "agent-go")
            summary["checks"] = summary["checks"][:-1]
            with self.assertRaises(RuntimeError):
                verify()
            summary["checks"].append({"name": "signed-artifact", "passed": True})
            for outputs in [["different", "main", b""], ["tree", "feature", b""], ["tree", "main", b" M file"]]:
                with mock.patch.object(upgrade.subprocess, "check_output", side_effect=outputs), self.assertRaises(RuntimeError):
                    verify()
            (root / "agent-go").write_text("tampered")
            with mock.patch.object(upgrade.subprocess, "check_output", side_effect=["tree", "main", b""]), self.assertRaises(RuntimeError):
                verify()

    def test_worker_never_replays_and_preserves_installer_failure(self):
        for success in [True, False]:
            with self.subTest(success=success), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                for name in ["installer.py", "agent-go", "config", "state.db"]:
                    (root / name).write_text(name)
                stat = (root / "state.db").stat()
                record = {"status": "queued", "expires": 10**12, "revision": "abc123",
                          "installer_sha256": upgrade.digest(root / "installer.py"),
                          "artifact_sha256": upgrade.digest(root / "agent-go"), "config_sha256": upgrade.digest(root / "config"),
                          "database_identity": [stat.st_dev, stat.st_ino]}
                path = root / "request.json"
                path.write_text(json.dumps(record))
                deploy = SimpleNamespace(DB=str(root / "state.db"), CONFIG=str(root / "config"), open_retained_db=mock.Mock())
                with mock.patch.object(upgrade, "installer", return_value=deploy), \
                     mock.patch.object(upgrade, "ready", return_value=True), \
                     mock.patch.object(upgrade.subprocess, "run", return_value=SimpleNamespace(returncode=0 if success else 1)) as dispatch:
                    if success:
                        upgrade.run(path)
                    else:
                        with self.assertRaises(RuntimeError):
                            upgrade.run(path)
                    self.assertEqual(json.loads(path.read_text())["status"], "completed" if success else "failed")
                    with self.assertRaises(RuntimeError):
                        upgrade.run(path)
                    self.assertEqual(dispatch.call_count, 1)


if __name__ == "__main__":
    unittest.main()
