#!/usr/bin/env python3
"""Install a pre-signed agent-go on the Mac mini.

This is the remote half of scripts/deploy-mini. It is the v0.7.0 one-shot
installer from 2026-09-05 (idle check, designated-requirement match, bootout,
backup, atomic replace, bootstrap, rollback) plus a staged exec preflight so a
codesign-killed binary never replaces the live agent.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import plistlib
import re
import select
import shutil
import sqlite3
import stat
import subprocess
import sys
import time

HOME = os.path.expanduser("~")
ROOT = os.path.join(HOME, ".local/share/agent-go")
DB = os.path.join(ROOT, "state.db")
LIVE = os.path.join(HOME, ".local/bin/agent-go")
CONFIG = os.path.join(ROOT, "config.json")
PLIST = os.path.join(HOME, "Library/LaunchAgents/ai.teslashibe.agent.plist")
DOMAIN = "gui/" + str(os.getuid())
LABEL = DOMAIN + "/ai.teslashibe.agent"
VERSION_RE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$")


class UnsafeToRollback(RuntimeError):
    """Durable work changed after bootstrap; preserve the installed candidate."""


UNRESOLVED_NOTES_SQL = """
SELECT count(*)
FROM note_actions a
JOIN jobs j ON j.id=a.job_id
WHERE a.state IN ('started','created_unshared','sharing_unverified','unknown')
  AND (
    j.state IN ('running','unknown')
    OR EXISTS (
      SELECT 1
      FROM native_note_claims n
      JOIN tool_operations o
        ON o.job_id=n.job_id AND o.operation_id=n.operation_id
      WHERE n.job_id=a.job_id
        AND o.state IN ('dispatching','unknown')
        AND o.resolution=''
    )
  )
"""


def run(*args: str, check: bool = True) -> subprocess.CompletedProcess[str]:
    return subprocess.run(args, check=check, capture_output=True, text=True)


def requirement(path: str) -> str:
    result = run("codesign", "-d", "-r-", path)
    text = result.stdout + "\n" + result.stderr
    for line in text.splitlines():
        if line.startswith("designated => "):
            return line.split(" => ", 1)[1].strip()
    raise RuntimeError(f"no designated requirement for {path}")


def open_retained_db() -> sqlite3.Connection:
    db = sqlite3.connect("file:" + DB + "?mode=ro", uri=True)
    db.execute("PRAGMA query_only=ON")
    return db


def idle(db: sqlite3.Connection) -> dict[str, int]:
    # An unknown acknowledgement is retained delivery evidence, not replayable
    # queued work. Only an acknowledgement actively being dispatched blocks.
    queries = {
        "jobs": "SELECT count(*) FROM jobs WHERE state IN ('queued','running','unknown') OR ack_state='dispatching'",
        "replies": "SELECT count(*) FROM replies WHERE state IN ('pending','dispatching','unknown')",
        "reminders": "SELECT count(*) FROM reminders WHERE status IN ('dispatching','unknown')",
        "paused_sources": "SELECT count(*) FROM sources WHERE paused=1",
        "invalid_sources": "SELECT count(*) FROM sources WHERE source_invalid=1",
        "notes": UNRESOLVED_NOTES_SQL,
        "effects": "SELECT count(*) FROM tool_operations WHERE state IN ('dispatching','unknown') AND resolution=''",
    }
    if db.execute("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='approvals'").fetchone()[0]:
        queries["approvals"] = "SELECT count(*) FROM approvals WHERE state IN ('waiting','decided')"
    db.execute("BEGIN")
    try:
        results = {name: db.execute(sql).fetchone()[0] for name, sql in queries.items()}
    finally:
        db.execute("COMMIT")
    return results


def require_idle(db: sqlite3.Connection) -> dict[str, int]:
    results = idle(db)
    if any(results.values()):
        raise RuntimeError("not idle; deploy refused: " + json.dumps(results))
    return results


def bootout() -> None:
    result = run("launchctl", "bootout", LABEL, check=False)
    if result.returncode == 0:
        return
    text = result.stdout + result.stderr
    if "No such process" in text or "Could not find service" in text:
        return
    raise RuntimeError("launchctl bootout failed: " + text.strip())


def bootstrap() -> None:
    run("launchctl", "bootstrap", DOMAIN, PLIST)


def service_info() -> dict[str, str]:
    result = run("launchctl", "print", LABEL, check=False)
    text = result.stdout + result.stderr
    info = {"state": "", "pid": "", "last_exit": ""}
    for line in text.splitlines():
        stripped = line.strip()
        if stripped.startswith("state =") and not info["state"]:
            info["state"] = stripped.split("=", 1)[1].strip()
        elif stripped.startswith("pid =") and not info["pid"]:
            info["pid"] = stripped.split("=", 1)[1].strip()
        elif stripped.startswith("last exit reason =") and not info["last_exit"]:
            info["last_exit"] = stripped.split("=", 1)[1].strip()
    return info


def error_count(db: sqlite3.Connection) -> int:
    db.execute("BEGIN")
    try:
        return db.execute("SELECT count(*) FROM jobs WHERE error<>''").fetchone()[0]
    finally:
        db.execute("COMMIT")


def health_state(db: sqlite3.Connection) -> dict[str, int]:
    queries = {
        "paused_sources": "SELECT count(*) FROM sources WHERE paused=1",
        "invalid_sources": "SELECT count(*) FROM sources WHERE source_invalid=1",
        "unknown_jobs": "SELECT count(*) FROM jobs WHERE state='unknown'",
        "unknown_replies": "SELECT count(*) FROM replies WHERE state='unknown'",
        "unknown_reminders": "SELECT count(*) FROM reminders WHERE status='unknown'",
        "unresolved_notes": UNRESOLVED_NOTES_SQL,
        "unresolved_effects": "SELECT count(*) FROM tool_operations WHERE state IN ('dispatching','unknown') AND resolution=''",
    }
    if db.execute("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='approvals'").fetchone()[0]:
        queries["unresolved_approvals"] = "SELECT count(*) FROM approvals WHERE state IN ('waiting','decided')"
    db.execute("BEGIN")
    try:
        return {name: db.execute(sql).fetchone()[0] for name, sql in queries.items()}
    finally:
        db.execute("COMMIT")


def wait_healthy(db: sqlite3.Connection, errors_before: int, duration: float = 70.0) -> dict[str, str]:
    timeout = duration + 20
    deadline = time.time() + timeout
    stable_since: float | None = None
    stable_pid = ""
    last = service_info()
    while time.time() < deadline:
        last = service_info()
        running = bool(last.get("pid")) and last.get("state") == "running"
        if running:
            ps = run("ps", "-p", last["pid"], "-o", "command=", check=False)
            if LIVE in ps.stdout:
                if stable_pid != last["pid"]:
                    stable_pid = last["pid"]
                    stable_since = time.time()
                counts = health_state(db)
                if any(counts.values()):
                    raise UnsafeToRollback("durable state became uncertain during health check: " + json.dumps(counts))
                if error_count(db) > errors_before:
                    raise RuntimeError("new durable errors during health check")
                if stable_since is not None and time.time() - stable_since >= duration:
                    return last
            else:
                stable_since = None
                stable_pid = ""
        if not running and last.get("last_exit") == "OS_REASON_CODESIGNING":
            raise RuntimeError("launchd codesign kill: " + json.dumps(last))
        time.sleep(1)
    raise RuntimeError("service did not stay up: " + json.dumps(last))


def preflight(staged: str) -> None:
    run("codesign", "--verify", "--strict", staged)
    staged_dr = requirement(staged)
    live_dr = requirement(LIVE)
    if live_dr != staged_dr:
        raise RuntimeError("signing requirement mismatch")
    run("xattr", "-d", "com.apple.quarantine", staged, check=False)
    # No -config prints usage and exits 2. SIGKILL means AMFI rejected the binary.
    probed = subprocess.run([staged], capture_output=True, text=True)
    if probed.returncode == -9:
        raise RuntimeError("staged binary killed by codesign before replace")
    if probed.returncode != 2:
        raise RuntimeError(f"staged binary unexpected exit {probed.returncode}: {probed.stderr.strip()}")


def app_root(value: str) -> str | None:
    match = re.match(r"^(.+/(?:ChatGPT|Codex)\.app)(?:/|$)", value)
    return match.group(1) if match else None


def app_roots(value: str) -> set[str]:
    roots = set(re.findall(r"(?:\$HOME|~)(?:/[^\s\"':=]+)*/(?:ChatGPT|Codex)\.app", value))
    remainder = value
    for root in roots:
        remainder = remainder.replace(root, "")
    roots.update(re.findall(r"/[^\s\"':=]+(?:/[^\s\"':=]+)*/(?:ChatGPT|Codex)\.app", remainder))
    return roots


def expanded_path(value: str) -> str:
    return os.path.normpath(os.path.expandvars(os.path.expanduser(value)))


def validate_openai_runtime(app: str) -> tuple[tuple[int, ...], dict[str, str]] | None:
    if not os.path.isdir(app) or run("codesign", "--verify", "--strict", app, check=False).returncode:
        return None
    metadata = run("codesign", "-dv", "--verbose=2", app, check=False)
    text = metadata.stdout + metadata.stderr
    if metadata.returncode or "Identifier=com.openai.codex" not in text or "TeamIdentifier=2DC432GLL2" not in text:
        return None
    resources = os.path.join(app, "Contents", "Resources")
    executables = {
        "cua_repl": os.path.join(resources, "cua_node", "bin", "node"),
        "node_repl": os.path.join(resources, "cua_node", "bin", "node_repl"),
        "codex": os.path.join(resources, "codex"),
    }
    if not all(os.path.isfile(path) and os.access(path, os.X_OK) for path in executables.values()):
        return None
    with open(os.path.join(app, "Contents", "Info.plist"), "rb") as handle:
        raw_version = str(plistlib.load(handle).get("CFBundleVersion", ""))
    if not re.fullmatch(r"\d+(?:\.\d+)*", raw_version):
        return None
    return tuple(int(part) for part in raw_version.split(".")), executables


def discover_openai_runtime(configured_app: str) -> tuple[str, dict[str, str]]:
    candidates: list[tuple[tuple[int, ...], str, dict[str, str]]] = []
    for app in (
        os.path.join(HOME, "Applications", "ChatGPT.app"),
        os.path.join(HOME, "Applications", "Codex.app"),
        "/Applications/ChatGPT.app",
        "/Applications/Codex.app",
    ):
        validated = validate_openai_runtime(app)
        if validated:
            candidates.append((validated[0], app, validated[1]))
    preferred = [candidate for candidate in candidates if os.path.normpath(candidate[1]) == expanded_path(configured_app)]
    if len(preferred) == 1:
        return preferred[0][1], preferred[0][2]
    if not candidates:
        raise RuntimeError("no valid signed OpenAI runtime app")
    highest = max(candidate[0] for candidate in candidates)
    newest = [candidate for candidate in candidates if candidate[0] == highest]
    if len(newest) != 1:
        raise RuntimeError(f"ambiguous OpenAI runtime apps at CFBundleVersion {'.'.join(map(str, highest))}")
    return newest[0][1], newest[0][2]


def probe_mcp(server: dict[str, object]) -> set[str]:
    command = server["command"]
    args = server.get("args") or []
    cwd = server.get("cwd")
    extra_env = server.get("env") or {}
    if not isinstance(command, str) or not isinstance(args, list) or not all(isinstance(arg, str) for arg in args):
        raise RuntimeError("invalid reviewed MCP command or arguments")
    if cwd is not None and not isinstance(cwd, str):
        raise RuntimeError("invalid reviewed MCP cwd")
    if not isinstance(extra_env, dict) or not all(isinstance(key, str) and isinstance(value, str) for key, value in extra_env.items()):
        raise RuntimeError("invalid reviewed MCP environment")
    env = os.environ.copy()
    env.update(extra_env)
    proc = subprocess.Popen([command, *args], cwd=cwd, env=env, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    assert proc.stdin is not None and proc.stdout is not None
    try:
        for message in (
            {"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"protocolVersion": "2025-06-18", "capabilities": {}, "clientInfo": {"name": "agent-go-deploy", "version": "1"}}},
            {"jsonrpc": "2.0", "method": "notifications/initialized"},
            {"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": {}},
        ):
            proc.stdin.write(json.dumps(message, separators=(",", ":")) + "\n")
        proc.stdin.flush()
        deadline = time.time() + 10
        while time.time() < deadline:
            ready, _, _ = select.select([proc.stdout], [], [], max(0, deadline - time.time()))
            if not ready:
                break
            line = proc.stdout.readline()
            if not line:
                break
            message = json.loads(line)
            if message.get("id") == 2:
                return {tool["name"] for tool in message.get("result", {}).get("tools", [])}
        raise RuntimeError(f"MCP probe failed for {os.path.basename(command)}")
    finally:
        proc.terminate()
        try:
            proc.wait(timeout=2)
        except subprocess.TimeoutExpired:
            proc.kill()


def replace_string_tree(value: object, old_root: str, new_root: str) -> object:
    if isinstance(value, str):
        return value.replace(old_root, new_root)
    if isinstance(value, list):
        return [replace_string_tree(item, old_root, new_root) for item in value]
    if isinstance(value, dict):
        return {key: replace_string_tree(item, old_root, new_root) for key, item in value.items()}
    return value


def string_values(value: object) -> list[str]:
    if isinstance(value, str):
        return [value]
    if isinstance(value, list):
        return [text for item in value for text in string_values(item)]
    if isinstance(value, dict):
        return [text for item in value.values() for text in string_values(item)]
    return []


def reconcile_runtime_paths(binary: str) -> str | None:
    with open(CONFIG, encoding="utf-8") as handle:
        current = json.load(handle)
    cfg = json.loads(json.dumps(current))
    servers = cfg.get("mcp_servers") or {}
    present = {name for name in ("cua_repl", "node_repl") if name in servers}
    if not present:
        return None
    interactive = cfg.get("interactive_config")
    if present != {"cua_repl", "node_repl"} or not isinstance(interactive, dict):
        raise RuntimeError("partial reviewed computer-use configuration")
    codex_home = interactive.get("CodexHome")
    if not isinstance(codex_home, str) or not os.path.isabs(os.path.expanduser(codex_home)):
        raise RuntimeError("invalid reviewed CodexHome")
    codex_config = os.path.join(os.path.expanduser(codex_home), "config.toml")
    roots = {app_root(servers[name].get("command", "")) for name in ("cua_repl", "node_repl")}
    if None in roots or len(roots) != 1:
        raise RuntimeError("partial or mismatched configured OpenAI app roots")
    old_root = roots.pop()
    assert old_root is not None
    referenced_roots = {
        root
        for name in ("cua_repl", "node_repl")
        for value in string_values(servers[name])
        for root in app_roots(value)
    }
    if referenced_roots != {old_root}:
        raise RuntimeError("partial or mismatched OpenAI app roots in reviewed MCP fields")
    app, executables = discover_openai_runtime(old_root)

    if sha256(codex_config) != interactive.get("ConfigSHA256"):
        raise RuntimeError("current reviewed config.toml hash mismatch")
    current_mcp = run(binary, "maintenance-mcp-servers-sha256", CONFIG).stdout.strip()
    if current_mcp != interactive.get("MCPServersSHA256"):
        raise RuntimeError("current reviewed MCP server hash mismatch")

    for name in ("cua_repl", "node_repl"):
        servers[name] = replace_string_tree(servers[name], old_root, app)
        if servers[name].get("command") != executables[name]:
            raise RuntimeError(f"migrated {name} command is not the exact reviewed executable")
    with open(codex_config, encoding="utf-8") as handle:
        toml = handle.read()
    migrated_lines: list[str] = []
    section = ""
    for line in toml.splitlines(keepends=True):
        if line.lstrip().startswith("["):
            section = line.strip().strip("[]")
        changed = line
        if old_root in changed:
            if section not in ("mcp_servers.cua_repl", "mcp_servers.cua_repl.env", "mcp_servers.node_repl", "mcp_servers.node_repl.env"):
                raise RuntimeError(f"unreviewed runtime path in config.toml section {section}")
            changed = changed.replace(old_root, app)
        migrated_lines.append(changed)
    migrated_toml = "".join(migrated_lines)

    for name, required in (("cua_repl", {"js", "js_reset"}), ("node_repl", {"js", "js_reset"})):
        tools = probe_mcp(servers[name])
        if not tools or not required.issubset(tools):
            raise RuntimeError(f"{name} MCP tool contract mismatch: {sorted(tools)}")
    if expanded_path(old_root) == os.path.normpath(app):
        return codex_config

    toml_flags = os.stat(codex_config).st_flags & stat.UF_IMMUTABLE
    config_flags = os.stat(CONFIG).st_flags & stat.UF_IMMUTABLE
    temp_toml = codex_config + ".new"
    temp_config = CONFIG + ".new"
    try:
        with open(temp_toml, "w", encoding="utf-8") as handle:
            handle.write(migrated_toml)
        os.chmod(temp_toml, 0o600)
        interactive["ConfigSHA256"] = sha256(temp_toml)
        with open(temp_config, "w", encoding="utf-8") as handle:
            json.dump(cfg, handle, indent=2)
            handle.write("\n")
        interactive["MCPServersSHA256"] = run(binary, "maintenance-mcp-servers-sha256", temp_config).stdout.strip()
        with open(temp_config, "w", encoding="utf-8") as handle:
            json.dump(cfg, handle, indent=2)
            handle.write("\n")
        os.chmod(temp_config, 0o600)
        allowed = json.loads(json.dumps(current))
        allowed["mcp_servers"]["cua_repl"] = cfg["mcp_servers"]["cua_repl"]
        allowed["mcp_servers"]["node_repl"] = cfg["mcp_servers"]["node_repl"]
        allowed["interactive_config"]["ConfigSHA256"] = interactive["ConfigSHA256"]
        allowed["interactive_config"]["MCPServersSHA256"] = interactive["MCPServersSHA256"]
        if cfg != allowed:
            raise RuntimeError("migration changed unreviewed config fields")
        if toml_flags:
            run("chflags", "nouchg", codex_config)
        if config_flags:
            run("chflags", "nouchg", CONFIG)
        os.replace(temp_toml, codex_config)
        os.replace(temp_config, CONFIG)
    finally:
        for temp in (temp_toml, temp_config):
            try:
                os.unlink(temp)
            except FileNotFoundError:
                pass
        if toml_flags:
            run("chflags", "uchg", codex_config)
        if config_flags:
            run("chflags", "uchg", CONFIG)
    return codex_config


def reviewed_codex_config() -> str | None:
    with open(CONFIG, encoding="utf-8") as handle:
        interactive = json.load(handle).get("interactive_config")
    if not isinstance(interactive, dict) or not isinstance(interactive.get("CodexHome"), str):
        return None
    return os.path.join(os.path.expanduser(interactive["CodexHome"]), "config.toml")


def is_immutable(path: str) -> bool:
    return bool(getattr(os.stat(path), "st_flags", 0) & stat.UF_IMMUTABLE)


def backup(version: str, codex_config: str | None) -> str:
    dest = os.path.join(ROOT, "backups", f"{version}-{time.strftime('%Y%m%d-%H%M%S')}")
    os.makedirs(dest, mode=0o700)
    db = sqlite3.connect(DB)
    copy = sqlite3.connect(os.path.join(dest, "state.db"))
    db.backup(copy)
    copy.close()
    db.close()
    paths = [LIVE, CONFIG, PLIST]
    if codex_config:
        paths.append(codex_config)
        with open(os.path.join(dest, "config-toml-path"), "w", encoding="utf-8") as handle:
            handle.write(codex_config)
    for path in paths:
        if os.path.exists(path):
            destination = os.path.join(dest, os.path.basename(path))
            shutil.copyfile(path, destination)
            os.chmod(destination, 0o700 if path == LIVE else 0o600)
            if is_immutable(path):
                marker = os.path.join(dest, os.path.basename(path) + ".immutable")
                with open(marker, "w", encoding="utf-8"):
                    pass
    return dest


def install_file(src: str, dst: str, mode: int) -> None:
    # Rename a fresh file over the destination. In-place cp of a binary AMFI
    # has already killed keeps the dead inode; v0.7.0 used replace for this.
    tmp = dst + ".new"
    try:
        shutil.copyfile(src, tmp)
        os.chmod(tmp, mode)
        os.replace(tmp, dst)
    finally:
        try:
            os.unlink(tmp)
        except FileNotFoundError:
            pass


def restore(backup_dir: str, restore_binary: bool) -> None:
    if restore_binary:
        install_file(os.path.join(backup_dir, "agent-go"), LIVE, 0o700)
    config = os.path.join(backup_dir, "config.json")
    if os.path.exists(config):
        run("chflags", "nouchg", CONFIG, check=False)
        install_file(config, CONFIG, 0o600)
        if os.path.exists(config + ".immutable"):
            run("chflags", "uchg", CONFIG)
    saved_path = os.path.join(backup_dir, "config-toml-path")
    codex_backup = os.path.join(backup_dir, "config.toml")
    if os.path.exists(saved_path) and os.path.exists(codex_backup):
        with open(saved_path, encoding="utf-8") as handle:
            codex_config = handle.read()
        run("chflags", "nouchg", codex_config, check=False)
        install_file(codex_backup, codex_config, 0o600)
        if os.path.exists(codex_backup + ".immutable"):
            run("chflags", "uchg", codex_config)


def relock_reviewed_files(backup_dir: str, codex_config: str | None) -> None:
    if os.path.exists(os.path.join(backup_dir, "config.json.immutable")):
        run("chflags", "uchg", CONFIG)
    if codex_config and os.path.exists(os.path.join(backup_dir, "config.toml.immutable")):
        run("chflags", "uchg", codex_config)


def wait_stopped(pid: str, timeout: float = 15.0) -> None:
    deadline = time.time() + timeout
    while pid and time.time() < deadline:
        if run("ps", "-p", pid, check=False).returncode:
            return
        time.sleep(0.2)
    if pid and not run("ps", "-p", pid, check=False).returncode:
        raise RuntimeError(f"process {pid} did not stop")


def sha256(path: str) -> str:
    digest = hashlib.sha256()
    with open(path, "rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--staged")
    parser.add_argument("--version", required=True)
    parser.add_argument("--config")
    parser.add_argument("--restart-only", action="store_true")
    args = parser.parse_args()
    if not args.restart_only and not args.staged:
        parser.error("--staged is required unless --restart-only")
    if not VERSION_RE.fullmatch(args.version):
        parser.error("--version must be 1-128 portable label characters")
    return args


def main() -> int:
    args = parse_args()
    retained = open_retained_db()
    counts = require_idle(retained)
    errors_before = error_count(retained)
    db_identity = os.stat(DB)
    if not args.restart_only:
        preflight(args.staged)
    bootout()
    try:
        stopped_identity = os.stat(DB)
        if (db_identity.st_dev, db_identity.st_ino) != (stopped_identity.st_dev, stopped_identity.st_ino):
            raise RuntimeError("state database changed device or inode across bootout")
        require_idle(retained)
    except Exception:
        retained.close()
        bootstrap()
        raise
    retained.close()
    try:
        codex_config = reviewed_codex_config()
        backup_dir = backup(args.version, codex_config)
    except Exception:
        bootstrap()
        raise
    health_db: sqlite3.Connection | None = None
    bootstrapped = False
    replaced_binary = False
    try:
        if not args.restart_only:
            install_file(args.staged, LIVE, 0o700)
            replaced_binary = True
            if args.config:
                run("chflags", "nouchg", CONFIG, check=False)
                install_file(args.config, CONFIG, 0o600)
                if reviewed_codex_config() != codex_config:
                    raise RuntimeError("replacement config changes the reviewed CodexHome")
        run("codesign", "--verify", "--strict", LIVE)
        if not args.restart_only and requirement(LIVE) != requirement(args.staged):
            raise RuntimeError("installed designated requirement drifted")
        migrated_codex_config = reconcile_runtime_paths(LIVE)
        relock_reviewed_files(backup_dir, migrated_codex_config or codex_config)
        bootstrap()
        bootstrapped = True
        health_db = open_retained_db()
        info = wait_healthy(health_db, errors_before)
        health_db.close()
        health_db = None
        print(
            json.dumps(
                {
                    "state": "restarted" if args.restart_only else "started",
                    "backup": backup_dir,
                    "sha256": sha256(LIVE),
                    "idle": counts,
                    **info,
                }
            ),
            flush=True,
        )
    except UnsafeToRollback:
        if health_db is not None:
            health_db.close()
        raise
    except Exception:
        if health_db is not None:
            health_db.close()
            health_db = None
        if bootstrapped:
            rollback_db = open_retained_db()
            try:
                require_idle(rollback_db)
                rollback_identity = os.stat(DB)
                failed_pid = service_info().get("pid", "")
                bootout()
                wait_stopped(failed_pid)
                stopped_identity = os.stat(DB)
                if (rollback_identity.st_dev, rollback_identity.st_ino) != (stopped_identity.st_dev, stopped_identity.st_ino):
                    raise RuntimeError("state database changed device or inode before rollback")
                require_idle(rollback_db)
            except Exception as stop_error:
                rollback_db.close()
                subprocess.run(["launchctl", "bootstrap", DOMAIN, PLIST], check=False)
                raise UnsafeToRollback(
                    "automatic rollback refused because durable work changed after bootstrap"
                ) from stop_error
            rollback_db.close()
        restore(backup_dir, replaced_binary)
        subprocess.run(["launchctl", "bootstrap", DOMAIN, PLIST], check=False)
        raise
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as exc:
        print(json.dumps({"state": "failed", "error": str(exc)}), flush=True)
        raise SystemExit(1) from exc
