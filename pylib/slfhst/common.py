"""Shared paths, config I/O, and the subprocess helper every module uses."""
from __future__ import annotations

import json
import logging
import os
import secrets
import subprocess
import sys
from pathlib import Path
from typing import Sequence

STATE_DIR = Path("/var/lib/slfhst")
CONFIG_DIR = Path("/etc/slfhst")
CONFIG_FILE = CONFIG_DIR / "config.json"
TEMPLATES_DIR = Path("/usr/share/slfhst/templates")
DATA_DIR = Path("/srv/data")
CONTAINERS_DIR = Path("/var/lib/containers")

SERVICE_USER = "svc"

log = logging.getLogger("slfhst")
if not log.handlers:
    handler = logging.StreamHandler(sys.stderr)
    handler.setFormatter(logging.Formatter("%(levelname)s: %(message)s"))
    log.addHandler(handler)
    log.setLevel(logging.INFO)


def run(argv: Sequence[str], *, input_text: str | None = None, check: bool = True,
        capture: bool = False, as_user: str | None = None) -> subprocess.CompletedProcess:
    """Run an external command by argv (never through a shell).

    as_user=... runs it via `runuser -u <user> --` for service-user actions
    (podman, garage CLI, etc.) without needing a login shell for that user.
    """
    cmd = list(argv)
    if as_user:
        cmd = ["runuser", "-u", as_user, "--"] + cmd
    log.debug("run: %s", " ".join(cmd))
    return subprocess.run(
        cmd,
        input=input_text,
        text=True,
        check=check,
        stdout=subprocess.PIPE if capture else None,
        stderr=subprocess.PIPE if capture else None,
    )


def mark_done(name: str) -> None:
    STATE_DIR.mkdir(parents=True, exist_ok=True)
    (STATE_DIR / f"{name}.done").touch()


def is_done(name: str) -> bool:
    return (STATE_DIR / f"{name}.done").exists()


def require_done_or_exit(name: str) -> None:
    """Stage2+ steps call this first: quietly no-op if a prior stage hasn't
    finished yet, rather than relying solely on systemd unit ordering."""
    if not is_done(name):
        log.info("%s not done yet, skipping", name)
        sys.exit(0)


def load_config() -> dict:
    if not CONFIG_FILE.exists():
        return {}
    return json.loads(CONFIG_FILE.read_text())


def save_config(cfg: dict) -> None:
    CONFIG_DIR.mkdir(parents=True, exist_ok=True)
    CONFIG_FILE.write_text(json.dumps(cfg, indent=2, sort_keys=True) + "\n")
    os.chmod(CONFIG_FILE, 0o600)


def update_config(**kv) -> dict:
    cfg = load_config()
    cfg.update(kv)
    save_config(cfg)
    return cfg


def gen_secret(nbytes: int = 24) -> str:
    return secrets.token_urlsafe(nbytes)


# --- Shared interactive-console prompt helpers ------------------------------
# Used by any stage1 step that takes over the console tty (wizard.py,
# netconf.py) -- one implementation of "ask a question, validate, retry"
# rather than each script rolling its own.

def ask(prompt: str, *, default: str | None = None, validate=None, required: bool = True) -> str:
    suffix = f" [{default}]" if default else ""
    while True:
        value = input(f"{prompt}{suffix}: ").strip()
        if not value and default is not None:
            value = default
        if not value and not required:
            return ""
        if not value:
            print("  required.")
            continue
        if validate and not validate(value):
            print("  doesn't look right, try again.")
            continue
        return value


def banner(text: str) -> None:
    print()
    print("=" * 70)
    print(text)
    print("=" * 70)
