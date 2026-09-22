"""Stage2: one-time idempotent setup that can only happen after a
container's first start -- Garage's single-node cluster layout + bucket/key
for Ente museum, waiting for Postgres to be ready (its own migrations run
inside museum on first start), and wiring Stalwart's hardening config in
after Stalwart's own auto-bootstrap has created its base config.toml.
"""
from __future__ import annotations

import shutil
import time

from . import common
from .common import DATA_DIR, SERVICE_USER, log, run

GARAGE_BUCKET = "ente"
GARAGE_KEY_NAME = "museum"

STALWART_CONFIG = DATA_DIR / "stalwart" / "etc" / "config.toml"
STALWART_HARDENING_INCLUDE = "/opt/stalwart-mail/etc/slfhst-hardening.toml"


def _podman_exec(container: str, *args: str, check: bool = True) -> str:
    return run(["podman", "exec", container, "garage", *args],
               as_user=SERVICE_USER, capture=True, check=check).stdout


def _wait_for(container: str, timeout: int = 120) -> None:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        r = run(["podman", "exec", container, "true"], as_user=SERVICE_USER,
                 check=False, capture=True)
        if r.returncode == 0:
            return
        time.sleep(2)
    raise SystemExit(f"{container} did not come up within {timeout}s")


def _garage_capacity() -> str:
    free_bytes = shutil.disk_usage(DATA_DIR / "garage").free
    gb = max(int(free_bytes / (1024 ** 3)) - 5, 5)  # leave 5G headroom
    return f"{gb}G"


def bootstrap_garage() -> None:
    _wait_for("garage")

    layout = _podman_exec("garage", "layout", "show")
    if "NO ROLE ASSIGNED" not in layout and "==== HEALTHY NODES ====" in layout and "capacity" in layout.lower():
        log.info("garage layout already assigned")
    else:
        node_id = _podman_exec("garage", "node", "id", "-q").strip()
        capacity = _garage_capacity()
        _podman_exec("garage", "layout", "assign", "-z", "dc1", "-c", capacity, node_id)
        _podman_exec("garage", "layout", "apply", "--version", "1")
        log.info("garage single-node layout applied (capacity=%s)", capacity)

    buckets = _podman_exec("garage", "bucket", "list")
    if GARAGE_BUCKET not in buckets:
        _podman_exec("garage", "bucket", "create", GARAGE_BUCKET)
        log.info("garage bucket %s created", GARAGE_BUCKET)

    keys = _podman_exec("garage", "key", "list")
    if GARAGE_KEY_NAME not in keys:
        _podman_exec("garage", "key", "create", GARAGE_KEY_NAME)
        _podman_exec("garage", "bucket", "allow", "--read", "--write", "--owner",
                     GARAGE_BUCKET, "--key", GARAGE_KEY_NAME)
        log.info("garage key %s created and granted on %s", GARAGE_KEY_NAME, GARAGE_BUCKET)


def wait_for_postgres() -> None:
    _wait_for("postgres")


def harden_stalwart() -> None:
    """Wire our rate-limit/auto-ban/max-connections config into Stalwart's
    own auto-generated config.toml via an `include` directive, once that
    file actually exists (Stalwart creates it on its own first start --
    quadlets.py already rendered our hardening file to disk before that,
    same timing as museum.yaml/garage.toml, but Stalwart's own bootstrap
    still has to run first). Additive and idempotent: never rewrites or
    replaces config.toml, only appends the include line if it's missing,
    and refuses to touch it at all if some *other* include directive is
    already present rather than risk producing an invalid duplicate TOML
    key (writing a proper TOML merge is out of scope for stdlib-only code).
    """
    _wait_for("stalwart")
    if not STALWART_CONFIG.exists():
        log.warning("stalwart config.toml not created yet, will retry next run")
        return

    text = STALWART_CONFIG.read_text()
    if STALWART_HARDENING_INCLUDE in text:
        log.info("stalwart hardening already wired up")
        return
    if any(line.strip().startswith("include") for line in text.splitlines()):
        log.warning("stalwart config.toml already has an include= line -- "
                     "not touching it, add %s to it manually", STALWART_HARDENING_INCLUDE)
        return

    STALWART_CONFIG.write_text(text.rstrip("\n") + f'\ninclude = ["{STALWART_HARDENING_INCLUDE}"]\n')
    run(["podman", "restart", "stalwart"], as_user=SERVICE_USER)
    log.info("stalwart hardening config wired up, restarted to apply")


def main() -> None:
    common.require_done_or_exit("stage1")
    wait_for_postgres()
    bootstrap_garage()
    harden_stalwart()


if __name__ == "__main__":
    main()
