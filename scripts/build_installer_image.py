#!/usr/bin/python3
"""podman build the slfhst installer image (Containerfile.installer).

Must run AFTER scripts/build_image.py has already built the appliance
image (localhost/slfhst:latest) -- this saves that exact image to an
oci-archive tarball in the build context first, so the installer embeds
precisely what it's FROM, never a stale or mismatched copy.
"""
import subprocess
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
APPLIANCE_IMAGE_REF = "localhost/slfhst:latest"
INSTALLER_IMAGE_REF = "localhost/slfhst-installer:latest"
APPLIANCE_TAR = REPO_ROOT / "appliance.tar"


def main() -> None:
    subprocess.run(
        ["podman", "save", "--format", "oci-archive", "-o", str(APPLIANCE_TAR), APPLIANCE_IMAGE_REF],
        check=True,
    )
    try:
        subprocess.run(
            ["podman", "build", "-t", INSTALLER_IMAGE_REF,
             "-f", str(REPO_ROOT / "Containerfile.installer"), str(REPO_ROOT)],
            check=True,
        )
    finally:
        APPLIANCE_TAR.unlink(missing_ok=True)


if __name__ == "__main__":
    sys.exit(main())
