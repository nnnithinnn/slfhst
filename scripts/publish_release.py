#!/usr/bin/python3
"""Publish a GitHub Release for a built ISO as a single asset.

GitHub hard-caps a single release asset at 2GiB ("size must be less than
2147483648", confirmed by hitting it for real before the image was
debloated -- see git history). Deliberately not splitting into parts:
fails loudly instead if the ISO is ever over the limit again, so that's
visible and addressed (further debloat, or a different distribution path)
rather than silently working around it.
"""
from __future__ import annotations

import argparse
import subprocess
from pathlib import Path

GITHUB_ASSET_LIMIT = 2_147_483_648


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--tag", required=True)
    parser.add_argument("--image", required=True)
    parser.add_argument("--iso-path", required=True, type=Path)
    parser.add_argument("--repo", required=True)
    args = parser.parse_args()

    size = args.iso_path.stat().st_size
    if size >= GITHUB_ASSET_LIMIT:
        raise SystemExit(
            f"{args.iso_path} is {size / 1e9:.2f}GB, at/over GitHub's 2GiB "
            "release-asset limit. Not splitting (by design) -- debloat the "
            "image further or pick a different distribution path."
        )

    notes = f"Bootc image: `{args.image}`. Generic Anaconda installer ISO -- see README for the first-boot wizard flow.\n"

    subprocess.run(
        ["gh", "release", "create", args.tag, str(args.iso_path),
         "--title", f"slfhst {args.tag}",
         "--notes", notes,
         "--repo", args.repo],
        check=True,
    )


if __name__ == "__main__":
    main()
