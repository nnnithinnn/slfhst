"""The `slfhst` command -- ongoing ops CLI. Thin argparse dispatcher over
the same modules stage1/stage2 use, so there's exactly one implementation of
each check, not a duplicate "first boot" vs "day 2" version.
"""
from __future__ import annotations

import argparse
import sys

from . import common, dns_cf, firewall, monitor


def cmd_status(_args) -> None:
    for name, state in monitor.service_status().items():
        print(f"{name:20s} {state}")
    for port, ok in monitor.port_checks().items():
        print(f"port {port:<5d}        {'open' if ok else 'CLOSED'}")


def cmd_dnsbl(_args) -> None:
    ip = dns_cf.public_ip()
    listed = monitor.dnsbl_check(ip)
    if listed:
        print(f"{ip} is listed on: {', '.join(listed)}")
        sys.exit(1)
    print(f"{ip} is not listed on any checked DNSBL")


def cmd_dns_sync(_args) -> None:
    dns_cf.sync(common.load_config())


def cmd_cf_ips_sync(_args) -> None:
    firewall.sync_cloudflare_ipsets()


def cmd_update_check(_args) -> None:
    print(monitor.bootc_update_check())


def cmd_update_apply(_args) -> None:
    print(monitor.bootc_update_apply())


def cmd_check_all(_args) -> None:
    monitor.check_all()


def build_parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(prog="slfhst", description=__doc__)
    sub = p.add_subparsers(dest="command", required=True)

    sub.add_parser("status", help="service + port health").set_defaults(func=cmd_status)
    sub.add_parser("dnsbl", help="check mail IP against DNSBLs").set_defaults(func=cmd_dnsbl)

    dns = sub.add_parser("dns", help="DNS record management")
    dns_sub = dns.add_subparsers(dest="dns_command", required=True)
    dns_sub.add_parser("sync", help="sync records via Cloudflare API").set_defaults(func=cmd_dns_sync)

    cf = sub.add_parser("cf-ips", help="Cloudflare IP allowlist management")
    cf_sub = cf.add_subparsers(dest="cf_command", required=True)
    cf_sub.add_parser("sync", help="refresh the firewalld Cloudflare ipset").set_defaults(func=cmd_cf_ips_sync)

    update = sub.add_parser("update", help="bootc update management")
    update_sub = update.add_subparsers(dest="update_command", required=True)
    update_sub.add_parser("check", help="check for a bootc update").set_defaults(func=cmd_update_check)
    update_sub.add_parser("apply", help="apply a pending bootc update").set_defaults(func=cmd_update_apply)

    sub.add_parser("check-all", help="run every check, email only if something needs attention") \
        .set_defaults(func=cmd_check_all)

    return p


def main(argv: list[str] | None = None) -> None:
    parser = build_parser()
    args = parser.parse_args(argv)
    args.func(args)


if __name__ == "__main__":
    main()
