import importlib.machinery
import importlib.util
import json
from pathlib import Path
import sqlite3
import tempfile
import unittest
from unittest import mock

loader = importlib.machinery.SourceFileLoader("mini_health", str(Path(__file__).with_name("mini-health")))
spec = importlib.util.spec_from_loader(loader.name, loader)
health = importlib.util.module_from_spec(spec)
loader.exec_module(health)


class HealthTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.path = Path(self.temp.name) / "fixture?#.db"
        self.writer = sqlite3.connect(self.path)
        self.addCleanup(self.writer.close)
        self.writer.executescript("""
            PRAGMA journal_mode=WAL;
            CREATE TABLE jobs(id INTEGER, state TEXT, ack_state TEXT, secret TEXT);
            CREATE TABLE replies(state TEXT);
            CREATE TABLE reminders(status TEXT);
            CREATE TABLE sources(paused INTEGER, source_invalid INTEGER);
            CREATE TABLE note_actions(job_id INTEGER, state TEXT);
            CREATE TABLE native_note_claims(job_id INTEGER, operation_id TEXT);
            CREATE TABLE tool_operations(job_id INTEGER, operation_id TEXT, state TEXT, resolution TEXT);
            CREATE TABLE approvals(state TEXT);
            INSERT INTO jobs VALUES(1,'unknown','','private-fixture@example.invalid');
            INSERT INTO tool_operations VALUES(1,'fixture','unknown','');
            INSERT INTO tool_operations VALUES(2,'abandoned','unknown','abandoned');
            INSERT INTO approvals VALUES('waiting');
        """)
        self.deploy = health.installer()
        self.patch = mock.patch.object(self.deploy, 'service_info', return_value={
            'state': 'running', 'pid': '123', 'last_exit': 'PRIVATE_SENTINEL'})
        self.patch.start()
        self.addCleanup(self.patch.stop)

    def test_wal_counts_and_no_database_writes(self):
        files = [self.path, Path(str(self.path) + '-wal')]
        before = [p.read_bytes() for p in files]
        result = health.snapshot(self.deploy, self.path)
        self.assertEqual(result['errors'], [])
        self.assertEqual(result['idle_counts']['jobs'], 1)
        self.assertEqual(result['health_counts']['unresolved_effects'], 1)
        self.assertEqual(result['health_counts']['unresolved_approvals'], 1)
        self.assertFalse(result['idle'])
        self.assertEqual(before, [p.read_bytes() for p in files])
        self.assertNotIn('PRIVATE_SENTINEL', json.dumps(result))
        self.assertNotIn('example.invalid', json.dumps(result))
        self.assertNotIn(str(self.path), json.dumps(result))

    def test_readonly_connection_rejects_writes_even_without_query_only(self):
        original = self.deploy.idle
        def probe(db):
            self.assertEqual(db.execute('PRAGMA query_only').fetchone()[0], 1)
            with self.assertRaises(sqlite3.OperationalError):
                db.execute("DELETE FROM jobs")
            db.rollback()
            db.execute('PRAGMA query_only=OFF')
            with self.assertRaises(sqlite3.OperationalError):
                db.execute("DELETE FROM jobs")
            db.rollback()
            db.execute('PRAGMA query_only=ON')
            return original(db)
        with mock.patch.object(self.deploy, 'idle', side_effect=probe):
            self.assertEqual(health.snapshot(self.deploy, self.path)['errors'], [])

    def test_missing_database_is_not_created(self):
        missing = self.path.with_name('missing.db')
        result = health.snapshot(self.deploy, missing)
        self.assertEqual(result['errors'], ['database_unavailable'])
        self.assertFalse(missing.exists())
        self.assertNotIn('idle', result)

    def test_errors_and_arbitrary_service_fields_are_not_exposed(self):
        self.deploy.service_info.return_value = {'state': 'PRIVATE_SENTINEL', 'pid': 'PRIVATE_SENTINEL'}
        with mock.patch.object(self.deploy, 'health_state', side_effect=RuntimeError('PRIVATE_SENTINEL')):
            result = health.snapshot(self.deploy, self.path)
        self.assertEqual(result['errors'], ['service_unavailable', 'database_unavailable'])
        self.assertEqual(result['service'], {'state': 'unknown', 'pid': None})
        self.assertNotIn('idle_counts', result)
        self.assertNotIn('PRIVATE_SENTINEL', json.dumps(result))

    def test_unavailable_service_still_reports_counts(self):
        self.deploy.service_info.side_effect = OSError('PRIVATE_SENTINEL')
        result = health.snapshot(self.deploy, self.path)
        self.assertEqual(result['errors'], ['service_unavailable'])
        self.assertIn('health_counts', result)

    def test_incompatible_schema_is_safe(self):
        self.writer.execute('DROP TABLE replies')
        self.writer.commit()
        self.assertEqual(health.snapshot(self.deploy, self.path)['errors'], ['database_unavailable'])


if __name__ == '__main__':
    unittest.main()
