"""Stage2: final sanity check + MOTD banner summarizing the live stack."""
from __future__ import annotations

from pathlib import Path

from . import common, monitor
from .common import log

MOTD_FILE = Path("/etc/motd.d/slfhst.motd")


def write_motd(cfg: dict) -> None:
    domain = cfg.get("domain", "<domain>")
    lines = [
        "",
        "slfhst is up. Services:",
        f"  mail    https://mail.{domain}",
        f"  vault   https://vault.{domain}",
        f"  photos  https://photos.{domain}",
        f"  auth    https://auth.{domain}",
        f"  locker  https://locker.{domain}",
        "",
        "TOTP enrollment (secret/QR/recovery codes) was shown ONLY on the",
        "physical console during setup -- it is not stored anywhere else.",
        "",
    ]
    MOTD_FILE.parent.mkdir(parents=True, exist_ok=True)
    MOTD_FILE.write_text("\n".join(lines))


def main() -> None:
    common.require_done_or_exit("stage1")
    cfg = common.load_config()

    statuses = monitor.service_status()
    down = [name for name, state in statuses.items() if state != "active"]
    if down:
        log.warning("services not active yet (may still be starting): %s", ", ".join(down))
    else:
        log.info("all app services active")

    write_motd(cfg)


if __name__ == "__main__":
    main()
