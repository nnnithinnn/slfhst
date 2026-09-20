#!/usr/bin/python3
"""Publish a release: push the built ISO to GHCR as an OCI artifact (via
oras), then create a GitHub Release whose notes link to it -- not attached
as a release asset.

GitHub hard-caps a single release asset at 2GiB ("size must be less than
2147483648", confirmed by hitting it for real), and the ISO is over that
regardless of how debloated the appliance image is (the Anaconda live
installer environment dominates the ISO's size, and that's not something
bootc-image-builder exposes any way to shrink -- see README). GHCR has no
such practical limit, and pushing via `oras` needs the same GITHUB_TOKEN
login already used for the container image, so this reuses that pattern
rather than adding a new distribution dependency.
"""
from __future__ import annotations

import argparse
import subprocess


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--tag", required=True)
    parser.add_argument("--image", required=True, help="bootc image ref, e.g. ghcr.io/owner/slfhst:v0.1.0")
    parser.add_argument("--iso-path", required=True)
    parser.add_argument("--iso-repo", required=True, help="GHCR repo for the ISO artifact, e.g. ghcr.io/owner/slfhst-iso")
    parser.add_argument("--repo", required=True, help="owner/repo for the GitHub Release")
    args = parser.parse_args()

    iso_ref = f"{args.iso_repo}:{args.tag}"
    subprocess.run(
        ["oras", "push", iso_ref, f"{args.iso_path}:application/vnd.slfhst.iso"],
        check=True,
    )

    notes = (
        f"Bootc image: `{args.image}`.\n\n"
        f"Anaconda installer ISO (published as a GHCR OCI artifact, not a "
        f"release asset -- GitHub's 2GiB release-asset limit doesn't fit "
        f"this ISO, GHCR has no such limit):\n"
        f"```\noras pull {iso_ref}\n```\n"
        f"See README for the first-boot wizard flow.\n"
    )

    subprocess.run(
        ["gh", "release", "create", args.tag,
         "--title", f"slfhst {args.tag}",
         "--notes", notes,
         "--repo", args.repo],
        check=True,
    )


if __name__ == "__main__":
    main()
