"""Stage2: pull every image the rendered quadlets reference, then start the
bundled slfhst.target. Image list is read back out of the rendered quadlet
files rather than duplicated here, so the templates stay the single source
of truth.
"""
from __future__ import annotations

import pwd
import re

from . import common
from .common import SERVICE_USER, log, run

IMAGE_RE = re.compile(r"^Image=(\S+)", re.MULTILINE)


def _quadlet_dir():
    from pathlib import Path
    return Path(pwd.getpwnam(SERVICE_USER).pw_dir) / ".config" / "containers" / "systemd"


def pull_images() -> None:
    images: set[str] = set()
    for f in _quadlet_dir().glob("*.container"):
        images.update(IMAGE_RE.findall(f.read_text()))
    for image in sorted(images):
        log.info("pulling %s", image)
        run(["podman", "pull", image], as_user=SERVICE_USER)


def start_stack() -> None:
    run(["machinectl", "shell", f"{SERVICE_USER}@", "/usr/bin/systemctl", "--user", "start", "slfhst.target"])


def main() -> None:
    common.require_done_or_exit("stage1")
    pull_images()
    start_stack()


if __name__ == "__main__":
    main()
