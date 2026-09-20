#!/usr/bin/python3
"""podman build the slfhst bootc appliance image.

--squash-all is not cosmetic: the Containerfile removes several base-image
packages (sssd/samba/toolbox/libicu, ~100MB) in a layer *after* the base
image's own layers. Without squashing, those bytes are still physically
present in the image (OCI layers are additive; removal just adds whiteout
markers on top) -- confirmed by measuring: 1.94GB unsquashed vs 1.84GB
squashed for the identical Containerfile. --squash alone isn't enough
either, since it only flattens layers *this build* creates, not the base
image's -- the removed packages are in the base image's own layers.
"""
import subprocess
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
IMAGE_REF = "localhost/slfhst:latest"


def main() -> None:
    subprocess.run(
        ["podman", "build", "--squash-all", "-t", IMAGE_REF,
         "-f", str(REPO_ROOT / "Containerfile"), str(REPO_ROOT)],
        check=True,
    )


if __name__ == "__main__":
    sys.exit(main())
