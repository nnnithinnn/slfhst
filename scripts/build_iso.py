#!/usr/bin/python3
"""Build a qcow2 (fast local iteration) or anaconda-iso (real install media)
with bootc-image-builder, embedding kickstart/generic.ks -- see plan doc
section 10 and README for the loop: qcow2 first, only cut anaconda-iso once
that's verified.

bootc-image-builder refuses to run under rootless podman at all ("this
command must be run in rootful (not rootless) podman") -- discovered when
publish.yml's first real ISO-building run failed on exactly this. So the actual
bootc-image-builder invocation always runs via `sudo podman run`, no matter
where --image comes from.

bootc-image-builder also always needs /var/lib/containers/storage mounted
in, even for a registry reference -- it's not just how it *sees* a local
image, it's the storage bootc-image-builder itself works out of internally.
And despite --pull=newer on `podman run` (which only governs pulling the
bootc-image-builder tool image itself), bootc-image-builder does NOT pull
its target image anymore -- it expects it already present in that mounted
storage and fails with "image not known" otherwise. So a registry
reference is explicitly `sudo podman pull`ed into that same storage first.

--image defaults to the local build (localhost/slfhst:latest). scripts/
build_image.py builds that rootlessly, so it lives in the invoking user's
*rootless* storage, not root's rootful one that a sudo'd bootc-image-builder
reads from -- this script copies it across (save/load) instead of pulling.
"""
import argparse
import os
import subprocess
import sys
import tempfile
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
KICKSTART = REPO_ROOT / "kickstart" / "generic.ks"
DEFAULT_IMAGE = "localhost/slfhst:latest"


def render_config_toml(image_type: str) -> str:
    # The kickstart customization only applies to Anaconda ISO builds --
    # bootc-image-builder rejects it for qcow2 ("customizations.installer:
    # not supported"), so it's only included for anaconda-iso.
    if image_type != "anaconda-iso":
        return ""
    ks = KICKSTART.read_text()
    if '"""' in ks:
        raise SystemExit("kickstart content contains a triple-quote, can't embed in TOML as-is")
    return f'[customizations.installer.kickstart]\ncontents = """\n{ks}"""\n'


def _copy_local_image_to_rootful_storage(image: str) -> None:
    with tempfile.TemporaryDirectory() as tmp:
        tarball = Path(tmp) / "image.tar"
        subprocess.run(["podman", "save", "-o", str(tarball), image], check=True)
        subprocess.run(["sudo", "podman", "load", "-i", str(tarball)], check=True)


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--type", default="qcow2", choices=["qcow2", "anaconda-iso"])
    parser.add_argument("--output", default="output")
    parser.add_argument("--image", default=DEFAULT_IMAGE,
                         help="image to build from: a local tag (copied into rootful "
                              "storage first) or a registry reference (pulled directly)")
    args = parser.parse_args()

    config_path = REPO_ROOT / "config.generated.toml"
    config_path.write_text(render_config_toml(args.type))

    output_dir = REPO_ROOT / args.output
    output_dir.mkdir(exist_ok=True)

    if args.image.startswith("localhost/"):
        _copy_local_image_to_rootful_storage(args.image)
    else:
        subprocess.run(["sudo", "podman", "pull", args.image], check=True)

    # Always sudo: bootc-image-builder refuses to run under rootless podman
    # regardless of where the target image comes from. The storage mount is
    # also unconditional -- bootc-image-builder needs it even to pull a
    # registry ref into, not only to see a local image (see docstring).
    cmd = ["sudo", "podman", "run", "--rm", "-i", "--privileged", "--pull=newer",
           "--security-opt", "label=type:unconfined_t",
           "-v", f"{config_path}:/config.toml:ro",
           "-v", f"{output_dir}:/output",
           "-v", "/var/lib/containers/storage:/var/lib/containers/storage"]
    if sys.stdin.isatty():
        cmd.insert(cmd.index("-i") + 1, "-t")
    cmd += ["quay.io/centos-bootc/bootc-image-builder:latest", "--type", args.type, args.image]

    subprocess.run(cmd, check=True)

    # The run above was root's; hand the output back to whoever invoked us.
    subprocess.run(["sudo", "chown", "-R", f"{os.getuid()}:{os.getgid()}", str(output_dir)], check=True)


if __name__ == "__main__":
    sys.exit(main())
