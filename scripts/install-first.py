#!/usr/bin/env python3
"""Prepare a new signed user installation; never replace or start a service."""
import argparse
import os
from pathlib import Path
import plistlib
import re
import subprocess
import sys
import tempfile

LABEL = "ai.teslashibe.agent"
ROOT = Path(__file__).resolve().parent.parent


def run(*args, **kwargs):
    return subprocess.run(args, check=True, **kwargs)


def install(home, identity, codex_home):
    if sys.platform != "darwin":
        raise ValueError("first installation requires macOS")
    if os.getuid() == 0:
        raise ValueError("run as the logged-in user, not root")
    if not re.fullmatch(r"[0-9a-fA-F]{40}", identity):
        raise ValueError("supply an existing code-signing identity's 40-digit SHA-1; never ad-hoc sign")
    binary = home / ".local/bin/agent-go"
    runtime = home / ".local/share/agent-go"
    plist = home / "Library/LaunchAgents" / (LABEL + ".plist")
    for path in (
        binary,
        plist,
        runtime / "state.db",
        runtime / "signing.keychain-db",
        runtime / "deploy-secret.pbkdf2",
        runtime / "ship.pin",
    ):
        if os.path.lexists(path):
            raise ValueError(f"existing installation artifact: {path}; use a reviewed update path")
    service = subprocess.run(("launchctl", "print", f"gui/{os.getuid()}/{LABEL}"),
                             stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    if service.returncode == 0:
        raise ValueError("service is already loaded; refusing first installation")
    identities = run("security", "find-identity", "-v", "-p", "codesigning",
                     capture_output=True, text=True).stdout
    if identity.upper() not in identities.upper():
        raise ValueError("requested signing identity/private key is unavailable; stop and provision it separately")
    config = runtime / "config.json"
    if not config.is_file():
        raise ValueError(f"write and review {config} first (see README)")
    if runtime.stat().st_mode & 0o077 or config.stat().st_mode & 0o077:
        raise ValueError("runtime directory must be private (700), and config private (600)")
    if not codex_home.is_absolute() or not codex_home.is_dir():
        raise ValueError("CODEX_HOME must be an existing absolute directory authenticated separately")
    # No launchctl bootstrap, privacy changes, keychain changes, or helper installs.
    with tempfile.TemporaryDirectory(prefix="agent-go-first-") as tmp:
        staged = Path(tmp) / "agent-go"
        env = dict(os.environ, GOWORK="off")
        run("go", "build", "-trimpath", "-o", str(staged), "./cmd/agent-go", cwd=ROOT, env=env)
        run("codesign", "--force", "--sign", identity, "--identifier", LABEL, str(staged))
        run("codesign", "--verify", "--strict", str(staged))
        # Reject ad-hoc signatures and verify the requested certificate, not just validity.
        run("codesign", "--verify", "--strict", "-R",
            f'identifier "{LABEL}" and certificate leaf = H"{identity}"', str(staged))
        run("codesign", "-d", "-r-", str(staged))
        document = {
            "Label": LABEL,
            "ProgramArguments": [str(binary), "-config", str(config)],
            "RunAtLoad": True,
            "KeepAlive": True,
            "StandardOutPath": str(runtime / "stdout.log"),
            "StandardErrorPath": str(runtime / "stderr.log"),
            "EnvironmentVariables": {
                "CODEX_HOME": str(codex_home),
                "PATH": os.environ.get("PATH", "/usr/bin:/bin:/usr/sbin:/sbin"),
            },
        }
        created = []
        try:
            for destination, data, mode in (
                (binary, staged.read_bytes(), 0o700),
                (plist, plistlib.dumps(document), 0o600),
            ):
                destination.parent.mkdir(parents=True, exist_ok=True)
                # Stage on the destination filesystem and publish without overwriting,
                # including if another installer races the preflight checks.
                with tempfile.NamedTemporaryFile(dir=destination.parent, delete=False) as output:
                    temp_path = Path(output.name)
                    try:
                        output.write(data)
                        output.flush()
                        os.fchmod(output.fileno(), mode)
                        os.link(temp_path, destination)
                        created.append(destination)
                    finally:
                        temp_path.unlink()
        except Exception:
            for path in reversed(created):
                path.unlink()
            raise
    print(f"Prepared {binary} and {plist}; service NOT started.")
    print("Review permissions and test this installed binary in the foreground before bootstrapping launchd.")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--identity", required=True, help="existing user-owned code-signing certificate SHA-1")
    parser.add_argument("--codex-home", required=True, type=Path, help="absolute authenticated Codex home")
    args = parser.parse_args()
    try:
        install(Path.home(), args.identity, args.codex_home)
    except (ValueError, OSError, subprocess.CalledProcessError) as error:
        parser.exit(1, f"first install refused/failed: {error}\n")


if __name__ == "__main__":
    main()
