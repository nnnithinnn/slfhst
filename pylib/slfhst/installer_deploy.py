"""Custom installer: deploy the appliance image onto the prepared root
filesystem (installer_disks.py) via `bootc install to-filesystem`, then
reboot into it.

Runs entirely offline -- the appliance image is embedded in the installer
image itself as an oci-archive (see Containerfile.installer), not pulled
from GHCR, so a fresh install always gets exactly the image that was baked
into the ISO it booted from, no network required during install. GHCR is
still what the installed system's own `bootc upgrade`/
slfhst-bootc-update.timer tracks afterward (--target-imgref below).

No kexec: real research this session found no supported way to kexec
directly into a fresh bootc/ostree deployment -- GRUB2+bootupd's Boot Loader
Spec entry selection and ostree/composefs deployment activation are real
bootloader-time logic, not something a raw kexec preserves. A real reboot
is what actually works.

`bootc install to-filesystem`'s exact flags are the least-verified part of
this whole redesign (no hands-on test in this sandbox) -- confirm against
a real `bootc install to-filesystem --help` and a real install before
trusting this blindly.
"""
from __future__ import annotations

from pathlib import Path

from . import installer_disks
from .common import log, run

EMBEDDED_IMAGE = Path("/usr/share/slfhst/appliance.tar")
TARGET_IMGREF = "ghcr.io/nnnithinnn/slfhst:stable"


def _uuid(part: str) -> str:
    return run(["blkid", "-s", "UUID", "-o", "value", part], capture=True).stdout.strip()


def deploy(root_partition: str, boot_partition: str, target: Path) -> None:
    run([
        "bootc", "install", "to-filesystem",
        "--source-imgref", f"oci-archive:{EMBEDDED_IMAGE}",
        "--target-imgref", TARGET_IMGREF,
        "--root-mount-spec", f"UUID={_uuid(root_partition)}",
        "--boot-mount-spec", f"UUID={_uuid(boot_partition)}",
        "--replace", "wipe",
        str(target),
    ])


def main() -> None:
    root_partition, boot_partition, _bulk_disk = installer_disks.prepare()
    deploy(root_partition, boot_partition, installer_disks.TARGET)
    log.info("bootc deployed, rebooting into the real OS")
    run(["systemctl", "reboot"])


if __name__ == "__main__":
    main()
