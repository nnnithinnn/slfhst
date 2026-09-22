"""Stage2: one-time idempotent setup that can only happen after a
container's first start -- Garage's single-node cluster layout + bucket/key
for Ente museum, waiting for Postgres to be ready (its own migrations run
inside museum on first start), and applying Stalwart's hostname/hardening
settings via stalwart-cli (Stalwart v0.16 has no config.toml any more --
everything is a JMAP object in its datastore, set declaratively through a
`stalwart-cli apply` plan instead of a file we can edit directly).
"""
from __future__ import annotations

import json
import shutil
import time

from . import common
from .common import DATA_DIR, SERVICE_USER, log, run

GARAGE_BUCKET = "ente"
GARAGE_KEY_NAME = "museum"

# stalwart-cli ships as its own multi-arch image (stalwartlabs/cli), not
# bundled into the mail-server image -- it authenticates to Stalwart over
# the network (STALWART_URL/_USER/_PASSWORD), so it runs as a one-shot
# container on the same podman network rather than via `podman exec`.
STALWART_CLI_IMAGE = "docker.io/stalwartlabs/cli:1.0.12"
STALWART_PLAN_DIR = DATA_DIR / "stalwart" / "slfhst-plan"


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


def _stalwart_hardening_plan(mail_host: str) -> list[dict]:
    """NDJSON operations for `stalwart-cli apply`. The object/field names
    (SystemSettings.defaultHostname, Security.authBanRate/authBanPeriod/
    abuseBanRate/abuseBanPeriod, MtaInboundThrottle.key/match/rate) are
    verified against Stalwart's actual registry schema source
    (crates/registry/src/schema/structs.rs) -- not guessed. The envelope
    (@type/object/id/matchOn/value) matches stalwartlabs/cli's own README
    and its apply.rs RawOp enum. What's genuinely NOT verified: this has
    never been run against a live v0.16 instance, so treat a first real
    deploy's `stalwart-cli apply` output as the actual test, not this
    comment. Per-listener max-connections (the v0.15 hardening file's
    third piece) was deliberately dropped rather than guessed at, since a
    wrong NetworkListener upsert could blank out an existing listener's
    bind/protocol instead of just capping its connection count.
    """
    return [
        {
            "@type": "update",
            "object": "SystemSettings",
            "id": "singleton",
            "value": {"defaultHostname": mail_host},
        },
        {
            "@type": "update",
            "object": "Security",
            "id": "singleton",
            "value": {
                "authBanRate": {"count": 5, "period": 120_000},    # 5 auth failures / 2m
                "authBanPeriod": 3_600_000,                        # ban lasts 1h
                "abuseBanRate": {"count": 3, "period": 60_000},    # 3 abuse events / 1m
                "abuseBanPeriod": 3_600_000,                       # ban lasts 1h
            },
        },
        {
            "@type": "upsert",
            "object": "MtaInboundThrottle",
            "matchOn": ["description"],
            "value": {
                "slfhst_ip_burst": {
                    "enable": True,
                    "description": "slfhst: remote IP burst",
                    "key": ["remoteIp"],
                    "match": {"match": [], "else": "true"},
                    "rate": {"count": 20, "period": 60_000},        # 20/1m
                },
                "slfhst_ip_sustained": {
                    "enable": True,
                    "description": "slfhst: remote IP sustained",
                    "key": ["remoteIp"],
                    "match": {"match": [], "else": "true"},
                    "rate": {"count": 300, "period": 3_600_000},    # 300/1h
                },
            },
        },
    ]


def harden_stalwart(cfg: dict) -> None:
    """Set Stalwart's hostname + auto-ban + inbound-throttle settings via
    `stalwart-cli apply`, run as a one-shot container against Stalwart's
    JMAP API. `apply` reconciles declared state (create/update, matched by
    the given key) -- re-running the same plan is a no-op, so no separate
    idempotency bookkeeping is needed here."""
    _wait_for("stalwart")
    admin_password = cfg.get("stalwart_admin_password")
    if not admin_password:
        log.warning("no stalwart_admin_password in config, skipping hardening")
        return

    plan = _stalwart_hardening_plan(f"mail.{cfg.get('domain', '')}")
    STALWART_PLAN_DIR.mkdir(parents=True, exist_ok=True)
    plan_file = STALWART_PLAN_DIR / "plan.ndjson"
    plan_file.write_text("\n".join(json.dumps(op) for op in plan) + "\n")

    deadline = time.monotonic() + 120
    while True:
        r = run(
            [
                "podman", "run", "--rm",
                "--network", "slfhst",
                "-v", f"{STALWART_PLAN_DIR}:/work:Z",
                "-w", "/work",
                "-e", "STALWART_URL=http://stalwart:8080",
                "-e", "STALWART_USER=admin",
                "-e", f"STALWART_PASSWORD={admin_password}",
                STALWART_CLI_IMAGE,
                "apply", "--file", "plan.ndjson",
            ],
            as_user=SERVICE_USER, check=False, capture=True,
        )
        if r.returncode == 0:
            log.info("stalwart hardening plan applied (hostname, auto-ban, inbound throttles)")
            return
        if time.monotonic() >= deadline:
            log.warning("stalwart-cli apply failed after retrying: %s", r.stderr.strip())
            return
        time.sleep(5)


def main() -> None:
    common.require_done_or_exit("stage1")
    cfg = common.load_config()
    wait_for_postgres()
    bootstrap_garage()
    harden_stalwart(cfg)


if __name__ == "__main__":
    main()
