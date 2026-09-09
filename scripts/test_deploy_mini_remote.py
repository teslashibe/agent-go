import hashlib
import importlib.util
import json
import os
import plistlib
import sqlite3
import subprocess
import sys
import tempfile
import unittest
from io import StringIO
from pathlib import Path
from unittest import mock


SCRIPT = Path(__file__).with_name("deploy-mini-remote.py")
SPEC = importlib.util.spec_from_file_location("deploy_mini_remote", SCRIPT)
deploy = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(deploy)


class DeployMiniRemoteTest(unittest.TestCase):
    def test_shell_entrypoints_do_not_accept_secret_or_choose_first_signer(self):
        ship = Path(__file__).with_name("ship-self").read_text(encoding="utf-8")
        deploy_script = Path(__file__).with_name("deploy-mini").read_text(encoding="utf-8")
        self.assertNotIn("--pin", ship)
        self.assertNotIn("NR==1", ship + deploy_script)
        self.assertNotIn("--secret-fd", ship)
        self.assertIn("remote_requirement", deploy_script)

    def test_ship_self_requires_confirmation_but_no_deploy_secret(self):
        # Exercise the real shell entrypoint with a stub installer: no native
        # commands, live files, signing, service changes or controlling terminal.
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            scripts = root / "scripts"
            scripts.mkdir()
            ship = scripts / "ship-self"
            ship.write_bytes(Path(__file__).with_name("ship-self").read_bytes())
            ship.chmod(0o700)
            (scripts / "deploy-mini-remote.py").write_text(
                "import json, sys; print(json.dumps(sys.argv[1:]))\n"
            )
            staged = root / "candidate"
            staged.touch()
            env = {"HOME": directory, "PATH": "/usr/bin:/bin"}
            args = [str(ship), "--version", "fixture", "--staged", str(staged)]
            missing = subprocess.run(args, stdin=subprocess.DEVNULL, env=env,
                                     capture_output=True, text=True, timeout=10)
            self.assertEqual(missing.returncode, 2)
            self.assertIn("without --confirm", missing.stderr)
            approved = subprocess.run(args + ["--confirm"], stdin=subprocess.DEVNULL,
                                      env=env, capture_output=True, text=True, timeout=10)
            self.assertEqual(approved.returncode, 0, approved.stderr)
            self.assertEqual(json.loads(approved.stdout),
                             ["--version", "fixture", "--staged", str(staged)])
            for flag in ("--pin", "--secret-fd"):
                rejected = subprocess.run(args + ["--confirm", flag, "0"],
                                          stdin=subprocess.DEVNULL, env=env,
                                          capture_output=True, text=True, timeout=10)
                self.assertEqual(rejected.returncode, 2)
                self.assertIn("unknown argument", rejected.stderr)

    def test_version_rejects_shell_syntax(self):
        for value in ("ok-1.2", "release_3"):
            with mock.patch.object(sys, "argv", ["deploy", "--version", value, "--restart-only"]):
                self.assertEqual(deploy.parse_args().version, value)
        for value in ("x;touch /tmp/pwn", "$(id)", "../x", ""):
            with self.subTest(value=value), mock.patch.object(
                sys, "argv", ["deploy", "--version", value, "--restart-only"]
            ), self.assertRaises(SystemExit):
                deploy.parse_args()

    def test_idle_counts_only_unresolved_effects(self):
        db = sqlite3.connect(":memory:")
        db.executescript(
            """
            CREATE TABLE jobs(id INTEGER PRIMARY KEY, state TEXT, error TEXT, ack_state TEXT);
            CREATE TABLE replies(state TEXT);
            CREATE TABLE reminders(status TEXT);
            CREATE TABLE sources(paused INTEGER, source_invalid INTEGER);
            CREATE TABLE note_actions(job_id INTEGER, state TEXT);
            CREATE TABLE native_note_claims(job_id INTEGER, operation_id TEXT);
            CREATE TABLE tool_operations(job_id INTEGER, operation_id TEXT, state TEXT, resolution TEXT);
            INSERT INTO jobs VALUES (1,'completed','','unknown');
            INSERT INTO jobs VALUES (2,'completed','','dispatching');
            INSERT INTO jobs VALUES (3,'unknown','','submitted');
            INSERT INTO tool_operations VALUES (1,'old','unknown','abandoned');
            INSERT INTO tool_operations VALUES (3,'active','unknown','');
            """
        )
        counts = deploy.idle(db)
        self.assertEqual(counts["effects"], 1)
        self.assertEqual(counts["jobs"], 2)

    def test_health_ignores_historical_unknown_acknowledgements(self):
        db = sqlite3.connect(":memory:")
        db.executescript(
            """
            CREATE TABLE jobs(id INTEGER PRIMARY KEY, state TEXT, error TEXT, ack_state TEXT);
            CREATE TABLE replies(state TEXT);
            CREATE TABLE reminders(status TEXT);
            CREATE TABLE sources(paused INTEGER, source_invalid INTEGER);
            CREATE TABLE note_actions(job_id INTEGER, state TEXT);
            CREATE TABLE native_note_claims(job_id INTEGER, operation_id TEXT);
            CREATE TABLE tool_operations(job_id INTEGER, operation_id TEXT, state TEXT, resolution TEXT);
            INSERT INTO jobs VALUES (1,'completed','','unknown');
            """
        )
        self.assertFalse(any(deploy.health_state(db).values()))

    def test_resolved_note_and_closed_approval_evidence_do_not_block(self):
        db = sqlite3.connect(":memory:")
        db.executescript(
            """
            CREATE TABLE jobs(id INTEGER PRIMARY KEY, state TEXT, error TEXT, ack_state TEXT);
            CREATE TABLE replies(state TEXT);
            CREATE TABLE reminders(status TEXT);
            CREATE TABLE sources(paused INTEGER, source_invalid INTEGER);
            CREATE TABLE note_actions(job_id INTEGER, state TEXT);
            CREATE TABLE native_note_claims(job_id INTEGER, operation_id TEXT);
            CREATE TABLE tool_operations(job_id INTEGER, operation_id TEXT, state TEXT, resolution TEXT);
            CREATE TABLE approvals(state TEXT, delivery TEXT);
            INSERT INTO jobs VALUES (1,'failed','reviewed','unknown');
            INSERT INTO note_actions VALUES (1,'unknown');
            INSERT INTO native_note_claims VALUES (1,'delete');
            INSERT INTO tool_operations VALUES (1,'delete','unknown','abandoned');
            INSERT INTO approvals VALUES ('closed','unknown');
            """
        )
        self.assertFalse(any(deploy.idle(db).values()))
        self.assertFalse(any(deploy.health_state(db).values()))
        db.execute("UPDATE tool_operations SET resolution=''")
        db.commit()
        self.assertEqual(deploy.idle(db)["notes"], 1)
        self.assertEqual(deploy.idle(db)["effects"], 1)

    def test_runtime_reconcile_is_noop_without_computer_use(self):
        with tempfile.TemporaryDirectory() as directory:
            config = os.path.join(directory, "config.json")
            Path(config).write_text('{"mcp_servers":{}}', encoding="utf-8")
            with mock.patch.object(deploy, "CONFIG", config), mock.patch.object(
                deploy, "discover_openai_runtime"
            ) as discover:
                deploy.reconcile_runtime_paths("/unused")
                discover.assert_not_called()

    def test_recursive_server_path_migration(self):
        old = "/Users/test/Applications/ChatGPT.app"
        new = "/Applications/Codex.app"
        value = {
            "command": old + "/Contents/Resources/cua_node/bin/node",
            "args": [old + "/Contents/Resources/cua_node/index.js"],
            "cwd": old + "/Contents/Resources",
            "env": {"RUNTIME": old + "/Contents/Resources/codex"},
            "number": 1,
        }
        migrated = deploy.replace_string_tree(value, old, new)
        self.assertNotIn(old, json_text(migrated))
        self.assertEqual(migrated["number"], 1)

    def test_home_app_discovery_prefers_configured_valid_app(self):
        with tempfile.TemporaryDirectory() as home:
            app = Path(home, "Applications", "ChatGPT.app")
            resources = app / "Contents" / "Resources"
            for executable in ("cua_node/bin/node", "cua_node/bin/node_repl", "codex"):
                path = resources / executable
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text("", encoding="utf-8")
                path.chmod(0o700)
            with open(app / "Contents" / "Info.plist", "wb") as handle:
                plistlib.dump({"CFBundleVersion": "9001"}, handle)
            completed = mock.Mock(returncode=0, stdout="", stderr="Identifier=com.openai.codex\nTeamIdentifier=2DC432GLL2")
            real_isdir = os.path.isdir
            with mock.patch.object(deploy, "HOME", home), mock.patch.object(
                deploy, "run", return_value=completed
            ), mock.patch("os.path.isdir", side_effect=lambda path: real_isdir(path) if path.startswith(home) else False):
                selected, executables = deploy.discover_openai_runtime(str(app))
            self.assertEqual(selected, str(app))
            self.assertEqual(executables["cua_repl"], str(resources / "cua_node/bin/node"))

    def test_current_toml_hash_mismatch_is_rejected_before_probe(self):
        with tempfile.TemporaryDirectory() as directory:
            config = Path(directory, "config.json")
            codex_home = Path(directory, "codex")
            codex_home.mkdir()
            (codex_home / "config.toml").write_text("[mcp_servers.cua_repl]\n", encoding="utf-8")
            root = "/Applications/ChatGPT.app"
            config.write_text(
                '{"mcp_servers":{"cua_repl":{"command":"%s/Contents/Resources/cua_node/bin/node"},'
                '"node_repl":{"command":"%s/Contents/Resources/node_repl"}},'
                '"interactive_config":{"CodexHome":"%s","ConfigSHA256":"wrong","MCPServersSHA256":"old"}}'
                % (root, root, codex_home),
                encoding="utf-8",
            )
            with mock.patch.object(deploy, "CONFIG", str(config)), mock.patch.object(
                deploy, "discover_openai_runtime",
                return_value=(root, {"cua_repl": root + "/Contents/Resources/cua_node/bin/node", "node_repl": root + "/Contents/Resources/node_repl", "codex": root + "/Contents/Resources/codex"}),
            ), mock.patch.object(deploy, "probe_mcp") as probe:
                with self.assertRaisesRegex(RuntimeError, "config.toml hash mismatch"):
                    deploy.reconcile_runtime_paths("/binary")
                probe.assert_not_called()

    def test_runtime_reconcile_changes_only_reviewed_paths_and_hashes(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            config = root / "config.json"
            codex_home = root / "codex"
            codex_home.mkdir()
            toml_path = codex_home / "config.toml"
            old = "/Users/test/Applications/Codex.app"
            new = "/Users/test/Applications/ChatGPT.app"
            toml = (
                "[mcp_servers.cua_repl]\n"
                f'command = "{old}/Contents/Resources/cua_node/bin/node"\n'
                "[mcp_servers.cua_repl.env]\n"
                f'NODE_REPL_NODE_PATH = "{old}/Contents/Resources/cua_node/bin/node"\n'
                "[mcp_servers.node_repl]\n"
                f'command = "{old}/Contents/Resources/cua_node/bin/node_repl"\n'
            )
            toml_path.write_text(toml, encoding="utf-8")
            document = {
                "computer_use": True,
                "mcp_servers": {
                    "cua_repl": {
                        "command": old + "/Contents/Resources/cua_node/bin/node",
                        "args": [old + "/Contents/Resources/cua_node/launch.mjs"],
                        "env": {"NODE_REPL_NODE_PATH": old + "/Contents/Resources/cua_node/bin/node"},
                    },
                    "node_repl": {
                        "command": old + "/Contents/Resources/cua_node/bin/node_repl",
                        "env": {"CODEX_CLI_PATH": old + "/Contents/Resources/codex"},
                    },
                },
                "interactive_config": {
                    "CodexHome": str(codex_home),
                    "ConfigSHA256": hashlib.sha256(toml.encode()).hexdigest(),
                    "MCPServersSHA256": "old-fingerprint",
                    "Sources": {"/reviewed/source": "unchanged"},
                },
                "unrelated": {"keep": True},
            }
            config.write_text(json.dumps(document), encoding="utf-8")
            executables = {
                "cua_repl": new + "/Contents/Resources/cua_node/bin/node",
                "node_repl": new + "/Contents/Resources/cua_node/bin/node_repl",
                "codex": new + "/Contents/Resources/codex",
            }

            def fingerprint(*args, **_kwargs):
                value = "old-fingerprint" if args[2] == str(config) else "new-fingerprint"
                return subprocess.CompletedProcess(args, 0, stdout=value + "\n", stderr="")

            with mock.patch.object(deploy, "CONFIG", str(config)), mock.patch.object(
                deploy, "discover_openai_runtime", return_value=(new, executables)
            ), mock.patch.object(
                deploy, "probe_mcp", return_value={"js", "js_reset"}
            ) as probe, mock.patch.object(
                deploy, "run", side_effect=fingerprint
            ):
                self.assertEqual(deploy.reconcile_runtime_paths("/binary"), str(toml_path))

            migrated = json.loads(config.read_text(encoding="utf-8"))
            self.assertEqual(migrated["unrelated"], document["unrelated"])
            self.assertEqual(
                migrated["interactive_config"]["Sources"],
                document["interactive_config"]["Sources"],
            )
            self.assertEqual(migrated["interactive_config"]["MCPServersSHA256"], "new-fingerprint")
            self.assertEqual(
                migrated["interactive_config"]["ConfigSHA256"],
                hashlib.sha256(toml_path.read_bytes()).hexdigest(),
            )
            self.assertNotIn(old, json.dumps(migrated["mcp_servers"]))
            self.assertNotIn(old, toml_path.read_text(encoding="utf-8"))
            self.assertEqual(probe.call_count, 2)

    def test_probe_uses_reviewed_cwd_environment_and_own_tools(self):
        process = mock.Mock()
        process.stdin = StringIO()
        process.stdout = StringIO('{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"js"}]}}\n')
        process.stderr = StringIO()
        server = {"command": "/runtime/node", "args": ["server.js"], "cwd": "/reviewed", "env": {"ONLY_TEST": "yes"}}
        with mock.patch.object(deploy.subprocess, "Popen", return_value=process) as popen, mock.patch.object(
            deploy.select, "select", return_value=([process.stdout], [], [])
        ):
            self.assertEqual(deploy.probe_mcp(server), {"js"})
        _, kwargs = popen.call_args
        self.assertEqual(popen.call_args.args[0], ["/runtime/node", "server.js"])
        self.assertEqual(kwargs["cwd"], "/reviewed")
        self.assertEqual(kwargs["env"]["ONLY_TEST"], "yes")

    def test_backup_marks_immutable_without_copying_flags(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            db = root / "state.db"
            sqlite3.connect(db).close()
            files = [root / name for name in ("agent-go", "config.json", "config.toml", "agent.plist")]
            for path in files:
                path.write_text("x", encoding="utf-8")
            with mock.patch.object(deploy, "ROOT", directory), mock.patch.object(
                deploy, "DB", str(db)
            ), mock.patch.object(deploy, "LIVE", str(files[0])), mock.patch.object(
                deploy, "CONFIG", str(files[1])
            ), mock.patch.object(deploy, "PLIST", str(files[3])), mock.patch.object(
                deploy, "is_immutable", return_value=True
            ):
                backup = deploy.backup("test", str(files[2]))
            self.assertTrue(Path(backup, "config.toml.immutable").exists())
            self.assertEqual(Path(backup, "config.toml").read_text(encoding="utf-8"), "x")

    def test_failure_path_stops_process_before_restore(self):
        source = SCRIPT.read_text(encoding="utf-8")
        failure = source[source.index("    except Exception:\n", source.index("def main()")):]
        self.assertLess(failure.index("bootout()"), failure.index("wait_stopped"))
        self.assertLess(failure.index("wait_stopped"), failure.index("restore("))


def json_text(value):
    import json
    return json.dumps(value, sort_keys=True)


if __name__ == "__main__":
    unittest.main()
