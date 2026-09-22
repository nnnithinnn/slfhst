#!/usr/bin/python3
"""podman build the slfhst bootc appliance image.

Deliberately NOT --squash-all: it broke real installs. bootc/ostree's
container-image deploy relies on a `ostree.final-diffid` label identifying
the base image's own OSTree content layer; squashing every layer (base
image included) into one destroys that label, and `ostree container image
deploy` then fails outright ("Missing ostree.final-diffid") -- confirmed
against a real install, not theorized. This was previously used to reclaim
space the Containerfile's package-removal RUN steps couldn't otherwise
touch (they remove packages that ship in the BASE image's own layers --
sssd/samba/toolbox/libicu/linux-firmware/udisks2/etc. -- so unlike a
package this Containerfile installs itself, there's no "do the removal in
the same layer as the install" fix available; the bytes are physically in
a layer we didn't create). Correctness wins over that size reclaim: the
image is back to ~1.94GB (was ~1.75GB squashed) until/unless a real
ostree-aware size-reduction path (e.g. an rpm-ostree-compose-style rebuild
instead of layering) is worth the added complexity -- see README's "Image
size" section.
"""
import subprocess
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
IMAGE_REF = "localhost/slfhst:latest"


def main() -> None:
    subprocess.run(
        ["podman", "build", "-t", IMAGE_REF,
         "-f", str(REPO_ROOT / "Containerfile"), str(REPO_ROOT)],
        check=True,
    )


if __name__ == "__main__":
    sys.exit(main())
