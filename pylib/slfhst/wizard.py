"""Stage1: interactive first-boot wizard (runs on the physical console only).

This is the ONLY place per-deployment specifics enter the system -- the image
and ISO stay generic; nothing here is baked in at build time.
"""
from __future__ import annotations

import getpass
import re

from . import common
from .common import ask, banner, log, run

VALID_PUBKEY_RE = re.compile(r"^(ssh-ed25519|ssh-rsa|ecdsa-sha2-\S+) \S+")
VALID_EMAIL_RE = re.compile(r"^[^@\s]+@[^@\s]+\.[^@\s]+$")
VALID_DOMAIN_RE = re.compile(r"^([a-z0-9-]+\.)+[a-z]{2,}$")

# Delivered to containers via `podman secret` (env injection).
SECRET_KEYS = (
    "vaultwarden_admin_token",
    "postgres_password",
    "garage_rpc_secret",
    "garage_admin_token",
    # Plain password -- quadlets.py wraps it as "admin:<password>" when
    # creating the actual podman secret, since that's the literal
    # STALWART_RECOVERY_ADMIN format Stalwart expects.
    "stalwart_admin_password",
)

# Ente museum only accepts these embedded in museum.yaml, not as env vars,
# so they're kept in config.json like the rest of the wizard's answers and
# interpolated straight into the rendered file (see quadlets.py).
FILE_SECRET_KEYS = (
    "ente_encryption_key",
    "ente_encryption_hash_key",
    "ente_jwt_secret",
)


def run_wizard() -> dict:
    banner("slfhst first-boot setup")
    print("This box will become a self-hosted mail + photos/auth/locker + vault")
    print("appliance. Answers below are only asked once.\n")

    hostname = ask("Hostname (short, e.g. box1)")
    domain = ask("Domain (must already be on Cloudflare)", validate=lambda v: VALID_DOMAIN_RE.match(v))
    admin_user = ask("Admin username", default="admin")
    pubkey = ask("Admin SSH public key (paste the full line)", validate=lambda v: VALID_PUBKEY_RE.match(v))
    alert_email = ask("Email address for alerts (delivered via this box's own mail server)",
                       validate=lambda v: VALID_EMAIL_RE.match(v))

    print("\nCloudflare API token (Zone:DNS Edit scope for the domain above).")
    print("Input is hidden.")
    cf_token = ""
    while not cf_token:
        cf_token = getpass.getpass("Cloudflare API token: ").strip()

    cfg = common.load_config()
    cfg.update(
        hostname=hostname,
        domain=domain,
        admin_user=admin_user,
        admin_pubkey=pubkey,
        alert_email=alert_email,
        # Long-lived credential, kept alongside the other secrets in the
        # root-only (0600) config.json. Turned into an actual `podman
        # secret` in stage2 once the service user + its podman storage
        # exist (see quadlets.py) -- creating it here as root would land in
        # the wrong (root) podman namespace, not the rootless `svc` one.
        cloudflare_api_token=cf_token,
    )

    print("\nPer-service secrets: leave blank to auto-generate (recommended).")
    for key in SECRET_KEYS:
        label = key.replace("_", " ")
        value = ask(f"{label} (blank = auto-generate)", required=False)
        cfg[key] = value or common.gen_secret()

    # No prompts for these -- always auto-generated, never worth typing.
    for key in FILE_SECRET_KEYS:
        cfg.setdefault(key, common.gen_secret())

    common.save_config(cfg)

    run(["hostnamectl", "set-hostname", hostname])

    banner("Setup captured. Continuing automatically (stage 2 will pull up services).")
    return cfg


def main() -> None:
    run_wizard()


if __name__ == "__main__":
    main()
