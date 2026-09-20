"""Ongoing ops checks: service health, DNSBL listing, bootc update status,
and the Cloudflare ipset refresh -- what `slfhst check-all` runs on a timer
(slfhst-monitor.timer). Anomaly-only: check_all() only emails when there's
something to look at, never on a clean run.
"""
from __future__ import annotations

import smtplib
import socket
from email.mime.text import MIMEText

from . import dns_cf, firewall
from . import common
from .common import SERVICE_USER, log, run

APP_SERVICES = (
    "traefik", "postgres", "garage", "stalwart", "bulwark", "museum",
    "ente-web-photos", "ente-web-auth", "ente-web-locker", "vaultwarden",
)

# Host-published ports to sanity-check with a plain TCP connect.
PORT_CHECKS = (8443, 2525, 4465, 4587, 4143, 4993, 4190)

DNSBL_ZONES = (
    "zen.spamhaus.org",
    "bl.spamcop.net",
    "b.barracudacentral.org",
)

# Mail relaying to send alerts through the box's own Stalwart needs an
# authenticated account provisioned in Stalwart -- not something the wizard
# collects today. Open item: wire up real credentials here; until then this
# best-effort attempts unauthenticated local submission and logs on failure
# instead of pretending it's guaranteed to work.
SMTP_HOST = "127.0.0.1"
SMTP_PORT = 2525


def service_status() -> dict[str, str]:
    status = {}
    for name in APP_SERVICES:
        r = run(["systemctl", "--user", "is-active", f"{name}.service"],
                as_user=SERVICE_USER, check=False, capture=True)
        status[name] = r.stdout.strip() or "unknown"
    return status


def port_checks() -> dict[int, bool]:
    results = {}
    for port in PORT_CHECKS:
        with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
            s.settimeout(3)
            results[port] = s.connect_ex(("127.0.0.1", port)) == 0
    return results


def dnsbl_check(ip: str) -> list[str]:
    reversed_ip = ".".join(reversed(ip.split(".")))
    listed = []
    for zone in DNSBL_ZONES:
        query = f"{reversed_ip}.{zone}"
        try:
            socket.gethostbyname(query)
            listed.append(zone)
        except socket.gaierror:
            pass
    return listed


def bootc_update_check() -> str:
    r = run(["bootc", "upgrade", "--check"], check=False, capture=True)
    return (r.stdout or "") + (r.stderr or "")


def bootc_update_apply() -> str:
    r = run(["bootc", "upgrade", "--apply"], check=False, capture=True)
    return (r.stdout or "") + (r.stderr or "")


def _send_alert(cfg: dict, subject: str, body: str) -> None:
    to_addr = cfg.get("alert_email")
    if not to_addr:
        log.warning("no alert_email configured, dropping alert: %s", subject)
        return
    msg = MIMEText(body)
    msg["Subject"] = f"[slfhst] {subject}"
    msg["From"] = f"slfhst@{cfg.get('domain', 'localhost')}"
    msg["To"] = to_addr
    try:
        with smtplib.SMTP(SMTP_HOST, SMTP_PORT, timeout=10) as smtp:
            smtp.send_message(msg)
    except OSError as e:
        log.error("could not send alert email (Stalwart relay not set up? see open items): %s", e)


def check_all() -> None:
    cfg = common.load_config()
    if not common.is_done("stage2"):
        log.info("stage2 not done yet, nothing to monitor")
        return

    issues: list[str] = []

    statuses = service_status()
    for name, state in statuses.items():
        if state != "active":
            issues.append(f"service {name} is {state}")

    ports = port_checks()
    for port, ok in ports.items():
        if not ok:
            issues.append(f"port {port} not accepting connections")

    try:
        ip = dns_cf.public_ip()
        listed = dnsbl_check(ip)
        for zone in listed:
            issues.append(f"mail IP {ip} is listed on {zone}")
    except OSError as e:
        issues.append(f"could not determine public IP for DNSBL check: {e}")

    try:
        firewall.sync_cloudflare_ipsets()
    except Exception as e:  # noqa: BLE001 -- best-effort, report and move on
        issues.append(f"cloudflare ipset refresh failed: {e}")

    update_info = bootc_update_check()

    if issues:
        body = "Issues found:\n" + "\n".join(f"- {i}" for i in issues)
        body += "\n\nbootc upgrade --check:\n" + update_info
        _send_alert(cfg, "action needed", body)
        log.warning("check-all found %d issue(s)", len(issues))
    else:
        log.info("check-all: all clear")


def main() -> None:
    check_all()


if __name__ == "__main__":
    main()
