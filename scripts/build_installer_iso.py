#!/usr/bin/python3
"""Build the live installer ISO directly -- mksquashfs + grub2-mkrescue --
instead of bootc-image-builder, which turned out not to support this at
all: confirmed directly from the tool's own error output and source
(imagetypes.go), not assumed. Every ISO type it supports (anaconda-iso,
iso, bootc-installer) goes through Anaconda in some form; a
"bootc-generic-iso" type doesn't exist anywhere in the tool (an earlier
research pass invented it).

The squashfs/grub2-mkrescue mechanism here was verified end-to-end
locally before ever touching CI: built a real ISO this way against the
base AlmaLinux bootc image, read dmsquash-live's actual dracut module
source (not docs) to confirm the exact structure it looks for
(LiveOS/squashfs.img, root=live:CDLABEL=<label> on the kernel command
line), and confirmed with `xorriso -indev ... -find` that the built ISO
actually has the right files at the right paths.

What ISN'T verified: real boot capability. This development sandbox is
aarch64, which has no BIOS/El Torito concept at all, so the x86_64
BIOS+UEFI hybrid boot path (grub2-pc + grub2-efi-x64, see
Containerfile.installer) could only be checked mechanically (right
packages exist, right files present), not boot-tested. A real x86_64 CI
run / VM boot is the actual first test of that part.
"""
import argparse
import subprocess
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
INSTALLER_IMAGE_REF = "localhost/slfhst-installer:latest"
VOLUME_LABEL = "SLFHST_INSTALL"

# Runs as one shell script inside a privileged container built FROM the
# installer image, so mksquashfs sees that image's own real filesystem
# (not the build host's) -- /iso is excluded from its own squashfs
# source, along with the usual runtime-only pseudo-filesystems and the
# output bind-mount itself.
BUILD_SCRIPT = r"""
set -e
KVER="$(ls /lib/modules | head -n1)"
INITRAMFS="/boot/initramfs-$KVER.img"
if [ ! -e "$INITRAMFS" ]; then
    echo "no dmsquash-live initramfs at $INITRAMFS -- Containerfile.installer's dracut step didn't run or produced a different path" >&2
    exit 1
fi

mkdir -p /iso/LiveOS /iso/boot/grub
mksquashfs / /iso/LiveOS/squashfs.img -comp gzip \
    -e proc sys dev tmp run iso iso-out

cp "/usr/lib/modules/$KVER/vmlinuz" /iso/boot/vmlinuz
cp "$INITRAMFS" /iso/boot/initramfs.img

cat > /iso/boot/grub/grub.cfg <<EOF
set default=0
set timeout=3
menuentry "slfhst installer" {
  linux /boot/vmlinuz root=live:CDLABEL=__VOLUME_LABEL__ rd.live.image quiet
  initrd /boot/initramfs.img
}
EOF

grub2-mkrescue -o /iso-out/install.iso /iso -volid __VOLUME_LABEL__
""".replace("__VOLUME_LABEL__", VOLUME_LABEL)


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", default="output")
    args = parser.parse_args()

    output_dir = REPO_ROOT / args.output
    output_dir.mkdir(exist_ok=True)

    subprocess.run(
        ["podman", "run", "--rm", "--privileged",
         "-v", f"{output_dir}:/iso-out:Z",
         INSTALLER_IMAGE_REF, "sh", "-c", BUILD_SCRIPT],
        check=True,
    )


if __name__ == "__main__":
    sys.exit(main())
