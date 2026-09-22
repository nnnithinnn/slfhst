#!/usr/bin/python3
"""Publish a GitHub Release for a built ISO as a direct release asset.

Rolling by design: `--tag latest` (what publish.yml always passes -- see
README's "Publishing" section) means each run replaces the previous
release under the same tag rather than accumulating one per push, so an
offline install always grabs the ISO that matches the image built in the
same run. `gh release create` errors on a tag that already has a release,
so an existing one (release + its git tag) is deleted first.

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


def _release_exists(tag: str, repo: str) -> bool:
    return subprocess.run(
        ["gh", "release", "view", tag, "--repo", repo],
        capture_output=True,
    ).returncode == 0


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--tag", required=True)
    parser.add_argument("--image", required=True, help="bootc image ref, e.g. ghcr.io/owner/slfhst:20260922-abcdef123456")
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
        f"for the first-boot wizard flow. Rebuilt on every push to main; "
        f"always the current appliance image.\n"
    )

    if _release_exists(args.tag, args.repo):
        subprocess.run(
            ["gh", "release", "delete", args.tag, "--repo", args.repo,
             "--yes", "--cleanup-tag"],
            check=True,
        )

    subprocess.run(
        ["gh", "release", "create", args.tag, str(args.iso_path),
         "--title", f"slfhst ({args.tag})",
         "--notes", notes,
         "--repo", args.repo],
        check=True,
    )


if __name__ == "__main__":
    main()
