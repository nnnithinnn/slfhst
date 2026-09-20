"""Stage2: bootstrap the backing services Ente museum needs -- Garage's
single-node cluster layout + bucket/key, and waiting for Postgres to be
ready (its own migrations run inside museum on first start, nothing to do
here beyond making sure the container is actually up first).
"""
from __future__ import annotations

import shutil
import time

from . import common
from .common import DATA_DIR, SERVICE_USER, log, run

GARAGE_BUCKET = "ente"
GARAGE_KEY_NAME = "museum"


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


def main() -> None:
    common.require_done_or_exit("stage1")
    wait_for_postgres()
    bootstrap_garage()


if __name__ == "__main__":
    main()
