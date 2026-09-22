"""Stage2: render podman secrets + Quadlet unit templates for the app stack.

Templates live in /usr/share/slfhst/templates and use stdlib
string.Template ($VAR) substitution -- deliberately not Jinja2, to keep the
image dependency-free. `.container`/`.network` files are Quadlets (go to
~svc/.config/containers/systemd/); the bundling `slfhst.target` is a plain
unit (goes to ~svc/.config/systemd/user/).
"""
from __future__ import annotations

import pwd
from pathlib import Path
from string import Template

from . import common
from .common import SERVICE_USER, TEMPLATES_DIR, log, run

QUADLET_SUFFIXES = (".container", ".network", ".volume")
PLAIN_UNIT_SUFFIXES = (".target",)

# Plain config files that aren't quadlets/units -- named explicitly rather
# than by suffix, since they land in service-specific data directories.
CONFIG_FILE_DESTS = {
    "traefik-dynamic.yml": ("traefik", "dynamic"),
    "museum.yaml": ("ente",),
    "garage.toml": ("garage",),
    "stalwart-hardening.toml": ("stalwart", "etc"),
}

SECRET_KEYS = (
    "cloudflare_api_token",
    "vaultwarden_admin_token",
    "postgres_password",
    "garage_rpc_secret",
    "garage_admin_token",
)
SECRET_ENV_NAMES = {
    "cloudflare_api_token": "cf_api_token",
    "vaultwarden_admin_token": "vaultwarden_admin_token",
    "postgres_password": "postgres_password",
    "garage_rpc_secret": "garage_rpc_secret",
    "garage_admin_token": "garage_admin_token",
}


def _home() -> Path:
    return Path(pwd.getpwnam(SERVICE_USER).pw_dir)


def _quadlet_dir() -> Path:
    return _home() / ".config" / "containers" / "systemd"


def _user_unit_dir() -> Path:
    return _home() / ".config" / "systemd" / "user"


def _secret_exists(name: str) -> bool:
    return run(["podman", "secret", "inspect", name], as_user=SERVICE_USER,
               check=False, capture=True).returncode == 0


def ensure_secrets(cfg: dict) -> None:
    for key in SECRET_KEYS:
        value = cfg.get(key)
        if not value:
            log.warning("no value for secret %s, skipping", key)
            continue
        name = SECRET_ENV_NAMES[key]
        if _secret_exists(name):
            continue
        run(["podman", "secret", "create", name, "-"], as_user=SERVICE_USER, input_text=value)
        log.info("created podman secret %s", name)


def _template_vars(cfg: dict) -> dict:
    domain = cfg.get("domain", "")
    return {
        "DOMAIN": domain,
        "MAIL_HOST": f"mail.{domain}",
        "VAULT_HOST": f"vault.{domain}",
        "PHOTOS_HOST": f"photos.{domain}",
        "AUTH_HOST": f"auth.{domain}",
        "LOCKER_HOST": f"locker.{domain}",
        "API_HOST": f"api.{domain}",
        "ALERT_EMAIL": cfg.get("alert_email", ""),
        "POSTGRES_PASSWORD": cfg.get("postgres_password", ""),
        "GARAGE_ADMIN_TOKEN": cfg.get("garage_admin_token", ""),
        "GARAGE_RPC_SECRET": cfg.get("garage_rpc_secret", ""),
        "ENTE_ENCRYPTION_KEY": cfg.get("ente_encryption_key", ""),
        "ENTE_ENCRYPTION_HASH_KEY": cfg.get("ente_encryption_hash_key", ""),
        "ENTE_JWT_SECRET": cfg.get("ente_jwt_secret", ""),
    }


def render_all(cfg: dict) -> None:
    quadlet_dir = _quadlet_dir()
    unit_dir = _user_unit_dir()
    quadlet_dir.mkdir(parents=True, exist_ok=True)
    unit_dir.mkdir(parents=True, exist_ok=True)

    tvars = _template_vars(cfg)
    for tmpl_path in sorted(TEMPLATES_DIR.glob("*.tmpl")):
        dest_name = tmpl_path.stem  # strip .tmpl
        suffix = Path(dest_name).suffix
        if dest_name in CONFIG_FILE_DESTS:
            dest_dir = common.DATA_DIR.joinpath(*CONFIG_FILE_DESTS[dest_name])
            dest_dir.mkdir(parents=True, exist_ok=True)
            dest = dest_dir / dest_name
        elif suffix in QUADLET_SUFFIXES:
            dest = quadlet_dir / dest_name
        elif suffix in PLAIN_UNIT_SUFFIXES:
            dest = unit_dir / dest_name
        else:
            log.warning("unknown template, skipping: %s", tmpl_path)
            continue
        rendered = Template(tmpl_path.read_text()).substitute(tvars)
        dest.write_text(rendered)

    run(["chown", "-R", f"{SERVICE_USER}:{SERVICE_USER}", str(_home() / ".config")])
    run(["chown", "-R", f"{SERVICE_USER}:{SERVICE_USER}", str(common.DATA_DIR)])
    run(["machinectl", "shell", f"{SERVICE_USER}@", "/usr/bin/systemctl", "--user", "daemon-reload"])
    log.info("quadlets rendered to %s", quadlet_dir)


def main() -> None:
    common.require_done_or_exit("stage1")
    cfg = common.load_config()
    ensure_secrets(cfg)
    render_all(cfg)


if __name__ == "__main__":
    main()
