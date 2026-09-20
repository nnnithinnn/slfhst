"""Stage1: create the rootless-podman service user and the human admin user."""
from __future__ import annotations

import pwd
from pathlib import Path
from textwrap import dedent

from . import common
from .common import DATA_DIR, SERVICE_USER, log, run

STORAGE_CONF_TEMPLATE = dedent("""\
    [storage]
    driver = "overlay"
    graphroot = "/var/lib/containers/storage"
    runroot = "/run/user/{uid}/containers"
    """)


def _user_exists(name: str) -> bool:
    try:
        pwd.getpwnam(name)
        return True
    except KeyError:
        return False


def _ensure_system_user(name: str) -> None:
    if _user_exists(name):
        return
    run(["useradd", "--system", "--create-home", "--shell", "/usr/sbin/nologin", name])


def _ensure_admin_user(name: str, pubkey: str) -> None:
    if not _user_exists(name):
        run(["useradd", "--create-home", "--shell", "/bin/bash", "-G", "wheel", name])
    run(["passwd", "-l", name])  # password login is disabled entirely; key+TOTP only
    home = Path(pwd.getpwnam(name).pw_dir)
    ssh_dir = home / ".ssh"
    ssh_dir.mkdir(parents=True, exist_ok=True)
    ssh_dir.chmod(0o700)
    auth_keys = ssh_dir / "authorized_keys"
    existing = auth_keys.read_text() if auth_keys.exists() else ""
    if pubkey not in existing:
        auth_keys.write_text(existing.rstrip("\n") + ("\n" if existing else "") + pubkey + "\n")
    auth_keys.chmod(0o600)
    run(["chown", "-R", f"{name}:{name}", str(ssh_dir)])


def _configure_rootless_storage(name: str) -> None:
    uid = pwd.getpwnam(name).pw_uid
    common.CONTAINERS_DIR.mkdir(parents=True, exist_ok=True)
    run(["chown", "-R", f"{name}:{name}", str(common.CONTAINERS_DIR)])
    home = Path(pwd.getpwnam(name).pw_dir)
    conf_dir = home / ".config" / "containers"
    conf_dir.mkdir(parents=True, exist_ok=True)
    (conf_dir / "storage.conf").write_text(STORAGE_CONF_TEMPLATE.format(uid=uid))
    run(["chown", "-R", f"{name}:{name}", str(home / ".config")])


def _enable_linger(name: str) -> None:
    run(["loginctl", "enable-linger", name])


def _chown_data_dirs(name: str) -> None:
    for sub in ("stalwart", "vaultwarden", "garage", "traefik", "ente"):
        d = DATA_DIR / sub
        if d.exists():
            run(["chown", "-R", f"{name}:{name}", str(d)])


def main() -> None:
    cfg = common.load_config()
    _ensure_system_user(SERVICE_USER)
    _configure_rootless_storage(SERVICE_USER)
    _enable_linger(SERVICE_USER)
    _chown_data_dirs(SERVICE_USER)

    admin_user = cfg.get("admin_user", "admin")
    pubkey = cfg.get("admin_pubkey", "")
    if not pubkey:
        raise SystemExit("no admin_pubkey in config.json -- wizard step didn't complete")
    _ensure_admin_user(admin_user, pubkey)
    run(["usermod", "-aG", "wheel", admin_user])

    log.info("service user %s and admin user %s ready", SERVICE_USER, admin_user)


if __name__ == "__main__":
    main()
