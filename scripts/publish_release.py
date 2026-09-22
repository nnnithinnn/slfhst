#!/usr/bin/python3
"""Publish a GitHub Release for a built ISO as a direct release asset.

GitHub hard-caps a single release asset at 2GiB. The ISO comfortably fits
under that -- measured at ~942MB for a real build (the Anaconda live
installer environment and our embedded appliance image are both ~1.1-1.2GB
individually, but bootc-image-builder's composefs/ostree-based storage
deduplicates the large amount of file content they share, since both are
built from overlapping EL10 packages). Still fails loudly rather than
silently splitting/compressing if a future build ever grows past the
limit, so that stays a visible decision, not a workaround.
"""
from __future__ import annotations

import argparse
import subprocess
from pathlib import Path

GITHUB_ASSET_LIMIT = 2_147_483_648


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--tag", required=True)
    parser.add_argument("--image", required=True, help="bootc image ref, e.g. ghcr.io/owner/slfhst:v0.1.0")
    parser.add_argument("--iso-path", required=True, type=Path)
    parser.add_argument("--repo", required=True, help="owner/repo for the GitHub Release")
    args = parser.parse_args()

    size = args.iso_path.stat().st_size
    if size >= GITHUB_ASSET_LIMIT:
        raise SystemExit(
            f"{args.iso_path} is {size / 1e9:.2f}GB, at/over GitHub's 2GiB "
            "release-asset limit. Debloat further or revisit distribution."
        )

    notes = (
        f"Bootc image: `{args.image}`.\n\n"
        f"Generic Anaconda installer ISO ({size / 1e6:.0f}MB) -- see README "
        f"for the first-boot wizard flow.\n"
    )

    subprocess.run(
        ["gh", "release", "create", args.tag, str(args.iso_path),
         "--title", f"slfhst {args.tag}",
         "--notes", notes,
         "--repo", args.repo],
        check=True,
    )


if __name__ == "__main__":
    main()
