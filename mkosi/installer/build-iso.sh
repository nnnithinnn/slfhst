#!/bin/bash
# Assembles the final bootable installer ISO from `mkosi build`'s
# directory output (mkosi/installer/mkosi.conf: Format=directory).
# Direct port of the bootc-era scripts/build_installer_iso.py's
# mksquashfs+grub2-mkrescue mechanism (LiveOS/squashfs.img layout,
# dmsquash-live kernel args), confirmed still correct this session --
# what changed is only the input (a much smaller mkosi-built installroot
# instead of the full ~1.9GB appliance image) and the dracut step's
# execution context.
#
# Run as root (needs chroot, mksquashfs, grub2-mkrescue) -- in CI, this
# runs inside the same privileged Fedora container `mkosi build` itself
# runs in, per the plan's Milestone D (same established pattern this
# project has always used for RPM-shaped build steps on a non-RPM CI
# runner: do it in a container, never on the bare runner).
#
# The dracut/dmsquash-live rebuild deliberately happens HERE, via a
# plain `chroot`, not inside an mkosi script hook -- confirmed this
# session that the exact same package set + dracut invocation succeeds
# via a direct chroot but fails inside mkosi's own sandboxed .chroot
# script execution (consistent with other nested-container sandboxing
# limitations found this session: PR_SET_MM, loop devices, the
# BuildPackages= overlay mount failure). Outside mkosi's sandbox
# entirely, this should be unaffected by that on real (non-nested) CI
# hardware either way.
set -euo pipefail

ROOT="${1:?usage: build-iso.sh <mkosi-output-directory> <iso-output-path>}"
ISO_OUT="${2:?usage: build-iso.sh <mkosi-output-directory> <iso-output-path>}"
VOLUME_LABEL=SLFHST_INSTALL

KVER="$(ls "$ROOT/usr/lib/modules" | head -n1)"
echo "kernel version: $KVER"

for fs in proc sys dev; do
    mount --bind "/$fs" "$ROOT/$fs"
done
trap 'for fs in dev sys proc; do umount "'"$ROOT"'/$fs" 2>/dev/null || true; done' EXIT

chroot "$ROOT" dracut --force --add dmsquash-live --kver "$KVER"

INITRAMFS="$ROOT/boot/initramfs-$KVER.img"
if [ ! -e "$INITRAMFS" ]; then
    echo "no dmsquash-live initramfs at $INITRAMFS" >&2
    exit 1
fi

ISO_STAGING="$(mktemp -d)"
mkdir -p "$ISO_STAGING/LiveOS" "$ISO_STAGING/boot/grub"

mksquashfs "$ROOT" "$ISO_STAGING/LiveOS/squashfs.img" -comp gzip \
    -e proc sys dev tmp run

cp "$ROOT/usr/lib/modules/$KVER/vmlinuz" "$ISO_STAGING/boot/vmlinuz"
cp "$INITRAMFS" "$ISO_STAGING/boot/initramfs.img"

cat > "$ISO_STAGING/boot/grub/grub.cfg" <<EOF
set default=0
set timeout=3
menuentry "slfhst installer" {
  linux /boot/vmlinuz root=live:CDLABEL=$VOLUME_LABEL rd.live.image quiet
  initrd /boot/initramfs.img
}
EOF

mkdir -p "$(dirname "$ISO_OUT")"
grub2-mkrescue -o "$ISO_OUT" "$ISO_STAGING" -volid "$VOLUME_LABEL"

rm -rf "$ISO_STAGING"
echo "ISO written to $ISO_OUT"
