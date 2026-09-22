"""firewalld setup: default-deny, 443->8443 forward restricted to Cloudflare
IPs, mail ports forwarded to fixed unprivileged container ports and open to
everyone, SSH open to everyone. Reused by stage1 (initial setup) and by
`slfhst cf-ips sync` (periodic Cloudflare range refresh) -- see monitor.py.
"""
from __future__ import annotations

import urllib.request

from . import common
from .common import log, run

CF_IPV4_URL = "https://www.cloudflare.com/ips-v4"
CF_IPV6_URL = "https://www.cloudflare.com/ips-v6"

CF_IPSET_V4 = "cloudflare-v4"
CF_IPSET_V6 = "cloudflare-v6"

# (public port, container-side unprivileged port) -- forwarded via firewalld
# rather than lowering net.ipv4.ip_unprivileged_port_start globally.
#
# No plaintext/STARTTLS submission(587)/imap(143) forwards: Stalwart v0.16
# stopped creating those listeners by default (implicit-TLS-only aligns
# with the PACC autoconfig draft) and stalwart.container.tmpl only
# publishes the matching implicit-TLS ports -- see that file's comment.
WEB_FORWARD = (443, 8443)
MAIL_FORWARDS = (
    (25, 2525),    # SMTP (MX delivery -- plaintext-by-default is normal
                   # here, STARTTLS is opportunistic on this same listener)
    (465, 4465),   # SMTPS (submissions, implicit TLS)
    (993, 4993),   # IMAPS (imaps, implicit TLS)
)
MAIL_DIRECT_PORTS = (4190,)  # ManageSieve, already unprivileged


def _fc(*args: str, check: bool = True) -> str:
    return run(["firewall-cmd", "--permanent", *args], capture=True, check=check).stdout.strip()


def _fetch_cf_ranges(url: str) -> list[str]:
    with urllib.request.urlopen(url, timeout=10) as resp:
        return [line.strip() for line in resp.read().decode().splitlines() if line.strip()]


def _ensure_ipset(name: str, family: str) -> None:
    existing = run(["firewall-cmd", "--permanent", "--get-ipsets"], capture=True).stdout.split()
    if name not in existing:
        _fc("--new-ipset", name, "--type=hash:net", f"--option=family={family}")


def sync_cloudflare_ipsets() -> None:
    """(Re)populate the Cloudflare ipsets from their published ranges. Safe
    to call any time -- Cloudflare's ranges do change occasionally."""
    _ensure_ipset(CF_IPSET_V4, "inet")
    _ensure_ipset(CF_IPSET_V6, "inet6")

    for name, url in ((CF_IPSET_V4, CF_IPV4_URL), (CF_IPSET_V6, CF_IPV6_URL)):
        ranges = _fetch_cf_ranges(url)
        current = set(run(["firewall-cmd", "--permanent", f"--ipset={name}", "--get-entries"],
                           capture=True).stdout.split())
        wanted = set(ranges)
        for stale in current - wanted:
            _fc(f"--ipset={name}", f"--remove-entry={stale}")
        for new in wanted - current:
            _fc(f"--ipset={name}", f"--add-entry={new}")
    run(["firewall-cmd", "--reload"])
    log.info("Cloudflare ipsets synced (%d v4, %d v6 ranges)",
              len(run(["firewall-cmd", f"--ipset={CF_IPSET_V4}", "--get-entries"], capture=True).stdout.split()),
              len(run(["firewall-cmd", f"--ipset={CF_IPSET_V6}", "--get-entries"], capture=True).stdout.split()))


def setup() -> None:
    # Default zone denies everything not explicitly allowed below.
    _fc("--add-service=ssh")

    for pub, priv in MAIL_FORWARDS:
        _fc(f"--add-forward-port=port={pub}:proto=tcp:toport={priv}")
        _fc(f"--add-port={pub}/tcp")
    for port in MAIL_DIRECT_PORTS:
        _fc(f"--add-port={port}/tcp")

    pub, priv = WEB_FORWARD
    _fc(f"--add-forward-port=port={pub}:proto=tcp:toport={priv}")
    # Deliberately NOT --add-port for 443/8443: only the rich rules below
    # (scoped to the Cloudflare ipsets) permit reaching it.
    sync_cloudflare_ipsets()
    for name, family in ((CF_IPSET_V4, "ipv4"), (CF_IPSET_V6, "ipv6")):
        _fc("--add-rich-rule",
            f'rule family="{family}" source ipset="{name}" port port="{priv}" protocol="tcp" accept')

    run(["firewall-cmd", "--reload"])
    log.info("firewalld configured: ssh open, mail ports open+forwarded, "
              "443->8443 restricted to Cloudflare IPs")


def main() -> None:
    setup()


if __name__ == "__main__":
    main()
