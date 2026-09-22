"""Custom installer: dynamic root+bulk disk selection and partitioning.

Runs once, in the live installer environment (a throwaway squashfs boot --
see Containerfile.installer), before `bootc install to-filesystem`
(installer_deploy.py) can run. Picks the root disk (smallest non-rotational
disk, falling back to smallest overall) and the bulk disk (largest of what's
left), partitions/formats both, and mounts the root disk's partitions under
/mnt/target.

Genuinely dynamic, unlike the old kickstart %pre attempt at the same thing
(which never actually took effect -- see git history / kickstart/generic.ks,
now deleted): this is our own Python script with full control over its own
live environment, not kickstart's fragile pre-parse-time file mutation.

The bulk disk is only partitioned+formatted here, deliberately left
unmounted -- stage1's disks.py claims it (writes /etc/fstab, mounts it)
after first real boot, same as it always has; it doesn't care whether the
partition it finds already existed or not.
"""
from __future__ import annotations

import json
from pathlib import Path

from . import disks
from .common import log, run

TARGET = Path("/mnt/target")


def _lsblk() -> list[dict]:
    out = run(
        ["lsblk", "-J", "-b", "-o", "NAME,PATH,SIZE,TYPE,ROTA,PKNAME"],
        capture=True,
    ).stdout
    return json.loads(out)["blockdevices"]


def select_disks() -> tuple[str, str]:
    """Returns (root_disk, bulk_disk)."""
    all_disks = [d for d in _lsblk() if d["type"] == "disk" and d.get("size")]
    if len(all_disks) < 2:
        raise SystemExit(f"need at least 2 disks (root + bulk), found {len(all_disks)}")

    non_rotational = [d for d in all_disks if str(d.get("rota")) == "0"]
    pool = sorted(non_rotational or all_disks, key=lambda d: int(d["size"]))
    root_disk = pool[0]["path"]

    others = [d for d in all_disks if d["path"] != root_disk]
    bulk_disk = max(others, key=lambda d: int(d["size"]))["path"]

    log.info("root disk: %s, bulk disk: %s", root_disk, bulk_disk)
    return root_disk, bulk_disk


def partition_root(disk: str) -> tuple[str, str, str]:
    """GPT: ESP (512M, fat32) + /boot (1G, xfs) + / (rest, xfs). Returns
    (esp_part, boot_part, root_part)."""
    run(["parted", "--script", disk,
         "mklabel", "gpt",
         "mkpart", "ESP", "fat32", "1MiB", "513MiB",
         "set", "1", "esp", "on",
         "mkpart", "boot", "xfs", "513MiB", "1537MiB",
         "mkpart", "root", "xfs", "1537MiB", "100%"])
    run(["partprobe", disk])
    run(["udevadm", "settle"])

    children = sorted(
        (d for d in _lsblk() if d.get("pkname") == Path(disk).name),
        key=lambda d: d["path"],
    )
    if len(children) != 3:
        raise SystemExit(f"expected 3 partitions on {disk}, got {len(children)}: {children}")
    esp, boot, root = (c["path"] for c in children)

    run(["mkfs.fat", "-F32", esp])
    run(["mkfs.xfs", "-f", boot])
    run(["mkfs.xfs", "-f", root])
    return esp, boot, root


def mount_target(esp: str, boot: str, root: str) -> None:
    TARGET.mkdir(parents=True, exist_ok=True)
    run(["mount", root, str(TARGET)])
    (TARGET / "boot").mkdir(exist_ok=True)
    run(["mount", boot, str(TARGET / "boot")])
    (TARGET / "boot" / "efi").mkdir(exist_ok=True)
    run(["mount", esp, str(TARGET / "boot" / "efi")])


def prepare() -> tuple[str, str, str]:
    """Returns (root_partition, boot_partition, bulk_disk). root/boot
    partitions are mounted at TARGET; bulk_disk is partitioned+formatted
    but left unmounted for stage1's disks.py to claim after first boot."""
    root_disk, bulk_disk = select_disks()
    esp, boot, root = partition_root(root_disk)
    mount_target(esp, boot, root)
    disks.partition_and_format(bulk_disk)
    return root, boot, bulk_disk


def main() -> None:
    prepare()


if __name__ == "__main__":
    main()
