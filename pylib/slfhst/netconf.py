"""Stage1: systemd-networkd (DHCP) + systemd-resolved pinned to Quad9 DoT.

Runs generically -- no interface names are hardcoded, DHCP is the only mode
configured at first boot. A static IP, if ever needed, is a deliberate
after-the-fact `slfhst network static ...` action, not part of first boot,
since making the wizard responsible for network config would force it to run
before stage1-network instead of after -- keeping it out of the wizard is
what lets network setup stay fully unattended.
"""
from __future__ import annotations

from pathlib import Path

from . import common
from .common import log, run

NETWORKD_DIR = Path("/etc/systemd/network")
WAN_NETWORK_FILE = NETWORKD_DIR / "20-wan.network"
RESOLVED_DROPIN_DIR = Path("/etc/systemd/resolved.conf.d")
QUAD9_DROPIN = RESOLVED_DROPIN_DIR / "quad9.conf"

WAN_NETWORK = """\
[Match]
Name=en* eth*

[Network]
DHCP=yes
IPv6AcceptRA=yes

[DHCP]
UseDNS=no
"""

QUAD9_CONF = """\
[Resolve]
DNS=9.9.9.9#dns.quad9.net 2620:fe::fe#dns.quad9.net
DNSOverTLS=yes
DNSSEC=yes
FallbackDNS=
"""


def configure() -> None:
    NETWORKD_DIR.mkdir(parents=True, exist_ok=True)
    WAN_NETWORK_FILE.write_text(WAN_NETWORK)

    RESOLVED_DROPIN_DIR.mkdir(parents=True, exist_ok=True)
    QUAD9_DROPIN.write_text(QUAD9_CONF)

    resolv = Path("/etc/resolv.conf")
    stub = Path("/run/systemd/resolve/stub-resolv.conf")
    if resolv.exists() or resolv.is_symlink():
        resolv.unlink()
    resolv.symlink_to(stub)

    run(["systemctl", "restart", "systemd-networkd.service"])
    run(["systemctl", "restart", "systemd-resolved.service"])
    log.info("networkd (DHCP) + resolved (Quad9 DoT) configured")


def main() -> None:
    configure()


if __name__ == "__main__":
    main()
