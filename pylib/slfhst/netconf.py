"""Stage1: systemd-networkd (DHCP or interactive static) + systemd-resolved
pinned to Quad9 DoT.

Interactive on the console, same as the wizard -- DHCP doesn't cover every
VPS provider. Static IPv4 is fairly common; static IPv6 is *more* common
than DHCP/SLAAC for IPv6 specifically, since a lot of providers route a
handful of discrete addresses (or a whole prefix) to you directly rather
than answering router solicitations. So this asks rather than assumes,
and supports multiple static IPv6 addresses on one interface.
"""
from __future__ import annotations

import ipaddress
from pathlib import Path

from . import common
from .common import ask, banner, log, run

NETWORKD_DIR = Path("/etc/systemd/network")
WAN_NETWORK_FILE = NETWORKD_DIR / "20-wan.network"
RESOLVED_DROPIN_DIR = Path("/etc/systemd/resolved.conf.d")
QUAD9_DROPIN = RESOLVED_DROPIN_DIR / "quad9.conf"

QUAD9_CONF = """\
[Resolve]
DNS=9.9.9.9#dns.quad9.net 2620:fe::fe#dns.quad9.net
DNSOverTLS=yes
DNSSEC=yes
FallbackDNS=
"""

# Interfaces never worth offering as "the" WAN link, even if present.
_SKIP_PREFIXES = ("lo", "veth", "podman", "docker", "cni", "br-", "virbr")


def _candidate_interfaces() -> list[str]:
    out = run(["ip", "-o", "link", "show"], capture=True).stdout
    names = []
    for line in out.splitlines():
        # e.g. "2: eth0: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 ..."
        parts = line.split(":", 2)
        if len(parts) < 2:
            continue
        name = parts[1].strip().split("@")[0]
        if name.startswith(_SKIP_PREFIXES):
            continue
        names.append(name)
    return names


def _choose_interface() -> str:
    candidates = _candidate_interfaces()
    if not candidates:
        raise SystemExit("no network interfaces found")
    if len(candidates) == 1:
        log.info("using network interface %s", candidates[0])
        return candidates[0]

    print("\nMultiple network interfaces found:")
    for i, name in enumerate(candidates, 1):
        print(f"  {i}. {name}")
    while True:
        choice = input(f"Which one is the WAN link? [1-{len(candidates)}]: ").strip()
        if choice.isdigit() and 1 <= int(choice) <= len(candidates):
            return candidates[int(choice) - 1]
        print("  invalid choice.")


def _ask_cidr(prompt: str, version: int, *, required: bool = True) -> str:
    while True:
        value = input(f"{prompt}: ").strip()
        if not value:
            if required:
                print("  required.")
                continue
            return ""
        try:
            iface = ipaddress.ip_interface(value)
        except ValueError:
            print("  not a valid address/prefix (e.g. 203.0.113.5/24).")
            continue
        if iface.version != version:
            print(f"  that's not an IPv{version} address.")
            continue
        return str(iface)


def _ask_ip(prompt: str, version: int, *, required: bool = True) -> str:
    while True:
        value = input(f"{prompt}: ").strip()
        if not value:
            if required:
                print("  required.")
                continue
            return ""
        try:
            addr = ipaddress.ip_address(value)
        except ValueError:
            print("  not a valid IP address.")
            continue
        if addr.version != version:
            print(f"  that's not an IPv{version} address.")
            continue
        return str(addr)


def _ask_static() -> dict:
    print()
    ipv4 = _ask_cidr("IPv4 address (CIDR, e.g. 203.0.113.5/24) -- blank to skip IPv4",
                      4, required=False)
    ipv4_gateway = _ask_ip("IPv4 gateway", 4, required=False) if ipv4 else ""

    print("\nIPv6 addresses -- enter one at a time (CIDR, e.g. 2001:db8::5/64),")
    print("blank to stop. Most providers give one gateway for all of them.")
    ipv6_addresses: list[str] = []
    while True:
        addr = _ask_cidr(f"IPv6 address #{len(ipv6_addresses) + 1}", 6, required=False)
        if not addr:
            break
        ipv6_addresses.append(addr)
    ipv6_gateway = _ask_ip("IPv6 gateway", 6, required=False) if ipv6_addresses else ""

    if not ipv4 and not ipv6_addresses:
        print("\nNo static addresses entered -- falling back to DHCP.")
        return {}

    return {
        "ipv4_address": ipv4,
        "ipv4_gateway": ipv4_gateway,
        "ipv6_addresses": ipv6_addresses,
        "ipv6_gateway": ipv6_gateway,
    }


def _render_network_file(iface: str, static: dict) -> str:
    lines = ["[Match]", f"Name={iface}", "", "[Network]"]
    if not static:
        lines += ["DHCP=yes", "", "[DHCP]", "UseDNS=no"]
        return "\n".join(lines) + "\n"

    lines.append("DHCP=no")
    if static.get("ipv6_addresses"):
        # Static IPv6 means we're not relying on router advertisements.
        lines.append("IPv6AcceptRA=no")
    if static.get("ipv4_address"):
        lines.append(f"Address={static['ipv4_address']}")
    for addr in static.get("ipv6_addresses", []):
        lines.append(f"Address={addr}")

    for gw in (static.get("ipv4_gateway"), static.get("ipv6_gateway")):
        if gw:
            # GatewayOnLink=yes: a lot of VPS providers hand out a gateway
            # that isn't actually within the assigned prefix, which a plain
            # Gateway= would otherwise reject as unreachable.
            lines += ["", "[Route]", f"Gateway={gw}", "GatewayOnLink=yes"]

    return "\n".join(lines) + "\n"


def configure() -> None:
    banner("Network setup")
    print("DHCP works for most VPS providers. Some require static IPv4 and/or")
    print("IPv6 instead (common for IPv6 specifically, since a lot of")
    print("providers route addresses to you directly rather than doing")
    print("SLAAC/DHCPv6) -- answer 'static' below if that's your box.\n")

    iface = _choose_interface()
    mode = ask("DHCP or static?", default="dhcp",
               validate=lambda v: v.lower() in ("dhcp", "static"))
    static = _ask_static() if mode.lower() == "static" else {}

    NETWORKD_DIR.mkdir(parents=True, exist_ok=True)
    WAN_NETWORK_FILE.write_text(_render_network_file(iface, static))

    RESOLVED_DROPIN_DIR.mkdir(parents=True, exist_ok=True)
    QUAD9_DROPIN.write_text(QUAD9_CONF)

    resolv = Path("/etc/resolv.conf")
    stub = Path("/run/systemd/resolve/stub-resolv.conf")
    if resolv.exists() or resolv.is_symlink():
        resolv.unlink()
    resolv.symlink_to(stub)

    common.update_config(network=static or {"mode": "dhcp"})

    run(["systemctl", "restart", "systemd-networkd.service"])
    run(["systemctl", "restart", "systemd-resolved.service"])
    log.info("networkd (%s) + resolved (Quad9 DoT) configured on %s",
              "static" if static else "DHCP", iface)

    banner("Network configured. Continuing automatically.")


def main() -> None:
    configure()


if __name__ == "__main__":
    main()
