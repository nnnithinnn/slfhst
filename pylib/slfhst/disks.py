"""Stage1: find and prepare the bulk data disk.

Anaconda's kickstart (autopart --type=plain, see kickstart/generic.ks)
already partitioned, formatted, and installed the appliance onto whichever
single disk its own candidate-disk logic picked -- we deliberately don't
tell it which one, and --type=plain (not lvm) means it can't have spanned
multiple disks even on a multi-disk box. So at first boot there is exactly
one job left: find whichever disk isn't the one that ended up as root
(discovered dynamically via `findmnt`, not assumed), and turn it into
/srv/data. Nothing here assumes a specific disk size or device name --
that's what keeps the image generic, and it never needed to coordinate
with anything the kickstart does.
"""
from __future__ import annotations

import json
from pathlib import Path

from . import common
from .common import log, run

DATA_MOUNT = common.DATA_DIR
SERVICE_SUBDIRS = ("stalwart", "vaultwarden", "garage", "traefik", "ente")


def _lsblk() -> list[dict]:
    out = run(
        ["lsblk", "-J", "-b", "-o", "NAME,PATH,SIZE,TYPE,MOUNTPOINT,PKNAME"],
        capture=True,
    ).stdout
    return json.loads(out)["blockdevices"]


def _root_disk_path() -> str:
    src = run(["findmnt", "-no", "SOURCE", "/"], capture=True).stdout.strip()
    pk = run(["lsblk", "-no", "PKNAME", src], capture=True).stdout.strip()
    if pk:
        return f"/dev/{pk}"
    # Already a whole-disk root (no partition), e.g. some virtio setups.
    return src


def _pick_bulk_disk() -> str:
    root_disk = _root_disk_path()
    log.info("root/boot disk is %s (left untouched)", root_disk)
    candidates = [
        d for d in _lsblk()
        if d["type"] == "disk" and d["path"] != root_disk and d["size"]
    ]
    if not candidates:
        raise SystemExit("no second block device found for /srv/data")
    candidates.sort(key=lambda d: int(d["size"]), reverse=True)
    chosen = candidates[0]["path"]
    log.info("bulk data disk is %s (%s bytes)", chosen, candidates[0]["size"])
    return chosen


def _partition_and_format(disk: str) -> str:
    run(["parted", "--script", disk, "mklabel", "gpt", "mkpart", "primary", "xfs", "0%", "100%"])
    run(["partprobe", disk])
    children = [d for d in _lsblk() if d.get("pkname") == Path(disk).name]
    if not children:
        # partprobe can race udev; re-read once more.
        run(["udevadm", "settle"])
        children = [d for d in _lsblk() if d.get("pkname") == Path(disk).name]
    if not children:
        raise SystemExit(f"partitioning {disk} did not produce a partition")
    part = children[0]["path"]
    run(["mkfs.xfs", "-f", part])
    return part


def _fstab_mount(part: str) -> None:
    uuid = run(["blkid", "-s", "UUID", "-o", "value", part], capture=True).stdout.strip()
    fstab = Path("/etc/fstab")
    line = f"UUID={uuid} {DATA_MOUNT} xfs defaults 0 2\n"
    text = fstab.read_text()
    if str(DATA_MOUNT) not in text:
        fstab.write_text(text.rstrip("\n") + "\n" + line)
    DATA_MOUNT.mkdir(parents=True, exist_ok=True)
    run(["systemctl", "daemon-reload"])
    run(["mount", str(DATA_MOUNT)])


def main() -> None:
    if run(["findmnt", str(DATA_MOUNT)], check=False, capture=True).returncode == 0:
        log.info("%s already mounted, nothing to do", DATA_MOUNT)
    else:
        disk = _pick_bulk_disk()
        part = _partition_and_format(disk)
        _fstab_mount(part)

    for name in SERVICE_SUBDIRS:
        (DATA_MOUNT / name).mkdir(parents=True, exist_ok=True)

    common.CONTAINERS_DIR.mkdir(parents=True, exist_ok=True)


if __name__ == "__main__":
    main()
