"""Cloudflare DNS record sync -- idempotent, safe to re-run any time (stage2
bootstrap calls it once; `slfhst dns sync` re-runs it on demand, e.g. after a
DKIM key rotation). Talks to Cloudflare's REST API with stdlib urllib only.

Mail (SMTP/IMAP) bypasses Cloudflare's proxy entirely (Cloudflare only
proxies HTTP(S), not arbitrary TCP) -- so the MX target is a separate,
UNPROXIED "smtp.<domain>" A record pointing straight at the origin, distinct
from the proxied "mail.<domain>" record used for the Bulwark webmail UI.
"""
from __future__ import annotations

import json
import urllib.error
import urllib.request

from . import common
from .common import log

API_BASE = "https://api.cloudflare.com/client/v4"
TRACE_URL = "https://www.cloudflare.com/cdn-cgi/trace"


def public_ip() -> str:
    with urllib.request.urlopen(TRACE_URL, timeout=10) as resp:
        for line in resp.read().decode().splitlines():
            if line.startswith("ip="):
                return line.split("=", 1)[1]
    raise SystemExit("could not determine public IP via cloudflare trace")


def _request(method: str, path: str, token: str, body: dict | None = None) -> dict:
    req = urllib.request.Request(
        f"{API_BASE}{path}",
        method=method,
        data=json.dumps(body).encode() if body is not None else None,
        headers={
            "Authorization": f"Bearer {token}",
            "Content-Type": "application/json",
        },
    )
    try:
        with urllib.request.urlopen(req, timeout=15) as resp:
            return json.loads(resp.read())
    except urllib.error.HTTPError as e:
        log.error("cloudflare API %s %s -> %s: %s", method, path, e.code, e.read().decode())
        raise


def _zone_id(domain: str, token: str) -> str:
    result = _request("GET", f"/zones?name={domain}", token)
    zones = result.get("result") or []
    if not zones:
        raise SystemExit(f"no Cloudflare zone found for {domain} -- is it on this account?")
    return zones[0]["id"]


def _existing_records(zone_id: str, token: str) -> list[dict]:
    result = _request("GET", f"/zones/{zone_id}/dns_records?per_page=200", token)
    return result.get("result") or []


def _upsert(zone_id: str, token: str, existing: list[dict], *, type_: str, name: str,
            content: str, proxied: bool = False, priority: int | None = None) -> None:
    body = {"type": type_, "name": name, "content": content, "ttl": 1, "proxied": proxied}
    if priority is not None:
        body["priority"] = priority

    match = next((r for r in existing if r["type"] == type_ and r["name"] == name
                  and (type_ != "MX" or r.get("priority") == priority)), None)
    if match:
        if match["content"] == content and match.get("proxied", False) == proxied:
            return
        _request("PUT", f"/zones/{zone_id}/dns_records/{match['id']}", token, body)
        log.info("updated %s %s", type_, name)
    else:
        _request("POST", f"/zones/{zone_id}/dns_records", token, body)
        log.info("created %s %s", type_, name)


def sync(cfg: dict) -> None:
    domain = cfg["domain"]
    token = cfg["cloudflare_api_token"]
    alert_email = cfg.get("alert_email", "")
    ip = public_ip()

    zone_id = _zone_id(domain, token)
    existing = _existing_records(zone_id, token)

    # Proxied (orange-cloud) web records -- reachable only from Cloudflare's
    # IPs per firewall.py.
    for sub in ("mail", "vault", "photos", "auth", "locker", "api"):
        _upsert(zone_id, token, existing, type_="A", name=f"{sub}.{domain}", content=ip, proxied=True)

    # Unproxied (grey-cloud) mail origin -- SMTP/IMAP go straight to the box.
    _upsert(zone_id, token, existing, type_="A", name=f"smtp.{domain}", content=ip, proxied=False)
    _upsert(zone_id, token, existing, type_="MX", name=domain, content=f"smtp.{domain}",
            proxied=False, priority=10)
    _upsert(zone_id, token, existing, type_="TXT", name=domain, content='"v=spf1 mx ~all"')
    if alert_email:
        _upsert(zone_id, token, existing, type_="TXT", name=f"_dmarc.{domain}",
                content=f'"v=DMARC1; p=quarantine; rua=mailto:{alert_email}"')

    dkim = _stalwart_dkim_txt()
    if dkim:
        _upsert(zone_id, token, existing, type_="TXT", name=f"default._domainkey.{domain}", content=dkim)
    else:
        log.warning("Stalwart DKIM public key not wired up yet (open item) -- "
                     "add default._domainkey TXT manually until this is automated")


def _stalwart_dkim_txt() -> str | None:
    """Placeholder: Stalwart can emit its own DKIM public key, but exactly
    how to pull it programmatically at this point in the pipeline is an open
    item (see plan doc) -- not guessing at a fake key here."""
    return None


def main() -> None:
    common.require_done_or_exit("stage1")
    sync(common.load_config())


if __name__ == "__main__":
    main()
